// Package api, файл dto.go — DTO для JSON-сериализации.
package api

import (
	"time"

	"github.com/ik-vidya/smsgateway/internal/storage"
)

// MessageDTO — представление сообщения в API.
type MessageDTO struct {
	ID        int64  `json:"id"`
	Direction string `json:"direction"`
	Status    string `json:"status"`
	Peer      string `json:"peer"`
	Text      string `json:"text,omitempty"`
	RawPDU    string `json:"raw_pdu,omitempty"`

	Reference  *int64 `json:"reference,omitempty"`
	TPMR       *int64 `json:"tp_mr,omitempty"`
	RetryCount int    `json:"retry_count,omitempty"`
	Error      string `json:"error,omitempty"`

	QueuedAt    *time.Time `json:"queued_at,omitempty"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toMessageDTO(m *storage.Message) MessageDTO {
	dto := MessageDTO{
		ID:         m.ID,
		Direction:  string(m.Direction),
		Status:     string(m.Status),
		Peer:       m.Peer,
		Text:       m.Text,
		RawPDU:     m.RawPDU,
		RetryCount: m.RetryCount,
		Error:      m.Error,
		CreatedAt:  m.CreatedAt,
		UpdatedAt:  m.UpdatedAt,
	}
	if m.Reference.Valid {
		v := m.Reference.Int64
		dto.Reference = &v
	}
	if m.TPMR.Valid {
		v := m.TPMR.Int64
		dto.TPMR = &v
	}
	if m.QueuedAt.Valid {
		v := m.QueuedAt.Time
		dto.QueuedAt = &v
	}
	if m.SentAt.Valid {
		v := m.SentAt.Time
		dto.SentAt = &v
	}
	if m.DeliveredAt.Valid {
		v := m.DeliveredAt.Time
		dto.DeliveredAt = &v
	}
	return dto
}

// SendRequest — тело POST /api/v1/messages.
type SendRequest struct {
	To        string `json:"to"`
	Text      string `json:"text"`
	RequestID string `json:"request_id,omitempty"`
}

// SendResponse — ответ POST /api/v1/messages.
type SendResponse struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// ListResponse — ответ GET /api/v1/messages.
type ListResponse struct {
	Items  []MessageDTO `json:"items"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// StatusResponse — ответ GET /api/v1/status.
type StatusResponse struct {
	Status    string         `json:"status"`
	Uptime    string         `json:"uptime,omitempty"`
	Version   string         `json:"version,omitempty"`
	BuildTime string         `json:"build_time,omitempty"`
	Messages  map[string]int `json:"messages,omitempty"`
	Database  *DBStatus      `json:"database,omitempty"`
	Modem     *ModemStatus   `json:"modem,omitempty"`
}

// DBStatus — состояние базы данных.
type DBStatus struct {
	Path    string `json:"path,omitempty"`
	Healthy bool   `json:"healthy"`
}

// ModemStatus — состояние модема.
type ModemStatus struct {
	Configured   bool   `json:"configured"`
	Port         string `json:"port,omitempty"`
	Profile      string `json:"profile,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	Revision     string `json:"revision,omitempty"`
	IMEI         string `json:"imei,omitempty"`
	Signal       int    `json:"signal"`
	Network      int    `json:"network"`
	NetworkText  string `json:"network_text,omitempty"`
}

func networkStatusText(stat int) string {
	switch stat {
	case 0:
		return "не зарегистрирован, поиск не ведётся"
	case 1:
		return "зарегистрирован (домашняя сеть)"
	case 2:
		return "идёт поиск сети"
	case 3:
		return "регистрация отклонена"
	case 4:
		return "неизвестно"
	case 5:
		return "зарегистрирован (роуминг)"
	}
	return ""
}

// TestSMSRequest — тело POST /api/v1/test-sms.
type TestSMSRequest struct {
	Text string `json:"text,omitempty"`
}

// TestSMSResponse — ответ POST /api/v1/test-sms.
type TestSMSResponse struct {
	ID          int64      `json:"id"`
	Status      string     `json:"status"`
	Reference   *int64     `json:"reference,omitempty"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	Error       string     `json:"error,omitempty"`
	TimedOut    bool       `json:"timed_out,omitempty"`
}
