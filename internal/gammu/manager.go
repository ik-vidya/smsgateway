// Package gammu, файл manager.go — watcher outbox и диспетчер
// отправки.
//
// Manager следит за каталогом outbox: файлы OUT*.txt сортируются
// лексикографически (по приоритету A-Z, потом по имени), парсятся
// и отправляются через Sender. На каждое отправленное сообщение
// создаётся запись в messages (source='gammu-outbox') и в
// gammu_outbox (состояние ретраев).
//
// Жизненный цикл файла:
//
//	outbox/OUTB_<recipient>_00.txt
//	   │
//	   ├─ успех  → sent/OUTB_<recipient>_00.txt
//	   │           messages.status = sent
//	   │
//	   └─ ошибка → outbox/OUTC_<recipient>_00.txt  (приоритет понижен)
//	               retries++, last_error
//	               если нельзя понизить (Z) или retries >= max:
//	                 error/OUTB_<recipient>_00.txt
//	                 messages.status = failed
//
// Manager не знает про modem.Modem и storage.Store напрямую:
// зависимости выражены интерфейсами Sender и OutboxStore.
package gammu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrOutboxEntryNotFound — запись о файле отсутствует в хранилище.
// Соответствует storage.ErrOutboxEntryNotFound; адаптер в
// internal/service транслирует одну в другую, чтобы gammu не
// импортировал storage.
var ErrOutboxEntryNotFound = errors.New("gammu: outbox entry not found")

// Sender — минимальный интерфейс отправки SMS.
//
// Возвращает reference (TP-MR от модема) при успехе. Ошибка —
// любая причина, по которой SMS не ушло (нет сети, модем занят,
// CMS ERROR). Manager сам решает, повторять или нет — он
// повторяет всё, кроме ошибок с явным признаком "permanent".
type Sender interface {
	Send(ctx context.Context, peer, text string,
		requestDR, flash bool) (reference int64, err error)
}

// OutboxStore — операции хранилища, нужные Manager-у.
// *storage.Store удовлетворяет этому интерфейсу.
type OutboxStore interface {
	// Сообщения.
	CreateOutboxMessage(ctx context.Context, peer, text string) (int64, error)
	MarkOutboxSending(ctx context.Context, id int64) error
	MarkOutboxSent(ctx context.Context, id, reference int64) error
	MarkOutboxFailed(ctx context.Context, id int64, errMsg string) error

	// Состояние файла.
	CreateOutboxEntry(ctx context.Context, path string,
		messageID int64, priority string) error
	GetOutboxEntry(ctx context.Context, path string) (*OutboxEntry, error)
	UpdateOutboxFailure(ctx context.Context,
		oldPath, newPath string, retries int, priority, lastError string) error
	DeleteOutboxEntry(ctx context.Context, path string) error
}

// OutboxEntry — состояние файла в outbox (зеркало storage.OutboxEntry,
// чтобы gammu не импортировал storage).
type OutboxEntry struct {
	Path      string
	MessageID int64
	Retries   int
	LastError string
	Priority  string
}

// ManagerConfig — параметры Manager.
type ManagerConfig struct {
	// InboxDir, OutboxDir, SentDir, ErrorDir — обязательны.
	InboxDir  string
	OutboxDir string
	SentDir   string
	ErrorDir  string

	// InboxFormat — формат файлов в inbox ("utf8" | "unicode").
	InboxFormat string

	// WatchInterval — период сканирования outbox. 0 — 5s.
	WatchInterval time.Duration

	// MaxAttempts — всего попыток отправки (включая первую).
	// 0 — 5.
	MaxAttempts int

	// GracePeriod — файл, изменённый меньше этого времени назад,
	// пропускается (может ещё писаться). 0 — 2s.
	GracePeriod time.Duration
}

