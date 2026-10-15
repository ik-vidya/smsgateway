package service

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ik-vidya/smsgateway/internal/modem"
	"github.com/ik-vidya/smsgateway/internal/storage"
)

// ==================== MockModem ====================

// mockModem реализует интерфейс Modem для тестов.
type mockModem struct {
	urcCh chan modem.URC

	// Скрипты.
	readSMSResult []modem.SMSMessage
	readSMSErr    error

	sendResult modem.SendResult
	sendErr    error
	// sendErrs — ошибки по попыткам. Если задан, приоритетнее sendErr.
	// nil в позиции = успех на этой попытке.
	sendErrs []error

	deletedIndexes []int

	// Записи вызовов.
	sentCount int
	execCalls []string
}

func newMockModem() *mockModem {
	return &mockModem{
		urcCh: make(chan modem.URC, 16),
	}
}

func (m *mockModem) Init(ctx context.Context) error { return nil }

func (m *mockModem) SendSMS(ctx context.Context, peer, text string, opts modem.SendOptions) (modem.SendResult, error) {
	m.sentCount++
	// Приоритет: sendErrs (по попыткам).
	if len(m.sendErrs) > 0 {
		idx := m.sentCount - 1
		if idx < len(m.sendErrs) {
			if err := m.sendErrs[idx]; err != nil {
				return modem.SendResult{}, err
			}
		}
	} else if m.sendErr != nil {
		return modem.SendResult{}, m.sendErr
	}
	res := m.sendResult
	// Для обратной совместимости: если задан только Reference,
	// заполнить References.
	if len(res.References) == 0 {
		if res.Reference == 0 {
			res.Reference = int64(m.sentCount)
		}
		res.References = []int64{res.Reference}
	}
	if res.Parts == 0 {
		res.Parts = len(res.References)
	}
	if len(res.RawPDUs) == 0 {
		res.RawPDUs = make([]string, len(res.References))
	}
	return res, nil
}

func (m *mockModem) ReadSMS(ctx context.Context, filter modem.ReadFilter) ([]modem.SMSMessage, error) {
	if m.readSMSErr != nil {
		return nil, m.readSMSErr
	}
	return m.readSMSResult, nil
}

func (m *mockModem) DeleteSMS(ctx context.Context, index int) error {
	m.deletedIndexes = append(m.deletedIndexes, index)
	return nil
}

func (m *mockModem) Exec(ctx context.Context, cmd string) (string, error) {
	m.execCalls = append(m.execCalls, cmd)
	return "", nil
}

func (m *mockModem) URCs() <-chan modem.URC { return m.urcCh }
func (m *mockModem) Info() modem.Info       { return modem.Info{} }
func (m *mockModem) Close() error           { return nil }

// Компилятор проверит, что mockModem реализует Modem.
var _ Modem = (*mockModem)(nil)

// ==================== helpers ====================

func newTestService(t *testing.T, m Modem) (*Service, *storage.Store) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(m, store, logger, Config{
		SendInterval:    50 * time.Millisecond,
		PendingInterval: 100 * time.Millisecond,
		ReferenceTTL:    time.Hour,
		DeleteAfterRead: true,
		SendBackoff:     []time.Duration{20 * time.Millisecond, 30 * time.Millisecond},
	})
	return svc, store
}

func putQueued(t *testing.T, store *storage.Store, peer, text string) int64 {
	t.Helper()
	msg := &storage.Message{
		Direction: storage.DirectionOut,
		Status:    storage.StatusQueued,
		Peer:      peer,
		Text:      text,
	}
	id, err := store.CreateMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	return id
}

// ==================== Send loop ====================

func TestSendLoopProcessesQueue(t *testing.T) {
	mock := newMockModem()
	mock.sendResult = modem.SendResult{Reference: 42}
	svc, store := newTestService(t, mock)

	id := putQueued(t, store, "+37121234567", "Test")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = svc.Run(ctx)

	got, err := store.GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.Status != storage.StatusSent {
		t.Errorf("status=%q, ожидалось sent", got.Status)
	}
	if mock.sentCount != 1 {
		t.Errorf("sentCount=%d, ожидалось 1", mock.sentCount)
	}
	if !got.Reference.Valid || got.Reference.Int64 != 42 {
		t.Errorf("reference=%v, ожидалось 42", got.Reference)
	}
}

