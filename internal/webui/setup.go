// Package webui, файл setup.go — страница /setup, SSE-поток портов,
// AT-проба выбранного порта, сохранение в .env с перезапуском
// сервиса через SIGHUP и самопроверка (отправка SMS самому себе).
package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ik-vidya/smsgateway/internal/config"
	"github.com/ik-vidya/smsgateway/internal/modem"
)

const (
	setupProbeMinInterval = 100 * time.Millisecond
	setupProbeTimeout     = 15 * time.Second
	setupCmdTimeout       = 3 * time.Second
	setupReloadDelay      = 200 * time.Millisecond
	setupTestTimeout      = 90 * time.Second
)

// handleSetup — GET /setup.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.portWatcher == nil {
		http.Error(w, "port watcher not running",
			http.StatusServiceUnavailable)
		return
	}
	data := s.baseData(r, "setup")
	data["CurrentPort"] = s.cfg.SerialPort
	if resolved, err := filepath.EvalSymlinks(s.cfg.SerialPort); err == nil {
		data["ActivePortResolved"] = resolved
	} else {
		data["ActivePortResolved"] = s.cfg.SerialPort
	}
	data["EnvPath"] = s.envPath
	data["SelfNumber"] = s.cfg.SelfNumber
	s.render(w, "setup", data)
}