func (c ManagerConfig) withDefaults() ManagerConfig {
	if c.WatchInterval <= 0 {
		c.WatchInterval = 5 * time.Second
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.GracePeriod <= 0 {
		c.GracePeriod = 2 * time.Second
	}
	if c.InboxFormat == "" {
		c.InboxFormat = "utf8"
	}
	return c
}

// Manager — watcher outbox + диспетчер.
type Manager struct {
	cfg    ManagerConfig
	sender Sender
	store  OutboxStore
	inbox  *InboxWriter
	logger *slog.Logger

	// sendInterval — пауза между отправками. Управляется
	// GAMMU_WATCH_INTERVAL, чтобы не спамить модем.
	sendInterval time.Duration
}

// NewManager создаёт Manager.
//
// sender может быть nil — тогда Manager работает в режиме
// «только inbox»: watcher не запускается, WriteIncoming работает
// как обычно. Это нужно при SERIAL_PORT=none.
func NewManager(cfg ManagerConfig, sender Sender, store OutboxStore,
	inbox *InboxWriter, logger *slog.Logger) (*Manager, error) {

	if logger == nil {
		logger = slog.Default()
	}
	if inbox == nil {
		return nil, errors.New("inbox writer обязателен")
	}
	if sender != nil && store == nil {
		return nil, errors.New("store обязателен при заданном sender")
	}
	cfg = cfg.withDefaults()

	return &Manager{
		cfg:          cfg,
		sender:       sender,
		store:        store,
		inbox:        inbox,
		logger:       logger,
		sendInterval: cfg.WatchInterval,
	}, nil
}

// Inbox возвращает writer для экспорта принятых SMS.
func (m *Manager) Inbox() *InboxWriter { return m.inbox }

// Run запускает watcher outbox. Блокируется до отмены ctx.
// Если sender == nil, ждёт ctx.Done() без работы.
func (m *Manager) Run(ctx context.Context) error {
	if err := m.prepareDirs(); err != nil {
		return err
	}
	if m.sender == nil {
		m.logger.Info("gammu: sender не задан, watcher не запущен")
		<-ctx.Done()
		return nil
	}

	m.logger.Info("gammu: watcher outbox запущен",
		"outbox", m.cfg.OutboxDir,
		"sent", m.cfg.SentDir,
		"error", m.cfg.ErrorDir,
		"interval", m.cfg.WatchInterval,
		"max_attempts", m.cfg.MaxAttempts,
	)

	// Первый проход — сразу.
	m.scanAndDispatch(ctx)

	tick := time.NewTicker(m.cfg.WatchInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("gammu: watcher outbox остановлен")
			return nil
		case <-tick.C:
			m.scanAndDispatch(ctx)
		}
	}
}

// SendNow выполняет один проход watcher-а немедленно. Полезно
// для CLI-команды `smsgateway gammu-outbox`.
func (m *Manager) SendNow(ctx context.Context) (Stats, error) {
	if m.sender == nil {
		return Stats{}, errors.New("sender не задан")
	}
	if err := m.prepareDirs(); err != nil {
		return Stats{}, err
	}
	return m.scan(ctx), nil
}

// Stats — статистика одного прохода.
type Stats struct {
	Scanned int
	Sent    int
	Retried int
	Failed  int
	Skipped int
}

func (m *Manager) prepareDirs() error {
	for _, d := range []string{
		m.cfg.InboxDir, m.cfg.OutboxDir, m.cfg.SentDir, m.cfg.ErrorDir,
	} {
		if d == "" {
			return errors.New("gammu: не все каталоги заданы")
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("создание каталога %q: %w", d, err)
		}
	}
	return nil
}

// scanAndDispatch — обёртка для логов: scan + отчёт.
func (m *Manager) scanAndDispatch(ctx context.Context) {
	st, err := m.scanSafely(ctx)
	if err != nil {
		m.logger.Error("gammu: проход watcher", "err", err)
		return
	}
	if st.Scanned == 0 {
		return
	}
	m.logger.Info("gammu: проход завершён",
		"scanned", st.Scanned,
		"sent", st.Sent,
		"retried", st.Retried,
		"failed", st.Failed,
		"skipped", st.Skipped,
	)
}

func (m *Manager) scanSafely(ctx context.Context) (Stats, error) {
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("gammu: паника в scan", "panic", r)
		}
	}()
	return m.scan(ctx), nil
}

