// Package api, файл handlers.go — HTTP-обработчики.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ik-vidya/smsgateway/internal/storage"
)

const maxBodySize = 64 * 1024

// handleHealthz — GET /healthz.
//
// Простой healthcheck: «процесс жив». Без авторизации.
// Возвращает 200 всегда, если процесс отвечает на HTTP.
//
// Для проверки «сервис готов обслуживать запросы» (БД доступна,
// модем сконфигурирован) — используйте GET /readyz.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyCheck — результат одной проверки в /readyz.
type readyCheck struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// readyResponse — тело ответа /readyz.
type readyResponse struct {
	Status   string                `json:"status"`
	Checks   map[string]readyCheck `json:"checks,omitempty"`
	Warnings []string              `json:"warnings,omitempty"`
}

// handleReadyz — GET /readyz.
//
// Расширенный healthcheck: проверяет доступность БД и, если
// модем сконфигурирован, его регистрацию в сети.
//
// 200 OK: БД доступна. Если модем сконфигурирован, но не
// зарегистрирован в сети — это warnings (SMS встают в очередь
// и уйдут, когда сеть появится; это не повод рубить сервис).
//
// 503 Service Unavailable: БД недоступна — сервис не может
// обслуживать запросы.
//
// Без авторизации (как /healthz) — для healthcheck-проб
// docker/k8s/reverse-proxy. Не отдаёт чувствительных данных.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	resp := readyResponse{
		Status: "ready",
		Checks: make(map[string]readyCheck),
	}

	// БД — критично.
	if err := s.store.DB().PingContext(ctx); err != nil {
		resp.Status = "not_ready"
		resp.Checks["database"] = readyCheck{OK: false, Error: err.Error()}
		writeJSON(w, http.StatusServiceUnavailable, resp)
		return
	}
	resp.Checks["database"] = readyCheck{OK: true}

	// Модем — информационно.
	//
	// SERIAL_PORT=none/пусто — самодостаточный признак
	// «модем не сконфигурирован», warning не зависит от
	// наличия svc (в тестах svc может быть nil).
	//
	// Если модем сконфигурирован — проверяем регистрацию через
	// svc; без svc состояние неизвестно, без warning.
	switch {
	case s.cfg.SerialPort == "" || s.cfg.SerialPort == "none":
		resp.Warnings = append(resp.Warnings, "modem not configured")
	case s.svc != nil && !s.svc.Registered():
		resp.Warnings = append(resp.Warnings,
			"modem configured but not registered in network")
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleStatus — GET /api/v1/status.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	counts, err := s.store.CountByStatus(ctx)
	if err != nil {
		s.logger.Error("status: countByStatus", "err", err)
		writeError(w, http.StatusInternalServerError,
			CodeInternalError, "не удалось получить статистику БД")
		return
	}

	msgCounts := make(map[string]int, len(counts))
	for st, n := range counts {
		msgCounts[string(st)] = n
	}

	resp := StatusResponse{
		Status:    "ok",
		Version:   s.version,
		BuildTime: s.buildTime,
		Messages:  msgCounts,
		Database: &DBStatus{
			Path:    s.cfg.DBPath,
			Healthy: true,
		},
	}

	if s.svc != nil {
		resp.Uptime = s.svc.Uptime().Round(time.Second).String()
		info := s.svc.ModemInfo()
		configured := s.cfg.SerialPort != "" && s.cfg.SerialPort != "none"
		netStat := s.svc.NetworkStatus()
		resp.Modem = &ModemStatus{
			Configured:   configured,
			Port:         s.cfg.SerialPort,
			Profile:      s.cfg.ModemProfile,
			Manufacturer: info.Manufacturer,
			Model:        info.Model,
			Revision:     info.Revision,
			IMEI:         info.IMEI,
			Signal:       s.svc.LastCSQ(),
			Network:      netStat,
			NetworkText:  networkStatusText(netStat),
		}
	} else {
		resp.Uptime = time.Since(s.startTime).Round(time.Second).String()
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleSendMessage — POST /api/v1/messages.
func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req SendRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	if err := validateSendRequest(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}

	msg := &storage.Message{
		Direction: storage.DirectionOut,
		Status:    storage.StatusQueued,
		Source:    storage.SourceAPI,
		Peer:      strings.TrimSpace(req.To),
		Text:      req.Text,
	}
	id, err := s.store.CreateMessage(ctx, msg)
	if err != nil {
		s.logger.Error("send: createMessage", "err", err)
		writeError(w, http.StatusInternalServerError,
			CodeInternalError, "не удалось создать сообщение")
		return
	}

	s.logger.Info("сообщение принято в очередь",
		"id", id, "peer", msg.Peer, "request_id", req.RequestID)

	writeJSON(w, http.StatusAccepted, SendResponse{
		ID:     id,
		Status: string(storage.StatusQueued),
	})
}

// handleListMessages — GET /api/v1/messages.
func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	f := storage.Filter{
		Direction: storage.Direction(q.Get("direction")),
		Status:    storage.Status(q.Get("status")),
		Peer:      q.Get("peer"),
	}

	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest,
				fmt.Sprintf("since: %v", err))
			return
		}
		f.Since = &t
	}
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest,
				fmt.Sprintf("until: %v", err))
			return
		}
		f.Until = &t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, CodeBadRequest,
				"limit: ожидается неотрицательное число")
			return
		}
		f.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, CodeBadRequest,
				"offset: ожидается неотрицательное число")
			return
		}
		f.Offset = n
	}

	items, total, err := s.store.ListMessages(ctx, f)
	if err != nil {
		s.logger.Error("list: ListMessages", "err", err)
		writeError(w, http.StatusInternalServerError,
			CodeInternalError, "не удалось получить список")
		return
	}

	dtos := make([]MessageDTO, 0, len(items))
	for _, m := range items {
		dtos = append(dtos, toMessageDTO(m))
	}
	writeJSON(w, http.StatusOK, ListResponse{
		Items:  dtos,
		Total:  total,
		Limit:  f.Limit,
		Offset: f.Offset,
	})
}

