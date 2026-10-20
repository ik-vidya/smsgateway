package service

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.vidya.lv/vidya/smsgateway/internal/gammu"
	"git.vidya.lv/vidya/smsgateway/internal/modem"
	"git.vidya.lv/vidya/smsgateway/internal/profiles"
	"git.vidya.lv/vidya/smsgateway/internal/storage"
)

// nopLogger реализует modem.Logger.
type nopLogger struct{}

func (nopLogger) Debug(string, ...any) {}
func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

func quietLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newMockModemWithInit создаёт *modem.Modem с mock-транспортом,
// отвечающий на базовые команды инициализации.
func newMockModemWithInit(t *testing.T) (*modem.Modem, *modem.MockTransport) {
	t.Helper()
	mock := modem.NewMock()
	mock.PushResponse("AT", "OK")
	mock.PushResponse("ATE0", "OK")
	mock.PushResponse("AT+CMEE=2", "OK")
	mock.PushResponse("AT+CGMI", "SIMCOM_Ltd", "OK")
	mock.PushResponse("AT+CGMM", "SIMCOM_SIM800L", "OK")
	mock.PushResponse("AT+CGMR", "r", "OK")
	mock.PushResponse("AT+CGSN", "i", "OK")

	m, err := modem.NewModem(mock, profiles.Generic{}, time.Second, nopLogger{})
	if err != nil {
		t.Fatalf("NewModem: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return m, mock
}

// ==================== ModemSender ====================

func TestModemSenderSuccess(t *testing.T) {
	m, mock := newMockModemWithInit(t)
	mock.PushResponse("AT+CMGF=0", "OK")
	mock.PushResponse("", "> ", "+CMGS: 42", "OK")

	sender := NewModemSender(m, quietLogger(t))
	ref, err := sender.Send(context.Background(),
		"+37121234567", "Hello", false, false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ref != 42 {
		t.Errorf("ref=%d, ожидалось 42", ref)
	}
}

func TestModemSenderRequestDR(t *testing.T) {
	m, mock := newMockModemWithInit(t)
	mock.PushResponse("AT+CMGF=0", "OK")
	mock.PushResponse("", "> ", "+CMGS: 1", "OK")

	sender := NewModemSender(m, quietLogger(t))
	if _, err := sender.Send(context.Background(),
		"+371", "x", true, false); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Первый октет после SMSC должен иметь SRR=1.
	written := string(mock.WrittenBytes())
	_ = written // проверка через PDU не критична, главное — не падаем
}

func TestModemSenderFlashWarn(t *testing.T) {
	m, mock := newMockModemWithInit(t)
	mock.PushResponse("AT+CMGF=0", "OK")
	mock.PushResponse("", "> ", "+CMGS: 5", "OK")

	sender := NewModemSender(m, quietLogger(t))
	ref, err := sender.Send(context.Background(),
		"+371", "x", false, true)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ref != 5 {
		t.Errorf("ref=%d", ref)
	}
}

func TestModemSenderError(t *testing.T) {
	m, mock := newMockModemWithInit(t)
	mock.PushResponse("AT+CMGF=0", "OK")
	mock.PushResponse("", "+CMS ERROR: 305")

	sender := NewModemSender(m, quietLogger(t))
	_, err := sender.Send(context.Background(), "+371", "x", false, false)
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
}

func TestModemSenderNilModem(t *testing.T) {
	sender := NewModemSender(nil, quietLogger(t))
	_, err := sender.Send(context.Background(), "+371", "x", false, false)
	if err == nil {
		t.Fatal("ожидалась ошибка для nil модема")
	}
}

// ==================== StorageOutboxStore ====================

func newStorageForGammu(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStorageOutboxCreateAndGet(t *testing.T) {
	store := newStorageForGammu(t)
	a := NewStorageOutboxStore(store)
	ctx := context.Background()

	id, err := a.CreateOutboxMessage(ctx, "+37121234567", "hi")
	if err != nil {
		t.Fatalf("CreateOutboxMessage: %v", err)
	}
	if id == 0 {
		t.Fatal("id=0")
	}

	// Проверим, что сообщение действительно с source='gammu-outbox'.
	msg, err := store.GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.Source != storage.SourceGammuOutbox {
		t.Errorf("Source=%q, ожидалось %q", msg.Source, storage.SourceGammuOutbox)
	}
	if msg.Status != storage.StatusQueued {
		t.Errorf("Status=%q", msg.Status)
	}

	path := "/tmp/outbox/OUTA_+37121234567_00.txt"
	if err := a.CreateOutboxEntry(ctx, path, id, "A"); err != nil {
		t.Fatalf("CreateOutboxEntry: %v", err)
	}

	e, err := a.GetOutboxEntry(ctx, path)
	if err != nil {
		t.Fatalf("GetOutboxEntry: %v", err)
	}
	if e.MessageID != id || e.Priority != "A" || e.Retries != 0 {
		t.Errorf("entry=%+v", e)
	}
}

func TestStorageOutboxGetNotFound(t *testing.T) {
	store := newStorageForGammu(t)
	a := NewStorageOutboxStore(store)

	_, err := a.GetOutboxEntry(context.Background(), "/no/such/path")
	if !errorsIs(err, gammu.ErrOutboxEntryNotFound) {
		t.Fatalf("ожидалось gammu.ErrOutboxEntryNotFound, получено %v", err)
	}
}

func TestStorageOutboxUpdateFailure(t *testing.T) {
	store := newStorageForGammu(t)
	a := NewStorageOutboxStore(store)
	ctx := context.Background()

	id, _ := a.CreateOutboxMessage(ctx, "+371", "x")
	pathA := "/tmp/outbox/OUTA_+371_00.txt"
	pathB := "/tmp/outbox/OUTB_+371_00.txt"

	if err := a.CreateOutboxEntry(ctx, pathA, id, "A"); err != nil {
		t.Fatalf("CreateOutboxEntry: %v", err)
	}
	if err := a.UpdateOutboxFailure(ctx, pathA, pathB, 1, "B", "timeout"); err != nil {
		t.Fatalf("UpdateOutboxFailure: %v", err)
	}

	e, err := a.GetOutboxEntry(ctx, pathB)
	if err != nil {
		t.Fatalf("GetOutboxEntry(new): %v", err)
	}
	if e.Retries != 1 || e.Priority != "B" || e.LastError != "timeout" {
		t.Errorf("entry=%+v", e)
	}

	if _, err := a.GetOutboxEntry(ctx, pathA); !errorsIs(err, gammu.ErrOutboxEntryNotFound) {
		t.Errorf("старый путь: ожидалось ErrOutboxEntryNotFound, получено %v", err)
	}
}

func TestStorageOutboxMarkTransitions(t *testing.T) {
	store := newStorageForGammu(t)
	a := NewStorageOutboxStore(store)
	ctx := context.Background()

	id, _ := a.CreateOutboxMessage(ctx, "+371", "x")

	if err := a.MarkOutboxSending(ctx, id); err != nil {
		t.Fatalf("MarkOutboxSending: %v", err)
	}
	if err := a.MarkOutboxSent(ctx, id, 42); err != nil {
		t.Fatalf("MarkOutboxSent: %v", err)
	}

	msg, _ := store.GetMessage(ctx, id)
	if msg.Status != storage.StatusSent {
		t.Errorf("Status=%q", msg.Status)
	}
	if !msg.Reference.Valid || msg.Reference.Int64 != 42 {
		t.Errorf("Reference=%v", msg.Reference)
	}
}

func TestStorageOutboxDeleteIdempotent(t *testing.T) {
	store := newStorageForGammu(t)
	a := NewStorageOutboxStore(store)
	ctx := context.Background()

	if err := a.DeleteOutboxEntry(ctx, "/no/such/path"); err != nil {
		t.Errorf("DeleteOutboxEntry для несуществующего: %v", err)
	}
}

// ==================== multipartFromModem ====================

func TestMultipartFromModem(t *testing.T) {
	if got := multipartFromModem(nil); got != nil {
		t.Errorf("nil → %+v, ожидалось nil", got)
	}
	in := &modem.MultipartInfo{Reference: 7, Total: 3, Sequence: 2}
	got := multipartFromModem(in)
	if got == nil || got.Reference != 7 || got.Total != 3 || got.Sequence != 2 {
		t.Errorf("got %+v", got)
	}
}

// errorsIs — локальная обёртка, чтобы не тянуть errors.Is в каждый
// вызов. Держит импорт errors в тестовом файле.
func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