// scan выполняет один проход: читает директорию, сортирует,
// обрабатывает по одному файлу за раз (не параллельно, чтобы не
// гнать SMS в модем одновременно).
func (m *Manager) scan(ctx context.Context) Stats {
	var st Stats

	files, err := m.listOutboxFiles()
	if err != nil {
		m.logger.Error("gammu: чтение outbox", "err", err)
		return st
	}
	st.Scanned = len(files)

	for _, name := range files {
		select {
		case <-ctx.Done():
			return st
		default:
		}

		res, err := m.processFile(ctx, name)
		if err != nil {
			m.logger.Warn("gammu: обработка файла",
				"name", name, "err", err)
			st.Failed++
			continue
		}
		switch res {
		case resultSent:
			st.Sent++
		case resultRetried:
			st.Retried++
		case resultFailed:
			st.Failed++
		case resultSkipped:
			st.Skipped++
		}
	}
	return st
}

// listOutboxFiles возвращает отсортированный список имён файлов
// в outbox, которые выглядят как валидные OUT<...>.txt[df].
func (m *Manager) listOutboxFiles() ([]string, error) {
	entries, err := os.ReadDir(m.cfg.OutboxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	now := time.Now()
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "OUT") {
			continue
		}
		// Grace period: файл мог ещё не дописаться.
		info, err := e.Info()
		if err == nil && now.Sub(info.ModTime()) < m.cfg.GracePeriod {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

type fileResult int

const (
	resultSkipped fileResult = iota
	resultSent
	resultRetried
	resultFailed
)

// processFile обрабатывает один файл из outbox.
//
// Если для текущего пути уже есть запись в gammu_outbox — это
// retry: берём из записи MessageID и retries, новое сообщение
// не создаём. Если записи нет — свежий файл: создаём и message,
// и entry.
func (m *Manager) processFile(ctx context.Context, name string) (fileResult, error) {
	full := filepath.Join(m.cfg.OutboxDir, name)

	fn, err := ParseOutgoingFileName(name)
	if err != nil {
		if mvErr := m.moveToError(full, name, err.Error()); mvErr != nil {
			return resultSkipped, mvErr
		}
		return resultFailed, fmt.Errorf("некорректное имя: %w", err)
	}

	content, err := m.readOutboxFile(full)
	if err != nil {
		if mvErr := m.moveToError(full, name, err.Error()); mvErr != nil {
			return resultSkipped, mvErr
		}
		return resultFailed, err
	}
	msg, err := ParseFile(content)
	if err != nil {
		if mvErr := m.moveToError(full, name, err.Error()); mvErr != nil {
			return resultSkipped, mvErr
		}
		return resultFailed, err
	}

	text, src := msg.EffectiveText()

	// debug-логирование: что попало в поля и какой источник
	// текста выбрал EffectiveText. При LOG_LEVEL=debug видно,
	// почему файл ушёл в error («пустой текст») — например,
	// не нашлось ни известных ключей, ни plain-текста.
	m.logger.Debug("gammu: файл разобран",
		"name", name,
		"fields_count", len(msg.Fields),
		"has_textdecoded", msg.TextDecoded != "",
		"has_text", msg.Text != "",
		"has_number", msg.Number != "",
		"effective_len", len(text),
		"effective_source", src,
	)
	if text == "" {
		err := errors.New("пустой текст сообщения")
		if mvErr := m.moveToError(full, name, err.Error()); mvErr != nil {
			return resultSkipped, mvErr
		}
		return resultFailed, err
	}

	recipient := msg.Number
	if recipient == "" {
		recipient = fn.Recipient
	}
	if recipient == "" {
		err := errors.New("не удалось определить получателя")
		if mvErr := m.moveToError(full, name, err.Error()); mvErr != nil {
			return resultSkipped, mvErr
		}
		return resultFailed, err
	}

	// Свежий файл или retry?
	entry, entryErr := m.store.GetOutboxEntry(ctx, full)

	var msgID int64
	var retries int
	var currentPrio string

	switch {
	case entryErr == nil:
		// Retry: файл уже знаком. Проверяем лимит ДО
		// отправки — если он исчерпан, сразу в error без
		// попытки. Это правильно: MaxAttempts — всего
		// попыток, включая первую.
		if entry.Retries >= m.cfg.MaxAttempts {
			m.logger.Warn("gammu: лимит попыток исчерпан",
				"name", name, "retries", entry.Retries,
				"max", m.cfg.MaxAttempts,
				"last_error", entry.LastError)
			lastErr := errors.New(entry.LastError)
			if entry.LastError == "" {
				lastErr = errors.New("max attempts reached")
			}
			return m.failPermanently(ctx, full, name,
				entry.MessageID, lastErr)
		}
		msgID = entry.MessageID
		retries = entry.Retries
		currentPrio = entry.Priority
		if currentPrio == "" {
			currentPrio = fn.Priority
		}
		m.logger.Info("gammu: повторная отправка из outbox",
			"name", name, "msg_id", msgID,
			"retries", retries, "priority", currentPrio,
			"peer", recipient)

	case errors.Is(entryErr, ErrOutboxEntryNotFound):
		// Свежий файл: создаём message и entry.
		id, err := m.store.CreateOutboxMessage(ctx, recipient, text)
		if err != nil {
			return resultSkipped, fmt.Errorf("CreateOutboxMessage: %w", err)
		}
		if err := m.store.CreateOutboxEntry(ctx, full, id, fn.Priority); err != nil {
			// Откатывать сообщение не будем — сироту подберёт
			// OrphanOutboxMessages при следующем старте.
			return resultSkipped, fmt.Errorf("CreateOutboxEntry: %w", err)
		}
		msgID = id
		retries = 0
		currentPrio = fn.Priority
		m.logger.Info("gammu: отправка из outbox",
			"name", name, "msg_id", msgID,
			"peer", recipient, "priority", fn.Priority,
			"dr", fn.RequestDR, "flash", fn.Flash,
			"source", src)

	default:
		return resultSkipped, fmt.Errorf("GetOutboxEntry: %w", entryErr)
	}

	if fn.Flash {
		m.logger.Warn("gammu: flash SMS не поддерживается, "+
			"отправляем как обычное", "name", name)
	}
	if err := m.store.MarkOutboxSending(ctx, msgID); err != nil {
		m.logger.Warn("gammu: MarkOutboxSending", "err", err)
	}

	ref, sendErr := m.sender.Send(ctx, recipient, text,
		fn.RequestDR, fn.Flash)
	if sendErr == nil {
		return m.handleSuccess(ctx, full, name, msgID, ref)
	}
	return m.handleFailure(ctx, full, name, fn,
		msgID, retries, currentPrio, sendErr)
}

func (m *Manager) readOutboxFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("чтение %q: %w", path, err)
	}
	content, err := DecodeText(raw)
	if err != nil {
		return "", fmt.Errorf("декодирование %q: %w", path, err)
	}

	// debug-логирование: при LOG_LEVEL=debug видно, что
	// пришло с диска и что получилось после декодирования.
	// Полезно для диагностики «файл в error»: сразу видна
	// кодировка (по первым байтам) и длина.
	m.logger.Debug("gammu: файл прочитан",
		"path", path,
		"raw_len", len(raw),
		"raw_first_bytes", hexFirstN(raw, 8),
		"decoded_len", len(content),
		"decoded_first_chars", firstChars(content, 60),
	)
	return content, nil
}

