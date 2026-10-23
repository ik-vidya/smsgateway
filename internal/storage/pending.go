// Package storage, файл pending.go — управление бронированиями TP-MR
// и незавершёнными multipart-сборками.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrReferenceBusy — reference не найден (просрочен, уже обработан
// или не был забронирован). Историческое название.
var ErrReferenceBusy = errors.New("reference не найден (просрочен или уже обработан)")

// ErrPendingPartNotFound — сборка не найдена.
var ErrPendingPartNotFound = errors.New("pending part не найдена")

// ==================== pending_references ====================

// ReserveReference пытается занять reference для messageID.
// Возвращает ErrReferenceBusy, если reference уже занят.
func (s *Store) ReserveReference(ctx context.Context, reference, messageID int64, ttl time.Duration) error {
	now := nowUTC()
	expires := now.Add(ttl)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pending_references (reference, message_id, created_at, expires_at)
		VALUES (?, ?, ?, ?)
	`, reference, messageID, now, expires)
	if err != nil {
		// UNIQUE-нарушение — reference занят.
		return ErrReferenceBusy
	}
	return nil
}

// ReleaseReference освобождает reference. Не ошибка, если его нет.
func (s *Store) ReleaseReference(ctx context.Context, reference int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM pending_references WHERE reference = ?`, reference)
	return err
}

// FindPendingReference возвращает messageID по reference.
func (s *Store) FindPendingReference(ctx context.Context, reference int64) (int64, error) {
	var messageID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT message_id FROM pending_references WHERE reference = ?`,
		reference,
	).Scan(&messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrReferenceBusy
	}
	return messageID, err
}

// ListFreeReferences возвращает список свободных reference
// (0..255, исключая занятые).
func (s *Store) ListFreeReferences(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT reference FROM pending_references`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	busy := make(map[int64]bool)
	for rows.Next() {
		var r int64
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		busy[r] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var free []int64
	for i := int64(0); i < 256; i++ {
		if !busy[i] {
			free = append(free, i)
		}
	}
	return free, nil
}

// ExpirePendingReferences удаляет записи, у которых истёк expires_at,
// и возвращает их список (reference + message_id) — чтобы сервисный
// слой мог перевести соответствующие сообщения в sent_no_dr.
func (s *Store) ExpirePendingReferences(ctx context.Context) ([]PendingReference, error) {
	now := nowUTC()

	rows, err := s.db.QueryContext(ctx, `
		SELECT reference, message_id, created_at, expires_at
		  FROM pending_references
		 WHERE expires_at <= ?
	`, now)
	if err != nil {
		return nil, err
	}
	var expired []PendingReference
	for rows.Next() {
		var p PendingReference
		if err := rows.Scan(&p.Reference, &p.MessageID, &p.CreatedAt, &p.ExpiresAt); err != nil {
			rows.Close()
			return nil, err
		}
		expired = append(expired, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(expired) == 0 {
		return nil, nil
	}

	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM pending_references WHERE expires_at <= ?`, now,
	); err != nil {
		return nil, err
	}
	return expired, nil
}

// ==================== pending_parts ====================

// SavePendingPart сохраняет одну часть multipart-сообщения.
// Возвращает complete=true, если все части собраны, и итоговый текст.
func (s *Store) SavePendingPart(ctx context.Context, reference int64, peer string, total, seq int, text string) (complete bool, fullText string, err error) {
	if seq < 1 || seq > total {
		return false, "", fmt.Errorf("неверный seq %d из %d", seq, total)
	}
	if total < 1 || total > 255 {
		return false, "", fmt.Errorf("неверный total %d", total)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, "", err
	}
	defer func() { _ = tx.Rollback() }()

	// Найти или создать pending_parts.
	var pendingID int64
	var received int
	err = tx.QueryRowContext(ctx,
		`SELECT id, received FROM pending_parts WHERE reference = ? AND peer = ?`,
		reference, peer,
	).Scan(&pendingID, &received)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx, `
			INSERT INTO pending_parts (reference, peer, total, received)
			VALUES (?, ?, ?, 0)
		`, reference, peer, total)
		if err != nil {
			return false, "", err
		}
		pendingID, err = res.LastInsertId()
		if err != nil {
			return false, "", err
		}
		received = 0
	case err != nil:
		return false, "", err
	}

	// Вставить тело части. Если уже существует — игнорируем.
	res, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO pending_part_bodies (pending_id, seq, text)
		VALUES (?, ?, ?)
	`, pendingID, seq, text)
	if err != nil {
		return false, "", err
	}
	affected, _ := res.RowsAffected()
	if affected > 0 {
		received++
		if _, err := tx.ExecContext(ctx,
			`UPDATE pending_parts SET received = ? WHERE id = ?`,
			received, pendingID,
		); err != nil {
			return false, "", err
		}
	}

	if received < total {
		if err := tx.Commit(); err != nil {
			return false, "", err
		}
		return false, "", nil
	}

	// Все части собраны — собираем текст.
	bodies, err := loadBodiesTx(ctx, tx, pendingID)
	if err != nil {
		return false, "", err
	}
	var joined string
	for i := 1; i <= total; i++ {
		joined += bodies[i]
	}

	// Удаляем pending.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM pending_parts WHERE id = ?`, pendingID,
	); err != nil {
		return false, "", err
	}

	if err := tx.Commit(); err != nil {
		return false, "", err
	}
	return true, joined, nil
}

func loadBodiesTx(ctx context.Context, tx *sql.Tx, pendingID int64) (map[int]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT seq, text FROM pending_part_bodies WHERE pending_id = ? ORDER BY seq`,
		pendingID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int]string)
	for rows.Next() {
		var seq int
		var text string
		if err := rows.Scan(&seq, &text); err != nil {
			return nil, err
		}
		out[seq] = text
	}
	return out, rows.Err()
}

// ExpirePendingParts удаляет сборки старше указанного TTL.
func (s *Store) ExpirePendingParts(ctx context.Context, ttl time.Duration) (int, error) {
	cutoff := nowUTC().Add(-ttl)
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM pending_parts WHERE created_at <= ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ListPendingParts возвращает все незавершённые сборки
// multipart-сообщений с уже полученными частями.
func (s *Store) ListPendingParts(ctx context.Context) ([]*PendingPart, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, reference, peer, total, received, created_at
		  FROM pending_parts
		 ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*PendingPart
	for rows.Next() {
		p := &PendingPart{}
		if err := rows.Scan(&p.ID, &p.Reference, &p.Peer, &p.Total, &p.Received, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}

	for _, p := range out {
		bodies, err := s.loadBodies(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		p.Bodies = bodies
	}
	return out, nil
}

func (s *Store) loadBodies(ctx context.Context, pendingID int64) (map[int]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, text FROM pending_part_bodies WHERE pending_id = ? ORDER BY seq`,
		pendingID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int]string)
	for rows.Next() {
		var seq int
		var text string
		if err := rows.Scan(&seq, &text); err != nil {
			return nil, err
		}
		out[seq] = text
	}
	return out, rows.Err()
}
