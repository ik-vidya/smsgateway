// Package webui, файл logs.go — страница /logs и SSE-поток.
package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ik-vidya/smsgateway/internal/logging"
)

// handleLogs — GET /logs.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	data := s.baseData(r, "logs")
	data["HasStream"] = s.logs != nil
	if s.logs == nil {
		data["ErrorMessage"] = "Log buffer недоступен"
	}
	s.render(w, "logs", data)
}

// handleLogsStream — GET /logs/stream (SSE).
//
// Каждое событие — строка вида "data: <json>" и пустая строка
// после неё (два перевода строки, как требует протокол SSE).
//
// Если клиент медленный и его канал переполнился, шлём событие
// "warning" с числом пропущенных записей.
//
// Каждые 15 секунд — keepalive-комментарий ": keepalive" плюс
// пустая строка, чтобы прокси не разорвали соединение.
func (s *Server) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		http.Error(w, "log buffer not configured",
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

	// Снимок + подписка атомарно. Между снимком и подпиской нет
	// «дыры» — всё, что попало в буфер после снимка, придёт в ch.
	id, snap, ch := s.logs.Subscribe()
	defer func() { _ = s.logs.Unsubscribe(id) }()

	for _, e := range snap {
		if err := writeSSEEntry(w, e); err != nil {
			return
		}
	}
	flusher.Flush()

	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if n := s.logs.Dropped(id); n > 0 {
				_ = writeSSEWarning(w, n)
			}
			if err := writeSSEEntry(w, e); err != nil {
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

// writeSSEEntry пишет одно событие с JSON-телом.
func writeSSEEntry(w http.ResponseWriter, e logging.Entry) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", raw)
	return err
}

// writeSSEWarning пишет событие "warning" — клиент потерял часть
// записей из-за переполнения канала.
func writeSSEWarning(w http.ResponseWriter, dropped int64) error {
	_, err := fmt.Fprintf(w,
		"event: warning\ndata: %d log entries dropped\n\n", dropped)
	return err
}