// hexFirstN возвращает hex первых n байт.
func hexFirstN(b []byte, n int) string {
	if len(b) < n {
		n = len(b)
	}
	return fmt.Sprintf("%x", b[:n])
}

// firstChars возвращает первые n рун строки (для debug-лога).
func firstChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (m *Manager) handleSuccess(ctx context.Context,
	full, name string, msgID int64, ref int64,
) (fileResult, error) {
	if err := m.store.MarkOutboxSent(ctx, msgID, ref); err != nil {
		m.logger.Warn("gammu: MarkOutboxSent", "err", err)
	}
	if err := m.store.DeleteOutboxEntry(ctx, full); err != nil {
		m.logger.Warn("gammu: DeleteOutboxEntry", "err", err)
	}
	dst := filepath.Join(m.cfg.SentDir, name)
	if err := m.moveFile(full, dst); err != nil {
		return resultSent, fmt.Errorf("перемещение в sent: %w", err)
	}
	m.logger.Info("gammu: отправлено",
		"name", name, "msg_id", msgID, "ref", ref, "dst", dst)
	return resultSent, nil
}

func (m *Manager) handleFailure(ctx context.Context,
	full, name string, fn FileName, msgID int64,
	retries int, currentPrio string, sendErr error,
) (fileResult, error) {

	nextRetries := retries + 1
	nextPrio, canLower := Lower(currentPrio)

	switch {
	case nextRetries >= m.cfg.MaxAttempts:
		m.logger.Warn("gammu: лимит попыток исчерпан",
			"name", name, "retries", nextRetries,
			"max", m.cfg.MaxAttempts, "err", sendErr)
		return m.failPermanently(ctx, full, name, msgID, sendErr)

	case !canLower:
		m.logger.Warn("gammu: приоритет Z, дальше только error",
			"name", name, "err", sendErr)
		return m.failPermanently(ctx, full, name, msgID, sendErr)

	default:
		return m.retryWithLowerPriority(ctx, full, name, fn,
			msgID, nextRetries, nextPrio, sendErr)
	}
}

