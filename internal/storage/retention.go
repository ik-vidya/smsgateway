// Package storage, файл retention.go — фоновая очистка старых данных.
package storage

import (
	"context"
	"io"
	"log/slog"
	"time"
)

// RetentionPolicy — правила очистки.
type RetentionPolicy struct {
	// Messages — удалять сообщения старше этого возраста.
	// 0 = хранить вечно.
	Messages time.Duration

	// DeliveryReports — удалять DR старше этого возраста.
	// 0 = использовать Messages × 2.
	DeliveryReports time.Duration

	// PendingPartsTTL — TTL незавершённых multipart-сборок.
	// 0 = 15 минут.
	PendingPartsTTL time.Duration

	// Interval — как часто запускать очистку.
	// 0 = 1 час.
	Interval time.Duration
}

func (p RetentionPolicy) withDefaults() RetentionPolicy {
	if p.PendingPartsTTL <= 0 {
		p.PendingPartsTTL = 15 * time.Minute
	}
	if p.Interval <= 0 {
		p.Interval = time.Hour
	}
	if p.DeliveryReports <= 0 && p.Messages > 0 {
		p.DeliveryReports = p.Messages * 2
	}
	return p
}

// RunRetention запускает фоновую очистку. Блокируется до отмены ctx.
// Логирует каждую итерацию через logger.
func (s *Store) RunRetention(ctx context.Context, policy RetentionPolicy, logger *slog.Logger) {
	policy = policy.withDefaults()

	// Первая очистка — сразу после старта (не ждём час).
	s.runRetentionOnce(ctx, policy, logger)

	ticker := time.NewTicker(policy.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("retention: остановлен")
			return
		case <-ticker.C:
			s.runRetentionOnce(ctx, policy, logger)
		}
	}
}

func (s *Store) runRetentionOnce(ctx context.Context, policy RetentionPolicy, logger *slog.Logger) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	} // Сообщения.
	if policy.Messages > 0 {
		cutoff := nowUTC().Add(-policy.Messages)
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM messages WHERE created_at < ?`, cutoff)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("retention: удаление messages", "err", err)
			}
		} else if n, _ := res.RowsAffected(); n > 0 {
			logger.Info("retention: удалено messages", "count", n)
		}
	}

	// Delivery reports.
	if policy.DeliveryReports > 0 {
		cutoff := nowUTC().Add(-policy.DeliveryReports)
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM delivery_reports WHERE received_at < ?`, cutoff)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("retention: удаление delivery_reports", "err", err)
			}
		} else if n, _ := res.RowsAffected(); n > 0 {
			logger.Info("retention: удалено delivery_reports", "count", n)
		}
	}

	// Просроченные pending_parts.
	if n, err := s.ExpirePendingParts(ctx, policy.PendingPartsTTL); err != nil {
		if ctx.Err() == nil {
			logger.Error("retention: очистка pending_parts", "err", err)
		}
	} else if n > 0 {
		logger.Info("retention: удалено pending_parts", "count", n)
	}

	// Просроченные pending_references.
	if expired, err := s.ExpirePendingReferences(ctx); err != nil {
		if ctx.Err() == nil {
			logger.Error("retention: очистка pending_references", "err", err)
		}
	} else if len(expired) > 0 {
		logger.Info("retention: истекло pending_references", "count", len(expired))
		// TODO(service): перевести связанные messages в sent_no_dr.
	}
}
