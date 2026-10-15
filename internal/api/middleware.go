// Package api, файл middleware.go — middleware: авторизация,
// логирование запросов, recover от паник.
package api

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// withAuth проверяет API-ключ. Пропускает запрос, если ключ валиден.
//
// Принимает:
//
//	X-API-Key: <key>
//	Authorization: Bearer <key>
//
// Сравнение — constant-time.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := extractAPIKey(r)
		if key == "" {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized,
				"требуется API-ключ (X-API-Key или Authorization: Bearer)")
			return
		}
		if !s.validKey(key) {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized,
				"неверный API-ключ")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractAPIKey достаёт ключ из заголовков.
func extractAPIKey(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get("X-API-Key")); k != "" {
		return k
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		return strings.TrimSpace(auth[len(prefix):])
	}
	return ""
}

// validKey проверяет ключ constant-time против всех настроенных.
func (s *Server) validKey(key string) bool {
	for _, want := range s.cfg.APIKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

// statusRecorder запоминает статус ответа для логирования.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

// withLogging логирует каждый запрос: method, path, status, duration.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		// Пропускаем шумные запросы к healthz, если уровень > debug.
		path := r.URL.Path
		level := slog.LevelInfo
		if path == "/healthz" && !s.logger.Enabled(r.Context(), slog.LevelDebug) {
			return
		}
		s.logger.Log(r.Context(), level, "http",
			"method", r.Method,
			"path", path,
			"query", r.URL.RawQuery,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration", time.Since(start).Round(time.Millisecond),
			"remote", r.RemoteAddr,
		)
	})
}

// withRecover перехватывает панику в handler и возвращает 500.
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("паника в handler",
					"err", rec,
					"method", r.Method,
					"path", r.URL.Path,
				)
				writeError(w, http.StatusInternalServerError,
					CodeInternalError, "внутренняя ошибка сервера")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