// retryWithLowerPriority фиксирует неудачу в БД, но НЕ
// переименовывает файл.
//
// Почему: при пересборке имени теряется структура. Из полной
// формы OUTA20261018_000647_6467_+371..._Admin.txtd
// генерируется короткая OUTB_+371..._99.txtd — теряются
// timestamp, serial (обрезается до 99) и note. Пользователь
// видит в sent файл, не имеющий отношения к исходному.
//
// Правильно: файл остаётся с тем же именем, состояние
// (retries, priority, last_error) — в БД. При следующем
// проходе watcher-а entry будет виден, retries проверен
// до попытки.
func (m *Manager) retryWithLowerPriority(ctx context.Context,
	full, name string, fn FileName,
	msgID int64, retries int, nextPrio string, sendErr error,
) (fileResult, error) {

	// Имя файла не меняем: обновляем только состояние в БД.
	// oldPath == newPath.
	if err := m.store.UpdateOutboxFailure(ctx,
		full, full, retries, nextPrio, sendErr.Error(),
	); err != nil {
		return resultSkipped, fmt.Errorf("UpdateOutboxFailure: %w", err)
	}
	m.logger.Info("gammu: retry, файл остаётся в outbox",
		"name", name,
		"retries", retries, "prio", nextPrio, "err", sendErr)
	return resultRetried, nil
}

func (m *Manager) failPermanently(ctx context.Context,
	full, name string, msgID int64, sendErr error,
) (fileResult, error) {

	if err := m.store.MarkOutboxFailed(ctx, msgID, sendErr.Error()); err != nil {
		m.logger.Warn("gammu: MarkOutboxFailed", "err", err)
	}
	if err := m.store.DeleteOutboxEntry(ctx, full); err != nil {
		m.logger.Warn("gammu: DeleteOutboxEntry", "err", err)
	}
	if err := m.moveToError(full, name, sendErr.Error()); err != nil {
		return resultSkipped, err
	}
	return resultFailed, nil
}

// moveToError переносит файл в error-каталог. Причина пишется
// в лог (отдельный .reason.txt не создаём — решено так).
func (m *Manager) moveToError(full, name, reason string) error {
	dst := filepath.Join(m.cfg.ErrorDir, name)
	if err := m.moveFile(full, dst); err != nil {
		return fmt.Errorf("перемещение в error: %w", err)
	}
	m.logger.Warn("gammu: файл в error",
		"name", name, "reason", reason, "dst", dst)
	return nil
}

// moveFile — атомарное перемещение в пределах одной ФС.
// Если dst существует, добавляется суффикс .<unixnano>.
func (m *Manager) moveFile(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		dst = fmt.Sprintf("%s.%d", dst, time.Now().UnixNano())
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("rename %q → %q: %w", src, dst, err)
	}
	return nil
}
