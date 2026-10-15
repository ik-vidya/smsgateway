// Package storage, файл delivery.go — репозиторий delivery reports.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CreateDeliveryReport сохраняет отчёт о доставке и, если найден
// связанный message, обновляет его статус на delivered/failed.
func (s *Store) CreateDeliveryReport(ctx context.Context, dr *DeliveryReport) (int64, error) {
	if dr.Reference == 0 {
		return 0, fmt.Errorf("reference обязателен")
	}
	if dr.RawPDU == "" {
		return 0, fmt.Errorf("raw_pdu обязателен")
	}
	if dr.ReceivedAt.IsZero() {
		dr.ReceivedAt = nowUTC()
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO delivery_reports (
			message_id, reference, tp_st, status_text,
			peer, raw_pdu, received_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		dr.MessageID, dr.Reference, dr.TPST, dr.StatusText,
		nullString(dr.Peer), dr.RawPDU, dr.ReceivedAt,
	)
	if err != nil {
		return 0, fmt.Errorf("INSERT delivery_reports: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	dr.ID = id

	// Если message_id определён — обновляем статус сообщения.
	if dr.MessageID.Valid {
		if err := s.applyDeliveryToMessage(ctx, dr.MessageID.Int64, dr.TPST, dr.ReceivedAt); err != nil {
			// Не считаем это фатальной ошибкой — DR уже сохранён.
			return id, fmt.Errorf("обновление статуса message %d: %w",
				dr.MessageID.Int64, err)
		}
	}
	return id, nil
}

// applyDeliveryToMessage переводит сообщение в delivered или failed
// в зависимости от TP-ST.
func (s *Store) applyDeliveryToMessage(ctx context.Context, id int64, tpst int, when time.Time) error {
	if tpst == 0x00 {
		return s.MarkDelivered(ctx, id, when)
	}
	return s.MarkFailed(ctx, id, fmt.Sprintf("delivery failed (TP-ST=0x%02X)", tpst))
}

// FindMessageByReference ищет исходящее сообщение с указанным reference.
// Возвращает ErrMessageNotFound, если не найдено.
func (s *Store) FindMessageByReference(ctx context.Context, reference int64) (*Message, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, direction, status, peer, text, raw_pdu,
		       reference, tp_mr, retry_count, error,
		       queued_at, sent_at, delivered_at,
		       created_at, updated_at
		  FROM messages
		 WHERE reference = ? AND direction = 'out'
		 ORDER BY created_at DESC LIMIT 1
	`, reference)
	m, err := scanMessage(row)
	if err == sql.ErrNoRows {
		return nil, ErrMessageNotFound
	}
	return m, err
}

// ListDeliveryReportsByMessage возвращает все DR для сообщения.
func (s *Store) ListDeliveryReportsByMessage(ctx context.Context, messageID int64) ([]*DeliveryReport, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, message_id, reference, tp_st, status_text,
		       peer, raw_pdu, received_at
		  FROM delivery_reports
		 WHERE message_id = ?
		 ORDER BY received_at ASC
	`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeliveryReports(rows)
}

// ListDeliveryReportsByReference возвращает все DR с указанным reference.
func (s *Store) ListDeliveryReportsByReference(ctx context.Context, reference int64) ([]*DeliveryReport, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, message_id, reference, tp_st, status_text,
		       peer, raw_pdu, received_at
		  FROM delivery_reports
		 WHERE reference = ?
		 ORDER BY received_at ASC
	`, reference)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeliveryReports(rows)
}

func scanDeliveryReports(rows *sql.Rows) ([]*DeliveryReport, error) {
	var out []*DeliveryReport
	for rows.Next() {
		var dr DeliveryReport
		var peer sql.NullString
		if err := rows.Scan(
			&dr.ID, &dr.MessageID, &dr.Reference, &dr.TPST,
			&dr.StatusText, &peer, &dr.RawPDU, &dr.ReceivedAt,
		); err != nil {
			return nil, err
		}
		dr.Peer = peer.String
		out = append(out, &dr)
	}
	return out, rows.Err()
}
