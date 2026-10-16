// Package storage, файл gammu_outbox.go — CRUD по таблице
// gammu_outbox: состояние файлов, лежащих в Gammu-outbox.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrOutboxEntryNotFound — запись о файле не найдена.
var ErrOutboxEntryNotFound = errors.New("gammu_outbox entry not found")

// OutboxEntry — состояние одного файла в outbox.
type OutboxEntry struct {
	Path      string
	MessageID int64
	Retries   int
	LastError string
	Priority  string
	CreatedAt string // как в БД, для аудита (не парсим)
	UpdatedAt string
}

// CreateOutboxEntry вставляет запись. Возвращает ошибку, если
// path уже занят.
func (s *Store) CreateOutboxEntry(ctx context.Context, e *OutboxEntry) error {
	if e.Path == "" {
		return errors.New("path обязателен")
	}
	if e.MessageID == 0 {
		return errors.New("message_id обязателен")
	}
	if e.Priority == "" {
		e.Priority = "A"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gammu_outbox (path, message_id, retries, last_error, priority)
		VALUES (?, ?, ?, ?, ?)
	`, e.Path, e.MessageID, e.Retries, nullString(e.LastError), e.Priority)
	if err != nil {
		return fmt.Errorf("INSERT gammu_outbox: %w", err)
	}
	return nil
}

// GetOutboxEntry возвращает запись по текущему пути файла.
func (s *Store) GetOutboxEntry(ctx context.Context, path string) (*OutboxEntry, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT path, message_id, retries, last_error, priority,
		       CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		  FROM gammu_outbox WHERE path = ?
	`, path)
	e := &OutboxEntry{}
	var lastErr sql.NullString
	if err := row.Scan(
		&e.Path, &e.MessageID, &e.Retries, &lastErr, &e.Priority,
		&e.CreatedAt, &e.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOutboxEntryNotFound
		}
		return nil, err
	}
	e.LastError = lastErr.String
	return e, nil
}

// FindOutboxByMessageID возвращает запись для конкретного сообщения.
func (s *Store) FindOutboxByMessageID(ctx context.Context, messageID int64) (*OutboxEntry, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT path, message_id, retries, last_error, priority,
		       CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		  FROM gammu_outbox WHERE message_id = ?
	`, messageID)
	e := &OutboxEntry{}
	var lastErr sql.NullString
	if err := row.Scan(
		&e.Path, &e.MessageID, &e.Retries, &lastErr, &e.Priority,
		&e.CreatedAt, &e.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOutboxEntryNotFound
		}
		return nil, err
	}
	e.LastError = lastErr.String
	return e, nil
}

// UpdateOutboxAfterFailure обновляет запись после неудачной попытки:
// новый путь (файл переименован в приоритет ниже), счётчик retries,
// последняя ошибка, новый приоритет.
func (s *Store) UpdateOutboxAfterFailure(
	ctx context.Context, oldPath, newPath string,
	retries int, priority, lastError string,
) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE gammu_outbox
		   SET path = ?, retries = ?, priority = ?,
		       last_error = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE path = ?
	`, newPath, retries, priority, nullString(lastError), oldPath)
	if err != nil {
		return fmt.Errorf("UPDATE gammu_outbox: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrOutboxEntryNotFound
	}
	return nil
}

// DeleteOutboxEntry удаляет запись. Не ошибка, если записи нет.
func (s *Store) DeleteOutboxEntry(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM gammu_outbox WHERE path = ?`, path)
	return err
}

// ListOutboxEntries возвращает все записи, отсортированные по
// приоритету и пути (лексикографически — так же, как Gammu
// выбирает файлы для отправки).
func (s *Store) ListOutboxEntries(ctx context.Context) ([]*OutboxEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT path, message_id, retries, last_error, priority,
		       CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		  FROM gammu_outbox
		 ORDER BY priority ASC, path ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*OutboxEntry
	for rows.Next() {
		e := &OutboxEntry{}
		var lastErr sql.NullString
		if err := rows.Scan(
			&e.Path, &e.MessageID, &e.Retries, &lastErr, &e.Priority,
			&e.CreatedAt, &e.UpdatedAt,
		); err != nil {
			return nil, err
		}
		e.LastError = lastErr.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// OrphanOutboxMessages возвращает исходящие сообщения со
// source='gammu-outbox', для которых нет записи в gammu_outbox.
// Такие записи появляются, если сервис упал между переименованием
// файла и обновлением БД. При старте сервис переводит их в failed.
func (s *Store) OrphanOutboxMessages(ctx context.Context) ([]*Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+messageColumns+`
		  FROM messages m
		 WHERE m.source = 'gammu-outbox'
		   AND m.status NOT IN ('sent', 'delivered', 'sent_no_dr', 'failed')
		   AND NOT EXISTS (SELECT 1 FROM gammu_outbox g
		                    WHERE g.message_id = m.id)
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// StaleOutboxPaths возвращает пути из gammu_outbox, которых больше
// нет на диске (файл удалён или переименован вне сервиса).
// Используется при старте для очистки.
func (s *Store) StaleOutboxPaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path FROM gammu_outbox ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
