// Package logging — настройка log/slog с уровнями и форматом.
//
// В базовом варианте (New) логгер пишет только в stdout.
// В расширенном (NewWithBuffer) — ещё и в in-memory ring buffer
// для Web UI.
package logging

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// ErrNoSubscription — попытка Unsubscribe с неизвестным id.
var ErrNoSubscription = errors.New("logging: unknown subscription")

// New создаёт slog.Logger с заданными уровнем и форматом.
//
// level: debug | info | warn | error
// format: text | json
func New(level, format string) (*slog.Logger, error) {
	h, err := newHandler(level, format)
	if err != nil {
		return nil, err
	}
	return slog.New(h), nil
}

// NewWithBuffer создаёт slog.Logger, который пишет одновременно в
// stdout (как New) и в in-memory Buffer для Web UI.
//
// Если buf == nil, ведёт себя как New.
func NewWithBuffer(level, format string, buf *Buffer) (*slog.Logger, error) {
	h, err := newHandler(level, format)
	if err != nil {
		return nil, err
	}
	if buf == nil {
		return slog.New(h), nil
	}
	return slog.New(newBufferHandler(h, buf)), nil
}

// newHandler собирает внутренний Handler по уровню и формату.
func newHandler(level, format string) (slog.Handler, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}

	switch strings.ToLower(format) {
	case "json":
		return slog.NewJSONHandler(os.Stdout, opts), nil
	case "", "text":
		return slog.NewTextHandler(os.Stdout, opts), nil
	}
	return nil, fmt.Errorf("неизвестный формат лога %q", format)
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("неизвестный уровень лога %q", s)
}