// handleSetupPortsStream — GET /setup/ports/stream (SSE).
func (s *Server) handleSetupPortsStream(w http.ResponseWriter, r *http.Request) {
	if s.portWatcher == nil {
		http.Error(w, "port watcher not running",
			http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported",
			http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	id, snap, ch := s.portWatcher.Subscribe()
	defer s.portWatcher.Unsubscribe(id)

	if err := writeSSEPortsEnriched(w, snap, s.cfg.SerialPort); err != nil {
		return
	}
	flusher.Flush()

	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSEPortsEnriched(w, ev, s.cfg.SerialPort); err != nil {
				return
			}
			flusher.Flush()
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSEPorts(w http.ResponseWriter, ev PortEvent) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", raw)
	return err
}

// setupProbeResponse — ответ POST /setup/probe.
type setupProbeResponse struct {
	Port         string `json:"port"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	Revision     string `json:"revision,omitempty"`
	IMEI         string `json:"imei,omitempty"`
	Error        string `json:"error,omitempty"`

	// ErrorCode — машиночитаемый код для UI. Переводится в
	// i18n на клиенте (см. setup.html data-port-busy).
	// Error остаётся английским — для API/CLI клиентов.
	ErrorCode string `json:"error_code,omitempty"`
}

// handleSetupProbe — POST /setup/probe.
func (s *Server) handleSetupProbe(w http.ResponseWriter, r *http.Request) {
	if !s.probeMu.TryLock() {
		http.Error(w, "probe already in progress", http.StatusConflict)
		return
	}
	defer s.probeMu.Unlock()

	if time.Since(s.lastProbe) < setupProbeMinInterval {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	s.lastProbe = time.Now()

	var req struct {
		Port string `json:"port"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).
		Decode(&req); err != nil {
		writeSetupErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if !validSetupPort(req.Port) {
		writeSetupErr(w, http.StatusBadRequest,
			"port must be under /dev/serial/by-id, /dev/serial/by-path, "+
				"/dev/ttyUSB* or /dev/ttyACM*, and exist")
		return
	}

	// Порт занят запущенным сервисом. Сервис открыл порт и
	// непрерывно читает байты. Если probe откроет тот же tty,
	// AT-команда уйдёт в reader сервиса, а не в probe —
	// гарантированный таймаут 3s. Подсказываем, что делать
	// вместо этого: диагностика и тест «отправка + приём» идут
	// через сервис и работают при занятом порте.
	// Резолвим симлинки: UI показывает /dev/serial/by-id/..., а
	// в .env обычно SERIAL_PORT=/dev/ttyUSB0. Это один и тот же
	// порт. Сравнивать строки бесполезно.
	reqReal, errReq := filepath.EvalSymlinks(req.Port)
	cfgReal, errCfg := filepath.EvalSymlinks(s.cfg.SerialPort)
	if errReq == nil && errCfg == nil && reqReal == cfgReal {
		writeJSONSetup(w, http.StatusConflict, setupProbeResponse{
			Port: req.Port,
			Error: "port is in use by the running service; " +
				"use diagnostics or send/receive test instead",
			ErrorCode: "port_busy",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), setupProbeTimeout)
	defer cancel()

	resp := setupProbeResponse{Port: req.Port}
	if err := probeModem(ctx, req.Port, &resp); err != nil {
		resp.Error = err.Error()
	}
	writeJSONSetup(w, http.StatusOK, resp)
}

// handleSetupSave — POST /setup/save.
//
// Принимает {"port": "..."}. Проверяет путь, пишет SERIAL_PORT в
// .env (сохраняя комментарии и остальные переменные), возвращает
// 200 и в фоне шлёт SIGHUP самому себе. main.go при получении
// SIGHUP делает graceful shutdown и syscall.Exec("/proc/self/exe").
func (s *Server) handleSetupSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Port string `json:"port"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).
		Decode(&req); err != nil {
		writeSetupErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if !validSetupPort(req.Port) {
		writeSetupErr(w, http.StatusBadRequest,
			"port must be under /dev/serial/by-id, /dev/serial/by-path, "+
				"/dev/ttyUSB* or /dev/ttyACM*, and exist")
		return
	}
	if s.envPath == "" {
		writeSetupErr(w, http.StatusInternalServerError,
			"env path not configured")
		return
	}

	applied, err := config.UpdateEnvFile(s.envPath, map[string]string{
		"SERIAL_PORT": req.Port,
	})
	if err != nil {
		s.logger.Error("webui: setup save", "err", err)
		writeSetupErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if len(applied) > 0 {
		s.logger.Info("webui: SERIAL_PORT записан в .env",
			"path", s.envPath, "value", req.Port)
	} else {
		s.logger.Info("webui: SERIAL_PORT уже совпадает, файл не менялся",
			"path", s.envPath, "value", req.Port)
	}

	// SIGHUP в фоне с задержкой: даём ответу уйти до того, как
	// main.go начнёт graceful shutdown.
	go func() {
		time.Sleep(setupReloadDelay)
		s.logger.Info("webui: отправляю SIGHUP для перезапуска")
		if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
			s.logger.Error("webui: SIGHUP", "err", err)
		}
	}()

	writeJSONSetup(w, http.StatusOK, map[string]any{
		"ok":      true,
		"port":    req.Port,
		"applied": applied,
	})
}

// handleSetupTest — POST /setup/test.
//
// Вызывает svc.SendTestSMS (синхронно, ждёт терминального статуса
// или таймаута). SendTestSMS сам уведомляет Notifier — при
// настроенном Telegram результат придёт и в бот.
func (s *Server) handleSetupTest(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		writeJSONSetup(w, http.StatusServiceUnavailable, map[string]any{
			"error": "service not configured",
		})
		return
	}
	if s.cfg.SelfNumber == "" {
		writeJSONSetup(w, http.StatusBadRequest, map[string]any{
			"error":       "SELF_NUMBER is not set in .env",
			"self_number": "",
		})
		return
	}
	if s.cfg.SerialPort == "" || s.cfg.SerialPort == "none" {
		writeJSONSetup(w, http.StatusServiceUnavailable, map[string]any{
			"error": "modem is not configured",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), setupTestTimeout)
	defer cancel()

	res, err := s.svc.SendTestSMS(ctx, "")
	if err != nil {
		s.logger.Warn("webui: selftest", "err", err)
		writeJSONSetup(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
		})
		return
	}

	s.logger.Info("webui: selftest завершён",
		"id", res.ID, "status", res.Status,
		"timed_out", res.TimedOut)

	writeJSONSetup(w, http.StatusOK, map[string]any{
		"id":          res.ID,
		"status":      string(res.Status),
		"timed_out":   res.TimedOut,
		"error":       res.Error,
		"reference":   res.Reference,
		"self_number": s.cfg.SelfNumber,
	})
}

// probeModem открывает порт и опрашивает идентификацию.
func probeModem(ctx context.Context, port string,
	out *setupProbeResponse) error {

	sp, err := modem.OpenSerial(modem.SerialConfig{
		Port: port,
		Baud: 115200,
	})
	if err != nil {
		return fmt.Errorf("открытие порта: %w", err)
	}
	defer func() { _ = sp.Close() }()

	at, err := modem.New(sp, modem.Config{
		CmdTimeout: setupCmdTimeout,
	})
	if err != nil {
		return fmt.Errorf("транспорт: %w", err)
	}
	defer func() { _ = at.Close() }()

	if _, err := at.Exec(ctx, "AT"); err != nil {
		return fmt.Errorf("AT: %w", err)
	}
	_, _ = at.Exec(ctx, "ATE0")

	out.Manufacturer = execFirstPayload(ctx, at, "AT+CGMI")
	out.Model = execFirstPayload(ctx, at, "AT+CGMM")
	out.Revision = execFirstPayload(ctx, at, "AT+CGMR")
	out.IMEI = execFirstPayload(ctx, at, "AT+CGSN")
	return nil
}

func execFirstPayload(ctx context.Context, at *modem.ATTransport,
	cmd string) string {
	resp, err := at.Exec(ctx, cmd)
	if err != nil {
		return ""
	}
	for _, line := range resp.Payload() {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// validSetupPort проверяет, что путь безопасен для probe и save.
//
// Принимаются только:
//   - /dev/serial/by-id/*
//   - /dev/serial/by-path/*
//   - /dev/ttyUSB*
//   - /dev/ttyACM*
//
// Плюс файл должен существовать. Это защита от опечатки: если
// админ по ошибке введёт /dev/ttyS0 (консоль) или /dev/mem,
// probe и save откажутся.
func validSetupPort(p string) bool {
	if p == "" {
		return false
	}
	ok := false
	for _, prefix := range []string{
		"/dev/serial/by-id/",
		"/dev/serial/by-path/",
		"/dev/ttyUSB",
		"/dev/ttyACM",
	} {
		if strings.HasPrefix(p, prefix) {
			ok = true
			break
		}
	}
	if !ok {
		return false
	}
	if _, err := os.Stat(p); err != nil {
		return false
	}
	return true
}

func writeSetupErr(w http.ResponseWriter, status int, msg string) {
	writeJSONSetup(w, status, setupProbeResponse{Error: msg})
}

func writeJSONSetup(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ==================== diagnostics + test-receive ====================

const (
	// setupDiagTimeout — таймаут GET /setup/diagnostics.
	// diagnostics.Collect ограничивает бюджет 25s, плюс запас на HTTP.
	setupDiagTimeout = 30 * time.Second

	// setupTestReceiveTimeout — общий таймаут POST /setup/test-receive.
	// SendTestSMSWithReceive укладывается в testSMSTimeout (до 120s)
	// + receiveTimeout (90s). HTTP-сервер имеет WriteTimeout=0.
	setupTestReceiveTimeout = 180 * time.Second

	// setupTestReceiveWait — сколько ждать входящее SMS.
	setupTestReceiveWait = 90 * time.Second
)

// handleSetupDiagnostics — GET /setup/diagnostics.
//
// Возвращает diagnostics.Result (JSON). Поле Raw не отдаём:
// там могут быть IMEI/IMSI/номера, а страница /setup доступна
// в сети (Web UI слушает 0.0.0.0).
//
// Rate limit — 100ms (как у probe). Lock — probeMu (модем может
// выполнять только одну длинную операцию за раз).
func (s *Server) handleSetupDiagnostics(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		writeJSONSetup(w, http.StatusServiceUnavailable, map[string]any{
			"error": "service not configured",
		})
		return
	}

	if !s.probeMu.TryLock() {
		writeJSONSetup(w, http.StatusConflict, map[string]any{
			"error": "modem is busy (probe or diagnostics already in progress)",
		})
		return
	}
	defer s.probeMu.Unlock()

	if time.Since(s.lastDiag) < setupProbeMinInterval {
		writeJSONSetup(w, http.StatusTooManyRequests, map[string]any{
			"error": "too many requests",
		})
		return
	}
	s.lastDiag = time.Now()

	ctx, cancel := context.WithTimeout(r.Context(), setupDiagTimeout)
	defer cancel()

	res, err := s.svc.RunDiagnostics(ctx)
	if err != nil {
		s.logger.Warn("webui: setup diagnostics", "err", err)
		writeJSONSetup(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
		})
		return
	}
	res.Raw = nil
	writeJSONSetup(w, http.StatusOK, res)
}

// handleSetupTestReceive — POST /setup/test-receive.
//
// Отправляет SMS на SELF_NUMBER с уникальным маркером и ждёт
// входящее с этим маркером. Если отправка прошла, а приём — нет,
// возвращает 200 с receive_timed_out=true: не ошибка сервиса,
// а сигнал о проблеме приёма.
//
// Не блокируем probeMu — sendLoop работает асинхронно,
// SendTestSMSWithReceive только ждёт статуса из БД.
func (s *Server) handleSetupTestReceive(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SelfNumber == "" {
		writeJSONSetup(w, http.StatusBadRequest, map[string]any{
			"error":       "SELF_NUMBER is not set in .env",
			"self_number": "",
		})
		return
	}
	if s.cfg.SerialPort == "" || s.cfg.SerialPort == "none" {
		writeJSONSetup(w, http.StatusServiceUnavailable, map[string]any{
			"error": "modem is not configured",
		})
		return
	}
	if s.svc == nil {
		writeJSONSetup(w, http.StatusServiceUnavailable, map[string]any{
			"error": "service not configured",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), setupTestReceiveTimeout)
	defer cancel()

	res, err := s.svc.SendTestSMSWithReceive(ctx, setupTestReceiveWait)
	if err != nil {
		s.logger.Warn("webui: test-receive", "err", err)
		writeJSONSetup(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
		})
		return
	}

	s.logger.Info("webui: test-receive завершён",
		"marker", res.Marker,
		"sent_status", res.Sent.Status,
		"sent_timed_out", res.Sent.TimedOut,
		"received", res.Received != nil,
		"receive_timed_out", res.ReceiveTimedOut)

	writeJSONSetup(w, http.StatusOK, map[string]any{
		"marker": res.Marker,
		"sent": map[string]any{
			"id":        res.Sent.ID,
			"status":    string(res.Sent.Status),
			"timed_out": res.Sent.TimedOut,
			"error":     res.Sent.Error,
			"reference": res.Sent.Reference,
		},
		"received":          res.Received,
		"receive_timed_out": res.ReceiveTimedOut,
		"receive_error":     res.ReceiveError,
		"self_number":       s.cfg.SelfNumber,
	})
}

type enrichedPort struct {
	Path     string `json:"path"`
	Resolved string `json:"resolved,omitempty"`
	Active   bool   `json:"active"`
}

type enrichedEvent struct {
	Ports []enrichedPort `json:"ports"`
	New   []string       `json:"new,omitempty"`
}

func writeSSEPortsEnriched(w http.ResponseWriter, ev PortEvent, activePort string) error {
	activeResolved, _ := filepath.EvalSymlinks(activePort)

	out := enrichedEvent{New: ev.New}
	for _, p := range ev.Ports {
		resolved, _ := filepath.EvalSymlinks(p)
		isActive := (resolved != "" && resolved == activeResolved) ||
			(p == activePort)
		out.Ports = append(out.Ports, enrichedPort{
			Path: p, Resolved: resolved, Active: isActive,
		})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", raw)
	return err
}