// handleGetMessage — GET /api/v1/messages/{id}.
func (s *Server) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseIDPath(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}

	msg, err := s.store.GetMessage(ctx, id)
	if errors.Is(err, storage.ErrMessageNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound,
			fmt.Sprintf("сообщение %d не найдено", id))
		return
	}
	if err != nil {
		s.logger.Error("get: GetMessage", "err", err)
		writeError(w, http.StatusInternalServerError,
			CodeInternalError, "не удалось получить сообщение")
		return
	}
	writeJSON(w, http.StatusOK, toMessageDTO(msg))
}

// handleDeleteMessage — DELETE /api/v1/messages/{id}.
func (s *Server) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseIDPath(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}

	err = s.store.DeleteMessage(ctx, id)
	if errors.Is(err, storage.ErrMessageNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound,
			fmt.Sprintf("сообщение %d не найдено", id))
		return
	}
	if err != nil {
		s.logger.Error("delete: DeleteMessage", "err", err)
		writeError(w, http.StatusInternalServerError,
			CodeInternalError, "не удалось удалить сообщение")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestSMS — POST /api/v1/test-sms.
func (s *Server) handleTestSMS(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		writeError(w, http.StatusServiceUnavailable,
			CodeUnavailable, "service не сконфигурирован")
		return
	}

	var req TestSMSRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
			return
		}
	}

	res, err := s.svc.SendTestSMS(r.Context(), req.Text)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			CodeInternalError, err.Error())
		return
	}

	resp := TestSMSResponse{
		ID:       res.ID,
		Status:   string(res.Status),
		Error:    res.Error,
		TimedOut: res.TimedOut,
	}
	if res.Reference != 0 {
		ref := res.Reference
		resp.Reference = &ref
	}
	if !res.SentAt.IsZero() {
		t := res.SentAt
		resp.SentAt = &t
	}
	if !res.DeliveredAt.IsZero() {
		t := res.DeliveredAt
		resp.DeliveredAt = &t
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDiagnostics — GET /api/v1/diagnostics.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		writeError(w, http.StatusServiceUnavailable,
			CodeUnavailable, "service не сконфигурирован")
		return
	}

	res, err := s.svc.RunDiagnostics(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			CodeInternalError, err.Error())
		return
	}

	// Обогащаем результат кешем из service, если live-запрос
	// упал. Пользователь видит и ошибку («no response»), и
	// кеш (последнее известное состояние из networkLoop).
	if s.svc != nil {
		if res.Registration.Error != "" {
			if st := s.svc.NetworkStatus(); st != 0 {
				res.Registration.CachedStatus = st
				res.Registration.CachedText = networkStatusText(st)
			}
		}
		if res.Signal.Error != "" {
			if csq := s.svc.LastCSQ(); csq > 0 && csq <= 31 {
				res.Signal.CachedRSSI = csq
				res.Signal.CachedDBM = -113 + 2*csq
			}
		}
	}

	// По умолчанию raw скрываем — там могут быть IMEI/IMSI/номера.
	if r.URL.Query().Get("raw") != "1" {
		res.Raw = nil
	}
	writeJSON(w, http.StatusOK, res)
}

// ==================== хелперы ====================

func decodeJSON(r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if !strings.HasPrefix(ct, "application/json") {
			return fmt.Errorf("Content-Type должен быть application/json")
		}
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodySize))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("разбор JSON: %w", err)
	}
	return nil
}

func validateSendRequest(req *SendRequest) error {
	req.To = strings.TrimSpace(req.To)
	if req.To == "" {
		return fmt.Errorf("поле 'to' обязательно")
	}
	if !strings.HasPrefix(req.To, "+") {
		return fmt.Errorf("поле 'to' должно быть в формате +<код><номер>")
	}
	if len(req.To) < 5 || len(req.To) > 20 {
		return fmt.Errorf("поле 'to': недопустимая длина")
	}
	if strings.TrimSpace(req.Text) == "" {
		return fmt.Errorf("поле 'text' обязательно")
	}
	if len(req.Text) > 4000 {
		return fmt.Errorf("поле 'text' слишком длинное (макс 4000 символов)")
	}
	return nil
}

func parseIDPath(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("некорректный id: %q", s)
	}
	return id, nil
}

// handleMessageDeliveryReports — GET /api/v1/messages/{id}/delivery-reports.
//
// Возвращает все delivery reports для сообщения.
// Пустой список — если DR ещё не пришли (например, только что
// отправлено) или модем не поддерживает DR (TP-SRR=0).
func (s *Server) handleMessageDeliveryReports(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseIDPath(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}

	// Проверим, что сообщение существует, чтобы отличать
	// «нет DR» от «нет сообщения».
	if _, err := s.store.GetMessage(ctx, id); errors.Is(err, storage.ErrMessageNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound,
			fmt.Sprintf("сообщение %d не найдено", id))
		return
	} else if err != nil {
		s.logger.Error("dr: GetMessage", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError,
			CodeInternalError, "не удалось получить сообщение")
		return
	}

	drs, err := s.store.ListDeliveryReportsByMessage(ctx, id)
	if err != nil {
		s.logger.Error("dr: ListDeliveryReportsByMessage", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError,
			CodeInternalError, "не удалось получить delivery reports")
		return
	}

	dtos := make([]DeliveryReportDTO, 0, len(drs))
	for _, dr := range drs {
		dtos = append(dtos, toDeliveryReportDTO(dr))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": dtos,
		"total": len(dtos),
	})
}
