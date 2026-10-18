// Package webui, файл message.go — страница /messages/{id}
// с полной информацией о сообщении и delivery reports.
package webui

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ik-vidya/smsgateway/internal/storage"
)

// handleMessageDetail — GET /messages/{id}.
func (s *Server) handleMessageDetail(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDPath(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	msg, err := s.store.GetMessage(ctx, id)
	if errors.Is(err, storage.ErrMessageNotFound) {
		http.Error(w, fmt.Sprintf("сообщение %d не найдено", id),
			http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("webui: GetMessage", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	drs, err := s.store.ListDeliveryReportsByMessage(ctx, id)
	if err != nil {
		s.logger.Warn("webui: ListDeliveryReportsByMessage",
			"err", err, "id", id)
		// Не падаем: показываем сообщение без DR.
	}

	data := s.baseData(r, "message")
	data["Message"] = msg
	data["Reports"] = drs
	data["IsIn"] = msg.Direction == storage.DirectionIn
	s.render(w, "message", data)
}

// parseIDPath разбирает {id} из пути (используется в
// сообщениях detail; в API — свой аналог).
func parseIDPath(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("некорректный id: %q", s)
	}
	return id, nil
}
