// Package notify — нотификаторы для критических событий.
//
// Основная реализация — Telegram (см. telegram.go). Интерфейс
// Notifier позволяет подставить mock в тестах и Noop, когда
// Telegram не сконфигурирован.
package notify

import "context"

// Level — важность уведомления.
type Level string

const (
	LevelCritical Level = "critical"
	LevelWarning  Level = "warning"
)

// Notifier — приёмник уведомлений.
type Notifier interface {
	// Notify отправляет уведомление. Не блокирует вызывающего —
	// реализация должна класть сообщение в очередь.
	Notify(ctx context.Context, level Level, err error)
	// Close дожидается отправки оставшихся сообщений и останавливает
	// фоновые горутины.
	Close() error
}

// Noop — заглушка, ничего не делает.
type Noop struct{}

func (Noop) Notify(context.Context, Level, error) {}
func (Noop) Close() error                         { return nil }