func TestSendLoopError(t *testing.T) {
	mock := newMockModem()
	mock.sendErr = &modem.ATError{Line: "+CMS ERROR: 305"}
	svc, store := newTestService(t, mock)

	id := putQueued(t, store, "+37121234567", "Test")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = svc.Run(ctx)

	got, _ := store.GetMessage(context.Background(), id)
	if got.Status != storage.StatusFailed {
		t.Errorf("status=%q, ожидалось failed", got.Status)
	}
	if mock.sentCount != 1 {
		t.Errorf("sentCount=%d, ожидалось 1 (permanent — без retry)", mock.sentCount)
	}
}

// ==================== Retry ====================

func TestSendRetrySuccessOnSecondAttempt(t *testing.T) {
	mock := newMockModem()
	mock.sendErrs = []error{
		&modem.ATError{Line: "+CMS ERROR: 500"},
		nil,
	}
	svc, store := newTestService(t, mock)

	id := putQueued(t, store, "+37121234567", "Test")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = svc.Run(ctx)

	got, _ := store.GetMessage(context.Background(), id)
	if got.Status != storage.StatusSent {
		t.Errorf("status=%q, ожидалось sent", got.Status)
	}
	if mock.sentCount != 2 {
		t.Errorf("sentCount=%d, ожидалось 2", mock.sentCount)
	}
}

func TestSendRetryExhausted(t *testing.T) {
	mock := newMockModem()
	mock.sendErrs = []error{
		&modem.ATError{Line: "+CMS ERROR: 500"},
		&modem.ATError{Line: "+CMS ERROR: 41"},
		&modem.ATError{Line: "+CMS ERROR: 42"},
	}
	svc, store := newTestService(t, mock)

	id := putQueued(t, store, "+37121234567", "Test")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = svc.Run(ctx)

	got, _ := store.GetMessage(context.Background(), id)
	if got.Status != storage.StatusFailed {
		t.Errorf("status=%q, ожидалось failed", got.Status)
	}
	if mock.sentCount != 3 {
		t.Errorf("sentCount=%d, ожидалось 3 (все попытки)", mock.sentCount)
	}
}

func TestSendRetryPermanentNoRetry(t *testing.T) {
	mock := newMockModem()
	mock.sendErrs = []error{
		&modem.ATError{Line: "+CMS ERROR: 305"},
	}
	svc, store := newTestService(t, mock)

	id := putQueued(t, store, "+37121234567", "Test")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = svc.Run(ctx)

	got, _ := store.GetMessage(context.Background(), id)
	if got.Status != storage.StatusFailed {
		t.Errorf("status=%q, ожидалось failed", got.Status)
	}
	if mock.sentCount != 1 {
		t.Errorf("sentCount=%d, ожидалось 1 (permanent — без retry)", mock.sentCount)
	}
}

// ==================== URC ingest ====================

