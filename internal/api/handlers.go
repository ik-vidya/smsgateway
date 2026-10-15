// Package api, файл handlers.go — HTTP-обработчики.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ik-vidya/smsgateway/internal/storage"
)

const maxBodySize = 64 * 1024 // 64 КиБ

// handleHealthz — GET /healthz (без авторизации).
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
		Status:   "ok",
		Messages: msgCounts,
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
		"id", id,
		"peer", msg.Peer,
		"request_id", req.RequestID,
	)

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
