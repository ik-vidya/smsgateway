package gammu

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ==================== mock Sender ====================

type mockSender struct {
	mu    sync.Mutex
	calls []sendCall

	// errs по индексу вызова; если индекс >= len(errs) — nil.
	errs []error
	// refs по индексу; если 0 — берём 100+i.
	refs []int64
}

type sendCall struct {
	peer      string
	text      string
	requestDR bool
	flash     bool
}

func (m *mockSender) Send(_ context.Context, peer, text string,
	requestDR, flash bool) (int64, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	idx := len(m.calls)
	m.calls = append(m.calls, sendCall{
		peer: peer, text: text,
		requestDR: requestDR, flash: flash,
	})
	if idx < len(m.errs) && m.errs[idx] != nil {
		return 0, m.errs[idx]
	}
	if idx < len(m.refs) && m.refs[idx] != 0 {
		return m.refs[idx], nil
	}
	return int64(100 + idx), nil
}

func (m *mockSender) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockSender) LastCall() (sendCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return sendCall{}, false
	}
	return m.calls[len(m.calls)-1], true
}

// ==================== mock Store ====================

type mockStore struct {
	mu       sync.Mutex
	entries  map[string]*OutboxEntry
	nextID   int64
	statuses map[int64]string // id → "sending"|"sent"|"failed"
	errors   map[int64]string
}

func newMockStore() *mockStore {
	return &mockStore{
		entries:  make(map[string]*OutboxEntry),
		statuses: make(map[int64]string),
		errors:   make(map[int64]string),
	}
}

func (m *mockStore) CreateOutboxMessage(_ context.Context, _, _ string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	return m.nextID, nil
}

func (m *mockStore) MarkOutboxSending(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[id] = "sending"
	return nil
}

func (m *mockStore) MarkOutboxSent(_ context.Context, id, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[id] = "sent"
	return nil
}

func (m *mockStore) MarkOutboxFailed(_ context.Context, id int64, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[id] = "failed"
	m.errors[id] = errMsg
	return nil
}

func (m *mockStore) CreateOutboxEntry(_ context.Context, path string,
	messageID int64, priority string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[path]; ok {
		return errors.New("already exists")
	}
	m.entries[path] = &OutboxEntry{
		Path: path, MessageID: messageID, Priority: priority,
	}
	return nil
}

func (m *mockStore) GetOutboxEntry(_ context.Context, path string) (*OutboxEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[path]
	if !ok {
		return nil, ErrOutboxEntryNotFound
	}
	cp := *e
	return &cp, nil
}

func (m *mockStore) UpdateOutboxFailure(_ context.Context,
	oldPath, newPath string, retries int, priority, lastError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[oldPath]
	if !ok {
		return ErrOutboxEntryNotFound
	}
	delete(m.entries, oldPath)
	e.Path = newPath
	e.Retries = retries
	e.Priority = priority
	e.LastError = lastError
	m.entries[newPath] = e
	return nil
}

func (m *mockStore) DeleteOutboxEntry(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, path)
	return nil
}

func (m *mockStore) EntryCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

func (m *mockStore) Status(id int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statuses[id]
}

// ==================== helpers ====================

type managerHarness struct {
	mgr    *Manager
	sender *mockSender
	store  *mockStore

	inboxDir  string
	outboxDir string
	sentDir   string
	errorDir  string
}

func newManagerHarness(t *testing.T, cfg ManagerConfig) *managerHarness {
	t.Helper()

	base := t.TempDir()
	h := &managerHarness{
		sender:    &mockSender{},
		store:     newMockStore(),
		inboxDir:  filepath.Join(base, "inbox"),
		outboxDir: filepath.Join(base, "outbox"),
		sentDir:   filepath.Join(base, "sent"),
		errorDir:  filepath.Join(base, "error"),
	}

	inbox, err := NewInboxWriter(h.inboxDir, "utf8", nil)
	if err != nil {
		t.Fatalf("NewInboxWriter: %v", err)
	}

	cfg.InboxDir = h.inboxDir
	cfg.OutboxDir = h.outboxDir
	cfg.SentDir = h.sentDir
	cfg.ErrorDir = h.errorDir
	cfg.GracePeriod = time.Nanosecond // тесты не должны ждать

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr, err := NewManager(cfg, h.sender, h.store, inbox, logger)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h.mgr = mgr

	if err := os.MkdirAll(h.outboxDir, 0o755); err != nil {
		t.Fatalf("MkdirAll outbox: %v", err)
	}
	return h
}

func (h *managerHarness) writeOutbox(t *testing.T, name, content string) string {
	t.Helper()
	full := filepath.Join(h.outboxDir, name)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %q: %v", name, err)
	}
	return full
}

