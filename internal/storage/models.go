// Package storage, файл models.go — типы предметной области и
// допустимые значения полей.
package storage

import (
	"database/sql"
	"fmt"
	"time"
)

// Direction — направление сообщения.
type Direction string

const (
	DirectionIn  Direction = "in"
	DirectionOut Direction = "out"
)

// Status — статус сообщения.
//
// Для direction=out:
//
//	queued    — принято API, лежит в очереди
//	sending   — началась отправка через AT+CMGS
//	sent      — модем подтвердил +CMGS: <ref>, ждём DR
//	delivered — пришёл DR с TP-ST=0x00
//	failed    — ошибка модема, таймаут или DR с TP-ST≠0
//	sent_no_dr — sent + истёк DR_WAIT_TIMEOUT, DR так и не пришёл
//
// Для direction=in:
//
//	received  — SMS прочитана из модема и сохранена
type Status string

const (
	StatusQueued    Status = "queued"
	StatusSending   Status = "sending"
	StatusSent      Status = "sent"
	StatusDelivered Status = "delivered"
	StatusFailed    Status = "failed"
	StatusSentNoDR  Status = "sent_no_dr"
	StatusReceived  Status = "received"
)

// IsTerminal — статус терминальный, дальнейшие переходы запрещены.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusDelivered, StatusFailed, StatusSentNoDR, StatusReceived:
		return true
	}
	return false
}

// Message — запись SMS.
type Message struct {
	ID        int64
	Direction Direction
	Status    Status
	Peer      string
	Text      string
	RawPDU    string

	// Исходящие
	Reference  sql.NullInt64
	TPMR       sql.NullInt64
	RetryCount int
	Error      string

	// Жизненный цикл
	QueuedAt    sql.NullTime
	SentAt      sql.NullTime
	DeliveredAt sql.NullTime

	CreatedAt time.Time
	UpdatedAt time.Time
}

// DeliveryReport — запись отчёта о доставке.
type DeliveryReport struct {
	ID         int64
	MessageID  sql.NullInt64
	Reference  int64
	TPST       int
	StatusText string
	Peer       string
	RawPDU     string
	ReceivedAt time.Time
}

// PendingReference — забронированный TP-MR.
type PendingReference struct {
	Reference int64
	MessageID int64
	CreatedAt time.Time
	ExpiresAt time.Time
}

// PendingPart — незавершённая multipart-сборка.
type PendingPart struct {
	ID        int64
	Reference int64
	Peer      string
	Total     int
	Received  int
	CreatedAt time.Time
	// Bodies — тела частей (seq → text). Заполняется при чтении
	// конкретной сборки.
	Bodies map[int]string
}

// Filter — параметры выборки сообщений.
type Filter struct {
	Direction Direction  // пусто — все
	Status    Status     // пусто — все
	Peer      string     // подстрока; пусто — все
	Since     *time.Time // created_at >= since
	Until     *time.Time // created_at < until
	Limit     int        // 0 — значение по умолчанию (50)
	Offset    int
}

// UpdateStatusOptions — дополнительные поля при смене статуса.
type UpdateStatusOptions struct {
	Error       string
	Reference   *int64
	TPMR        *int64
	RetryCount  *int
	QueuedAt    *time.Time
	SentAt      *time.Time
	DeliveredAt *time.Time
}

// String возвращает человекочитаемое представление.
func (m Message) String() string {
	return fmt.Sprintf("Message{id=%d, direction=%s, status=%s, peer=%s}",
		m.ID, m.Direction, m.Status, m.Peer)
}
