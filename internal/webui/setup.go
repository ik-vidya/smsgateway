// Package webui, файл setup.go — страница /setup, SSE-поток портов,
// AT-проба выбранного порта и сохранение в .env с перезапуском
// сервиса через SIGHUP.
package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
	data["EnvPath"] = s.envPath
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

	if err := writeSSEPorts(w, snap); err != nil {
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
			if err := writeSSEPorts(w, ev); err != nil {
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
// SIGHUP делает graceful shutdown и syscall.Exec("/proc/self/exe"),
// благодаря чему новый процесс читает уже обновлённый .env.
// PID сохраняется, systemd не замечает рестарт.
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
