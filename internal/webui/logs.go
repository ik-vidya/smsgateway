// Package webui, файл logs.go — страница /logs и SSE-поток.
package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.vidya.lv/vidya/smsgateway/internal/logging"
)

// handleLogs — GET /logs.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	data := s.baseData(r, "logs")
	data["HasStream"] = s.logs != nil
	if s.logs == nil {
		data["ErrorMessage"] = "Log buffer недоступен"
		s.render(w, "logs", data)
		return
	}
	data["BufferSize"] = s.logs.Size()
	data["BufferCount"] = s.logs.Count()
	oldest, newest := s.logs.OldestNewest()
	if !oldest.IsZero() {
		data["OldestTime"] = oldest
		data["NewestTime"] = newest
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

// handleLogsDownload — GET /logs/download.
//
// Скачивает ring buffer как .log (text, совместимый с slog)
// или .jsonl (json, каждая запись отдельной строкой).
func (s *Server) handleLogsDownload(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		http.Error(w, "log buffer not configured",
			http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()

	f := logging.Filter{Contains: q.Get("contains")}
	lvl, err := logging.ParseLevelFilter(q.Get("level"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.MinLevel = lvl

	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "since: "+err.Error(), http.StatusBadRequest)
			return
		}
		f.Since = t
	}
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "until: "+err.Error(), http.StatusBadRequest)
			return
		}
		f.Until = t
	}

	entries := s.logs.SnapshotFiltered(f)

	format := strings.ToLower(q.Get("format"))
	if format == "" {
		format = "text"
	}

	filename := "smsgateway-" + time.Now().Format("20060102-150405")
	switch format {
	case "text":
		filename += ".log"
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	case "json":
		filename += ".jsonl"
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	default:
		http.Error(w, `format должен быть "text" или "json"`,
			http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+filename+`"`)

	switch format {
	case "text":
		for _, e := range entries {
			_, _ = fmt.Fprintln(w, e.Text())
		}
	case "json":
		enc := json.NewEncoder(w)
		for _, e := range entries {
			_ = enc.Encode(e)
		}
	}
}
