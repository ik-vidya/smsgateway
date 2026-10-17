package logging

import (
	"io"
	"log/slog"
	"testing"
)

func newTestHandler(buf *Buffer, level slog.Level) slog.Handler {
	inner := slog.NewTextHandler(io.Discard,
		&slog.HandlerOptions{Level: level})
	return newBufferHandler(inner, buf)
}

func TestBufferHandlerWritesToBuffer(t *testing.T) {
	buf := NewBuffer(10)
	logger := slog.New(newTestHandler(buf, slog.LevelDebug))

	logger.Info("hello", "peer", "+371")

	snap := buf.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("len=%d", len(snap))
	}
	if snap[0].Msg != "hello" {
		t.Errorf("Msg=%q", snap[0].Msg)
	}
	if snap[0].Level != slog.LevelInfo {
		t.Errorf("Level=%v", snap[0].Level)
	}
	if snap[0].Attrs["peer"] != "+371" {
		t.Errorf("Attrs=%v", snap[0].Attrs)
	}
}

func TestBufferHandlerWithAttrs(t *testing.T) {
	buf := NewBuffer(10)
	logger := slog.New(newTestHandler(buf, slog.LevelDebug)).With("svc", "gammu")

	logger.Info("x")

	snap := buf.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("len=%d", len(snap))
	}
	if snap[0].Attrs["svc"] != "gammu" {
		t.Errorf("Attrs=%v", snap[0].Attrs)
	}
}

func TestBufferHandlerWithGroup(t *testing.T) {
	buf := NewBuffer(10)
	logger := slog.New(newTestHandler(buf, slog.LevelDebug)).
		WithGroup("modem").With("port", "/dev/ttyUSB0")

	logger.Info("x")

	snap := buf.Snapshot()
	if snap[0].Attrs["modem.port"] != "/dev/ttyUSB0" {
		t.Errorf("Attrs=%v", snap[0].Attrs)
	}
}

func TestBufferHandlerRespectsLevel(t *testing.T) {
	buf := NewBuffer(10)
	logger := slog.New(newTestHandler(buf, slog.LevelWarn))

	logger.Info("should-be-dropped")
	logger.Warn("visible")

	snap := buf.Snapshot()
	if len(snap) != 1 || snap[0].Msg != "visible" {
		t.Errorf("%v", snap)
	}
}
