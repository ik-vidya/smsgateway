// Package storage, файл messages.go — репозиторий сообщений.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrMessageNotFound — сообщение не найдено.
var ErrMessageNotFound = errors.New("сообщение не найдено")

const (
	defaultLimit = 50
	maxLimit     = 500
)

// CreateMessage вставляет новую запись и возвращает её ID.
func (s *Store) CreateMessage(ctx context.Context, m *Message) (int64, error) {
	if m.Direction == "" {
		return 0, errors.New("direction не задан")
	}
	if m.Status == "" {
		return 0, errors.New("status не задан")
	}
	if m.Peer == "" {
		return 0, errors.New("peer не задан")
	}

	now := nowUTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO messages (
			direction, status, source, peer, text, raw_pdu,
			reference, tp_mr, retry_count, error,
			queued_at, sent_at, delivered_at,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		string(m.Direction), string(m.Status),
		nullString(string(m.Source)), m.Peer,
		nullString(m.Text), nullString(m.RawPDU),
		m.Reference, m.TPMR, m.RetryCount,
		nullString(m.Error),
		m.QueuedAt, m.SentAt, m.DeliveredAt,
		m.CreatedAt, now,
	)
	if err != nil {
		return 0, fmt.Errorf("INSERT messages: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	m.ID = id
	return id, nil
}

// GetMessage возвращает сообщение по ID.
func (s *Store) GetMessage(ctx context.Context, id int64) (*Message, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+messageColumns+`
		  FROM messages WHERE id = ?
	`, id)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMessageNotFound
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// ListMessages возвращает список сообщений по фильтру и общее
// количество подходящих записей (без учёта limit/offset).
func (s *Store) ListMessages(ctx context.Context, f Filter) ([]*Message, int, error) {
	where, args := buildWhere(f)

	// total
	var total int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM messages "+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("COUNT messages: %w", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	query := `SELECT ` + messageColumns + `
	            FROM messages ` + where + `
	           ORDER BY created_at DESC, id DESC
	           LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("SELECT messages: %w", err)
	}
	defer rows.Close()

	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// buildWhere собирает WHERE-условие и аргументы из фильтра.
func buildWhere(f Filter) (string, []any) {
	var conds []string
	var args []any

	if f.Direction != "" {
		conds = append(conds, "direction = ?")
		args = append(args, string(f.Direction))
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, string(f.Status))
	}
	if f.Source != "" {
		conds = append(conds, "source = ?")
		args = append(args, string(f.Source))
	}
	if f.Peer != "" {
		conds = append(conds, "peer LIKE ?")
		args = append(args, "%"+f.Peer+"%")
	}
	if f.Since != nil {
		conds = append(conds, "created_at >= ?")
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		conds = append(conds, "created_at < ?")
		args = append(args, *f.Until)
	}

	if len(conds) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// UpdateStatus переводит сообщение в новый статус, проверяя
// допустимость перехода. Возвращает ErrMessageNotFound, если
// сообщение не найдено, и ошибку при недопустимом переходе.
func (s *Store) UpdateStatus(ctx context.Context, id int64, newStatus Status, opts UpdateStatusOptions) error {
	current, err := s.GetMessage(ctx, id)
	if err != nil {
		return err
	}
	if !isTransitionAllowed(current.Status, newStatus) {
		return fmt.Errorf("недопустимый переход статуса: %s → %s",
			current.Status, newStatus)
	}

	now := nowUTC()
	sets := []string{"status = ?", "updated_at = ?"}
	args := []any{string(newStatus), now}

	if opts.Error != "" {
		sets = append(sets, "error = ?")
		args = append(args, opts.Error)
	}
	if opts.Reference != nil {
		sets = append(sets, "reference = ?")
		args = append(args, *opts.Reference)
	}
	if opts.TPMR != nil {
		sets = append(sets, "tp_mr = ?")
		args = append(args, *opts.TPMR)
	}
	if opts.RetryCount != nil {
		sets = append(sets, "retry_count = ?")
		args = append(args, *opts.RetryCount)
	}
	if opts.QueuedAt != nil {
		sets = append(sets, "queued_at = ?")
		args = append(args, *opts.QueuedAt)
	}
	if opts.SentAt != nil {
		sets = append(sets, "sent_at = ?")
		args = append(args, *opts.SentAt)
	}
	if opts.DeliveredAt != nil {
		sets = append(sets, "delivered_at = ?")
		args = append(args, *opts.DeliveredAt)
	}
	args = append(args, id)

	query := "UPDATE messages SET " + strings.Join(sets, ", ") + " WHERE id = ?"
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("UPDATE messages: %w", err)
	}
	return nil
}

// isTransitionAllowed проверяет допустимость перехода.
//
// Правила:
//   - из терминального статуса переходов нет
//     (кроме sent_no_dr → delivered: опоздавший DR)
//   - из queued: только в sending или failed
//   - из sending: в sent или failed
//   - из sent: в delivered, failed, sent_no_dr
func isTransitionAllowed(from, to Status) bool {
	if from == to {
		return true
	}
	switch from {
	case StatusQueued:
		return to == StatusSending || to == StatusFailed
	case StatusSending:
		return to == StatusSent || to == StatusFailed
	case StatusSent:
		return to == StatusDelivered || to == StatusFailed || to == StatusSentNoDR
	case StatusSentNoDR:
		// Опоздавший DR может доехать.
		return to == StatusDelivered
	}
	return false
}

// MarkSending: queued → sending.
func (s *Store) MarkSending(ctx context.Context, id int64) error {
	now := nowUTC()
	return s.UpdateStatus(ctx, id, StatusSending, UpdateStatusOptions{
		QueuedAt: &now,
	})
}

// MarkSent: sending → sent.
func (s *Store) MarkSent(ctx context.Context, id, reference int64) error {
	now := nowUTC()
	return s.UpdateStatus(ctx, id, StatusSent, UpdateStatusOptions{
		Reference: &reference,
		SentAt:    &now,
	})
}

// MarkDelivered: sent → delivered.
func (s *Store) MarkDelivered(ctx context.Context, id int64, deliveredAt time.Time) error {
	return s.UpdateStatus(ctx, id, StatusDelivered, UpdateStatusOptions{
		DeliveredAt: &deliveredAt,
	})
}

// MarkFailed: любой нетерминальный → failed.
func (s *Store) MarkFailed(ctx context.Context, id int64, errMsg string) error {
	return s.UpdateStatus(ctx, id, StatusFailed, UpdateStatusOptions{
		Error: errMsg,
	})
}

// MarkSentNoDR: sent → sent_no_dr.
func (s *Store) MarkSentNoDR(ctx context.Context, id int64) error {
	return s.UpdateStatus(ctx, id, StatusSentNoDR, UpdateStatusOptions{})
}

// DeleteMessage удаляет сообщение по ID.
func (s *Store) DeleteMessage(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("DELETE messages: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrMessageNotFound
	}
	return nil
}

// CountByStatus возвращает карту status → количество.
func (s *Store) CountByStatus(ctx context.Context) (map[Status]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM messages GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[Status]int)
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[Status(st)] = n
	}
	return out, rows.Err()
}

// ==================== сканирование ====================

// rowScanner — общий интерфейс для *sql.Row и *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// messageColumns — единый список колонок для SELECT из messages.
// Порядок ДОЛЖЕН совпадать с scanMessage.
const messageColumns = `id, direction, status, source, peer, text, raw_pdu,
	reference, tp_mr, retry_count, error,
	queued_at, sent_at, delivered_at,
	created_at, updated_at`

func scanMessage(sc rowScanner) (*Message, error) {
	var m Message
	var direction, status string
	var source, text, rawPDU, errMsg sql.NullString

	if err := sc.Scan(
		&m.ID, &direction, &status, &source, &m.Peer, &text, &rawPDU,
		&m.Reference, &m.TPMR, &m.RetryCount, &errMsg,
		&m.QueuedAt, &m.SentAt, &m.DeliveredAt,
		&m.CreatedAt, &m.UpdatedAt,
	); err != nil {
		return nil, err
	}
	m.Direction = Direction(direction)
	m.Status = Status(status)
	m.Source = Source(source.String)
	m.Text = text.String
	m.RawPDU = rawPDU.String
	m.Error = errMsg.String
	return &m, nil
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// Проверим, что Message реализует Stringer.
var _ fmt.Stringer = Message{}