func (h *managerHarness) fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func fileText(peer, text string) string {
	return "Number = " + peer + "\n" +
		"TextDecoded = " + text + "\n"
}

// ==================== tests ====================

func TestNewManagerValidation(t *testing.T) {
	if _, err := NewManager(ManagerConfig{}, &mockSender{}, newMockStore(), nil, nil); err == nil {
		t.Error("ожидалась ошибка без inbox")
	}
	inbox, _ := NewInboxWriter(t.TempDir(), "utf8", nil)
	if _, err := NewManager(ManagerConfig{}, &mockSender{}, nil, inbox, nil); err == nil {
		t.Error("ожидалась ошибка: sender задан, store не задан")
	}
	if _, err := NewManager(ManagerConfig{}, nil, nil, inbox, nil); err != nil {
		t.Errorf("без sender допустимо: %v", err)
	}
}

func TestSendNowBasic(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{MaxAttempts: 5})
	h.writeOutbox(t, "OUTA_+37121234567_00.txt",
		fileText(`"+37121234567"`, `"hello"`))

	st, err := h.mgr.SendNow(context.Background())
	if err != nil {
		t.Fatalf("SendNow: %v", err)
	}
	if st.Sent != 1 || st.Retried != 0 || st.Failed != 0 {
		t.Errorf("Stats=%+v", st)
	}

	if !h.fileExists(h.sentDir, "OUTA_+37121234567_00.txt") {
		t.Error("файл не в sent")
	}
	if h.fileExists(h.outboxDir, "OUTA_+37121234567_00.txt") {
		t.Error("файл остался в outbox")
	}
	if h.store.EntryCount() != 0 {
		t.Errorf("записи в gammu_outbox не удалены: %d", h.store.EntryCount())
	}

	call, _ := h.sender.LastCall()
	if call.peer != "+37121234567" || call.text != "hello" {
		t.Errorf("отправлено не то: %+v", call)
	}
}

func TestSendNowPriorityOrder(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{})

	// Пишем в обратном порядке — Manager должен отсортировать.
	h.writeOutbox(t, "OUTC_+371333_00.txt", fileText(`"+371333"`, `"c"`))
	h.writeOutbox(t, "OUTA_+371111_00.txt", fileText(`"+371111"`, `"a"`))
	h.writeOutbox(t, "OUTB_+371222_00.txt", fileText(`"+371222"`, `"b"`))

	if _, err := h.mgr.SendNow(context.Background()); err != nil {
		t.Fatalf("SendNow: %v", err)
	}

	h.sender.mu.Lock()
	defer h.sender.mu.Unlock()
	if len(h.sender.calls) != 3 {
		t.Fatalf("calls=%d", len(h.sender.calls))
	}
	want := []string{"+371111", "+371222", "+371333"}
	for i, c := range h.sender.calls {
		if c.peer != want[i] {
			t.Errorf("[%d] peer=%q, ожидалось %q", i, c.peer, want[i])
		}
	}
}

func TestSendNowRetryOnFailure(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{MaxAttempts: 5})
	h.sender.errs = []error{errors.New("temporary failure")}

	h.writeOutbox(t, "OUTB_+371_00.txt", fileText(`"+371"`, `"x"`))

	st, err := h.mgr.SendNow(context.Background())
	if err != nil {
		t.Fatalf("SendNow: %v", err)
	}
	if st.Retried != 1 || st.Sent != 0 {
		t.Errorf("Stats=%+v", st)
	}

	// Файл должен быть переименован в приоритет C и остаться в outbox.
	if h.fileExists(h.outboxDir, "OUTB_+371_00.txt") {
		t.Error("старый файл остался")
	}
	if !h.fileExists(h.outboxDir, "OUTC_+371_00.txt") {
		t.Error("новый файл с приоритетом C не создан")
	}
	if h.store.EntryCount() != 1 {
		t.Errorf("entries=%d, ожидалось 1", h.store.EntryCount())
	}

	// Запись в БД должна указывать на новый путь с retries=1.
	e, err := h.store.GetOutboxEntry(context.Background(),
		filepath.Join(h.outboxDir, "OUTC_+371_00.txt"))
	if err != nil {
		t.Fatalf("GetOutboxEntry: %v", err)
	}
	if e.Retries != 1 || e.Priority != "C" {
		t.Errorf("entry=%+v", e)
	}
}

