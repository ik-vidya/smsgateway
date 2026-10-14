// Package storage, файл storage.go — открытие базы данных, применение
// миграций, общие операции.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store — обёртка над *sql.DB с репозиториями.
type Store struct {
	db *sql.DB
}

// Open открывает (или создаёт) базу данных по пути path,
// настраивает WAL и busy timeout, применяет миграции.
//
// Если path == ":memory:" — открывается in-memory база для тестов.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := buildDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("открытие БД: %w", err)
	}

	// SQLite не любит конкурентные записи из разных соединений.
	// Одно соединение — самое безопасное решение; для нашего
	// уровня нагрузки этого хватает с запасом.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping БД: %w", err)
	}

	s := &Store{db: db}
	if err := s.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("миграции: %w", err)
	}
	return s, nil
}

// buildDSN собирает строку подключения modernc.org/sqlite.
func buildDSN(path string) string {
	if path == ":memory:" {
		return ":memory:?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)",
		abs,
	)
}

// Close закрывает БД.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB даёт доступ к *sql.DB для тестов или нестандартных запросов.
func (s *Store) DB() *sql.DB { return s.db }

// Migrate применяет неприменённые миграции из встроенной директории.
// Идемпотентна: повторный запуск не делает ничего.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("создание schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("чтение migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		applied, err := s.isMigrationApplied(ctx, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := s.applyMigration(ctx, name); err != nil {
			return fmt.Errorf("применение %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) isMigrationApplied(ctx context.Context, name string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM schema_migrations WHERE version = ?`, name,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) applyMigration(ctx context.Context, name string) error {
	body, err := migrationsFS.ReadFile("migrations/" + name)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version) VALUES (?)`, name,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// nowUTC — единая точка получения текущего времени для записи.
func nowUTC() time.Time {
	return time.Now().UTC()
}