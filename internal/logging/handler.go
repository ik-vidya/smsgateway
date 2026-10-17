// Package logging, файл handler.go — slog.Handler, который
// дублирует записи в Buffer (для SSE-страницы логов).
package logging

import (
	"context"
	"log/slog"
)

// bufferHandler оборачивает inner slog.Handler и дополнительно
// кладёт каждую запись в Buffer.
//
// Enabled наследуется от inner: если inner отфильтровал уровень,
// в буфер запись тоже не попадёт — иначе Web UI показывал бы
// больше, чем видит обычный stdout-лог.
type bufferHandler struct {
	inner  slog.Handler
	buffer *Buffer
	attrs  []slog.Attr // накопленные WithAttrs
	groups []string    // накопленные WithGroup
}

func newBufferHandler(inner slog.Handler, buf *Buffer) slog.Handler {
	return &bufferHandler{inner: inner, buffer: buf}
}

func (h *bufferHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *bufferHandler) Handle(ctx context.Context, r slog.Record) error {
	if err := h.inner.Handle(ctx, r); err != nil {
		return err
	}

	e := Entry{
		Time:  r.Time,
		Level: r.Level,
		Msg:   r.Message,
	}
	// Атрибуты, добавленные через WithAttrs.
	if len(h.attrs) > 0 {
		e.Attrs = make(map[string]string, len(h.attrs)+r.NumAttrs())
		for _, a := range h.attrs {
			e.Attrs[h.prefixKey(a.Key)] = a.Value.String()
		}
	}
	// Атрибуты записи.
	r.Attrs(func(a slog.Attr) bool {
		if e.Attrs == nil {
			e.Attrs = make(map[string]string)
		}
		e.Attrs[h.prefixKey(a.Key)] = a.Value.String()
		return true
	})

	h.buffer.Add(e)
	return nil
}

func (h *bufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	newAttrs := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	newAttrs = append(newAttrs, h.attrs...)
	newAttrs = append(newAttrs, attrs...)

	return &bufferHandler{
		inner:  h.inner.WithAttrs(attrs),
		buffer: h.buffer,
		attrs:  newAttrs,
		groups: h.groups,
	}
}

func (h *bufferHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	newGroups := make([]string, 0, len(h.groups)+1)
	newGroups = append(newGroups, h.groups...)
	newGroups = append(newGroups, name)

	return &bufferHandler{
		inner:  h.inner.WithGroup(name),
		buffer: h.buffer,
		attrs:  h.attrs,
		groups: newGroups,
	}
}

// prefixKey добавляет префикс группы к ключу (через точку).
// Это упрощает жизнь SSE-клиенту: все ключи плоские.
func (h *bufferHandler) prefixKey(key string) string {
	if len(h.groups) == 0 {
		return key
	}
	out := ""
	for _, g := range h.groups {
		out += g + "."
	}
	return out + key
}