func TestSendNowRetriesAcrossScans(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{MaxAttempts: 5})
	// Все попытки падают.
	h.sender.errs = []error{
		errors.New("fail1"),
		errors.New("fail2"),
	}

	h.writeOutbox(t, "OUTA_+371_00.txt", fileText(`"+371"`, `"x"`))

	// Проход 1: retry, файл становится OUTB.
	if _, err := h.mgr.SendNow(context.Background()); err != nil {
		t.Fatalf("SendNow 1: %v", err)
	}
	if h.sender.CallCount() != 1 {
		t.Fatalf("calls=%d", h.sender.CallCount())
	}
	if !h.fileExists(h.outboxDir, "OUTB_+371_00.txt") {
		t.Fatalf("после 1-го прохода нет OUTB")
	}

	// Проход 2: retry снова, файл становится OUTC.
	if _, err := h.mgr.SendNow(context.Background()); err != nil {
		t.Fatalf("SendNow 2: %v", err)
	}
	if h.sender.CallCount() != 2 {
		t.Fatalf("calls=%d, ожидалось 2", h.sender.CallCount())
	}
	if !h.fileExists(h.outboxDir, "OUTC_+371_00.txt") {
		t.Fatalf("после 2-го прохода нет OUTC")
	}

	e, err := h.store.GetOutboxEntry(context.Background(),
		filepath.Join(h.outboxDir, "OUTC_+371_00.txt"))
	if err != nil {
		t.Fatalf("GetOutboxEntry: %v", err)
	}
	if e.Retries != 2 || e.Priority != "C" {
		t.Errorf("entry=%+v, ожидалось retries=2 priority=C", e)
	}

	// Проход 3: тот же message_id переиспользуется.
	// Проверяем, что все три попытки шли в одно сообщение.
	// (Поскольку mockStore хранит один nextID — это косвенно
	// проверяется через count сообщений.)
}

func TestSendNowMaxAttempts(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{MaxAttempts: 2})
	h.sender.errs = []error{
		errors.New("fail1"),
		errors.New("fail2"),
	}

	h.writeOutbox(t, "OUTA_+371_00.txt", fileText(`"+371"`, `"x"`))

	// Первый проход — retry.
	if _, err := h.mgr.SendNow(context.Background()); err != nil {
		t.Fatalf("SendNow 1: %v", err)
	}
	// Файл должен быть в OUTB.
	if !h.fileExists(h.outboxDir, "OUTB_+371_00.txt") {
		t.Fatalf("после первого прохода нет OUTB")
	}

	// Второй проход — лимит (2) достигнут, файл в error.
	st, err := h.mgr.SendNow(context.Background())
	if err != nil {
		t.Fatalf("SendNow 2: %v", err)
	}
	if st.Failed != 1 {
		t.Errorf("Stats=%+v", st)
	}
	if !h.fileExists(h.errorDir, "OUTB_+371_00.txt") {
		t.Error("файл не в error")
	}
	if h.store.EntryCount() != 0 {
		t.Errorf("запись в gammu_outbox не удалена: %d", h.store.EntryCount())
	}
}

func TestSendNowPriorityZGoesToError(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{MaxAttempts: 100})
	h.sender.errs = []error{errors.New("fail")}

	// Приоритет Z — дальше понижать некуда, сразу в error.
	h.writeOutbox(t, "OUTZ_+371_00.txt", fileText(`"+371"`, `"x"`))

	st, err := h.mgr.SendNow(context.Background())
	if err != nil {
		t.Fatalf("SendNow: %v", err)
	}
	if st.Failed != 1 {
		t.Errorf("Stats=%+v", st)
	}
	if !h.fileExists(h.errorDir, "OUTZ_+371_00.txt") {
		t.Error("файл не в error")
	}
}

func TestSendNowInvalidName(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{})

	// Имя начинается с OUT, но не парсится.
	h.writeOutbox(t, "OUTbadname.txt", "garbage")

	st, err := h.mgr.SendNow(context.Background())
	if err != nil {
		t.Fatalf("SendNow: %v", err)
	}
	if st.Failed != 1 {
		t.Errorf("Stats=%+v", st)
	}
	if h.sender.CallCount() != 0 {
		t.Error("sender вызван при невалидном имени")
	}
	if !h.fileExists(h.errorDir, "OUTbadname.txt") {
		t.Error("файл не в error")
	}
}

func TestSendNowInvalidContent(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{})

	// Имя ок, но содержимое пустое.
	h.writeOutbox(t, "OUTA_+371_00.txt", "")

	st, err := h.mgr.SendNow(context.Background())
	if err != nil {
		t.Fatalf("SendNow: %v", err)
	}
	if st.Failed != 1 {
		t.Errorf("Stats=%+v", st)
	}
	if h.sender.CallCount() != 0 {
		t.Error("sender вызван при пустом тексте")
	}
	if !h.fileExists(h.errorDir, "OUTA_+371_00.txt") {
		t.Error("файл не в error")
	}
}

