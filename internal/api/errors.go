// Package api, файл errors.go — единый формат ошибок и хелперы
// для JSON-ответов.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// APIError — тело ошибки.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorResponse — общий конверт для ошибок.
type ErrorResponse struct {
	Error APIError `json:"error"`
}

// Коды ошибок.
const (
	CodeBadRequest    = "bad_request"
	CodeUnauthorized  = "unauthorized"
	CodeNotFound      = "not_found"
	CodeConflict      = "conflict"
	CodeInternalError = "internal_error"
	CodeUnavailable   = "unavailable"
)

// writeJSON сериализует v в JSON и отправляет с указанным кодом.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Уже начали писать ответ — лог не поможет, но пусть будет.
		slog.Default().Error("writeJSON: encode", "err", err)
	}
}

// writeError отправляет JSON-ошибку.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{
		Error: APIError{Code: code, Message: message},
	})
}