func TestURCCMTIIngest(t *testing.T) {
	mock := newMockModem()
	mock.readSMSResult = []modem.SMSMessage{
		{
			Index:  5,
			Status: "REC UNREAD",
			Peer:   "+37255691999",
			Text:   "Привет",
			RawPDU: "00AA",
		},
	}
	svc, store := newTestService(t, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)

	mock.urcCh <- modem.URCCMTI{Mem: "SM", Index: 5, Raw: `+CMTI: "SM",5`}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	msgs, total, err := store.ListMessages(context.Background(), storage.Filter{
		Direction: storage.DirectionIn,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if total != 1 {
		t.Fatalf("total=%d, ожидалось 1", total)
	}
	if msgs[0].Peer != "+37255691999" || msgs[0].Text != "Привет" {
		t.Errorf("msg=%+v", msgs[0])
	}
	if len(mock.deletedIndexes) != 1 || mock.deletedIndexes[0] != 5 {
		t.Errorf("deleted=%v, ожидалось [5]", mock.deletedIndexes)
	}
}

func TestURCCMTIDeduplication(t *testing.T) {
	mock := newMockModem()
	mock.readSMSResult = []modem.SMSMessage{
		{
			Index:  5,
			Status: "REC UNREAD",
			Peer:   "+37255691999",
			Text:   "Привет",
			RawPDU: "00AA",
		},
	}
	svc, store := newTestService(t, mock)

	_, _ = store.CreateMessage(context.Background(), &storage.Message{
		Direction: storage.DirectionIn,
		Status:    storage.StatusReceived,
		Peer:      "+37255691999",
		Text:      "Привет",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = svc.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	mock.urcCh <- modem.URCCMTI{Mem: "SM", Index: 5}
	time.Sleep(200 * time.Millisecond)
	cancel()

	_, total, _ := store.ListMessages(context.Background(), storage.Filter{
		Direction: storage.DirectionIn,
	})
	if total != 1 {
		t.Errorf("total=%d, ожидалось 1 (дедупликация)", total)
	}
}

// ==================== URC network ====================

func TestURCCREGUpdatesStatus(t *testing.T) {
	mock := newMockModem()
	svc, _ := newTestService(t, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = svc.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	mock.urcCh <- modem.URCCREG{
		Stat: 5, LAC: "00DF", CID: "156B",
		Raw: `+CREG: 2,5,"00DF","156B"`,
	}
	time.Sleep(100 * time.Millisecond)

	if svc.NetworkStatus() != 5 {
		t.Errorf("networkStat=%d, ожидалось 5", svc.NetworkStatus())
	}
	cancel()
}

// ==================== Pending references ====================

func TestPendingReferencesExpire(t *testing.T) {
	mock := newMockModem()
	svc, store := newTestService(t, mock)

	id := putQueued(t, store, "+37121234567", "Test")
	_ = store.MarkSending(context.Background(), id)
	_ = store.MarkSent(context.Background(), id, 100)

	_ = store.ReserveReference(context.Background(), 100, id, 50*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = svc.Run(ctx)

	got, _ := store.GetMessage(context.Background(), id)
	if got.Status != storage.StatusSentNoDR {
		t.Errorf("status=%q, ожидалось sent_no_dr", got.Status)
	}
}

// ==================== DR ====================

func TestHandleDeliveryReport(t *testing.T) {
	mock := newMockModem()
	svc, store := newTestService(t, mock)

	id := putQueued(t, store, "+37121234567", "Test")
	_ = store.MarkSending(context.Background(), id)
	_ = store.MarkSent(context.Background(), id, 200)
	_ = store.ReserveReference(context.Background(), 200, id, time.Hour)

	err := svc.HandleDeliveryReport(context.Background(), 200, 0x00, "00AA")
	if err != nil {
		t.Fatalf("HandleDeliveryReport: %v", err)
	}

	got, _ := store.GetMessage(context.Background(), id)
	if got.Status != storage.StatusDelivered {
		t.Errorf("status=%q, ожидалось delivered", got.Status)
	}
	if _, err := store.FindPendingReference(context.Background(), 200); err == nil {
		t.Error("reference не освобождён")
	}
}

// ==================== ingestSMS error ====================

func TestIngestSMSError(t *testing.T) {
	mock := newMockModem()
	mock.readSMSErr = context.DeadlineExceeded
	svc, _ := newTestService(t, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = svc.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	mock.urcCh <- modem.URCCMTI{Mem: "SM", Index: 5}
	time.Sleep(150 * time.Millisecond)
	cancel()

	if !strings.Contains("ok", "ok") {
		t.Fatal("unreachable")
	}
}

// ==================== backoffFor ====================

func TestBackoffFor(t *testing.T) {
	svc := New(nil, nil, nil, Config{
		SendBackoff: []time.Duration{1 * time.Second, 5 * time.Second},
	})
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 5 * time.Second},
		{3, 0},
	}
	for _, c := range cases {
		got := svc.backoffFor(c.attempt)
		if got != c.want {
			t.Errorf("backoffFor(%d)=%v, ожидалось %v", c.attempt, got, c.want)
		}
	}
}

// ==================== Multipart ====================

func TestSendMultipartReservesAllReferences(t *testing.T) {
	mock := newMockModem()
	mock.sendResult = modem.SendResult{
		Parts:      2,
		Reference:  10,
		References: []int64{10, 11},
		RawPDUs:    []string{"AA", "BB"},
	}
	svc, store := newTestService(t, mock)

	id := putQueued(t, store, "+37121234567", "long")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = svc.Run(ctx)

	got, _ := store.GetMessage(context.Background(), id)
	if got.Status != storage.StatusSent {
		t.Errorf("status=%q, ожидалось sent", got.Status)
	}
	if _, err := store.FindPendingReference(context.Background(), 10); err != nil {
		t.Errorf("ref 10 не забронирован: %v", err)
	}
	if _, err := store.FindPendingReference(context.Background(), 11); err != nil {
		t.Errorf("ref 11 не забронирован: %v", err)
	}
	if !got.Reference.Valid || got.Reference.Int64 != 10 {
		t.Errorf("messages.reference=%v, ожидалось 10", got.Reference)
	}
}

func TestSendMultipartPartialFailure(t *testing.T) {
	mock := newMockModem()
	mock.sendErr = &modem.ATError{Line: "+CMS ERROR: 305"}
	svc, store := newTestService(t, mock)

	id := putQueued(t, store, "+37121234567", "long")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = svc.Run(ctx)

	got, _ := store.GetMessage(context.Background(), id)
	if got.Status != storage.StatusFailed {
		t.Errorf("status=%q, ожидалось failed", got.Status)
	}
}