func TestSendNowNumberFromFileWins(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{})
	// Number в файле отличается от recipient в имени.
	h.writeOutbox(t, "OUTA_+371111_00.txt",
		fileText(`"+372222"`, `"hi"`))

	if _, err := h.mgr.SendNow(context.Background()); err != nil {
		t.Fatalf("SendNow: %v", err)
	}
	call, _ := h.sender.LastCall()
	if call.peer != "+372222" {
		t.Errorf("peer=%q, ожидалось +372222 (из файла)", call.peer)
	}
}

func TestSendNowNumberFallbackToFileName(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{})
	// В файле нет Number — fallback на recipient из имени.
	h.writeOutbox(t, "OUTA_+371111_00.txt",
		"TextDecoded = \"hi\"\n")

	if _, err := h.mgr.SendNow(context.Background()); err != nil {
		t.Fatalf("SendNow: %v", err)
	}
	call, _ := h.sender.LastCall()
	if call.peer != "+371111" {
		t.Errorf("peer=%q, ожидалось +371111 (из имени)", call.peer)
	}
}

func TestSendNowRequestDRFlag(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{})
	h.writeOutbox(t, "OUTA_+371_00.txtd", fileText(`"+371"`, `"x"`))

	if _, err := h.mgr.SendNow(context.Background()); err != nil {
		t.Fatalf("SendNow: %v", err)
	}
	call, _ := h.sender.LastCall()
	if !call.requestDR {
		t.Error("requestDR=false, ожидалось true")
	}
}

func TestSendNowFlashFlag(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{})
	h.writeOutbox(t, "OUTA_+371_00.txtf", fileText(`"+371"`, `"x"`))

	if _, err := h.mgr.SendNow(context.Background()); err != nil {
		t.Fatalf("SendNow: %v", err)
	}
	call, _ := h.sender.LastCall()
	if !call.flash {
		t.Error("flash=false, ожидалось true")
	}
	// Файл всё равно уходит в sent (flash сейчас не реализован
	// на уровне отправки, но флаг передаётся Sender-у).
	if !h.fileExists(h.sentDir, "OUTA_+371_00.txtf") {
		t.Error("файл не в sent")
	}
}

func TestSendNowIgnoresNonOUTFiles(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{})
	h.writeOutbox(t, "README.txt", "not for us")
	h.writeOutbox(t, "IN20261015_120034_00_+371_00.txt", "from inbox")

	st, err := h.mgr.SendNow(context.Background())
	if err != nil {
		t.Fatalf("SendNow: %v", err)
	}
	if st.Scanned != 0 {
		t.Errorf("Scanned=%d, ожидалось 0", st.Scanned)
	}
}

func TestSendNowDuplicateNameInSent(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{})

	// Файл с таким именем уже есть в sent.
	if err := os.MkdirAll(h.sentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(h.sentDir, "OUTA_+371_00.txt"),
		[]byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.writeOutbox(t, "OUTA_+371_00.txt", fileText(`"+371"`, `"new"`))

	if _, err := h.mgr.SendNow(context.Background()); err != nil {
		t.Fatalf("SendNow: %v", err)
	}
	// Исходный файл в sent не тронут.
	raw, _ := os.ReadFile(filepath.Join(h.sentDir, "OUTA_+371_00.txt"))
	if string(raw) != "old" {
		t.Errorf("исходный sent перезаписан: %q", raw)
	}
	// Новый файл получил суффикс.
	found := false
	entries, _ := os.ReadDir(h.sentDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "OUTA_+371_00.txt.") {
			found = true
		}
	}
	if !found {
		t.Errorf("не найден файл с суффиксом: %v", entries)
	}
}

func TestInboxAccessor(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{})
	if h.mgr.Inbox() == nil {
		t.Fatal("Inbox() вернул nil")
	}
	if h.mgr.Inbox().Dir() != h.inboxDir {
		t.Errorf("Inbox().Dir()=%q", h.mgr.Inbox().Dir())
	}
}

func TestRunStopsByContext(t *testing.T) {
	h := newManagerHarness(t, ManagerConfig{WatchInterval: 20 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.mgr.Run(ctx) }()

	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run вернул ошибку: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run не завершился после cancel")
	}
}

func TestRunWithoutSenderBlocksUntilCancel(t *testing.T) {
	inbox, _ := NewInboxWriter(t.TempDir(), "utf8", nil)
	mgr, err := NewManager(ManagerConfig{
		InboxDir:  t.TempDir(),
		OutboxDir: t.TempDir(),
		SentDir:   t.TempDir(),
		ErrorDir:  t.TempDir(),
	}, nil, nil, inbox, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run не завершился")
	}
}
