// Package logging — настройка log/slog с уровнями и форматом.
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// New создаёт slog.Logger с заданными уровнем и форматом.
//
// level: debug | info | warn | error
// format: text | json
func New(level, format string) (*slog.Logger, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}

	var h slog.Handler
	switch strings.ToLower(format) {
	case "json":
		h = slog.NewJSONHandler(os.Stdout, opts)
	case "", "text":
		h = slog.NewTextHandler(os.Stdout, opts)
	default:
		return nil, fmt.Errorf("неизвестный формат лога %q", format)
	}
	return slog.New(h), nil
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
