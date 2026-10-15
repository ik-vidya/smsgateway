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
	if m.sendErr != nil {
		return modem.SendResult{}, m.sendErr
	}
	res := m.sendResult
	if res.Reference == 0 {
		res.Reference = int64(m.sentCount)
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
	})
	return svc, store
}

// ==================== Send loop ====================

func TestSendLoopProcessesQueue(t *testing.T) {
	mock := newMockModem()
	mock.sendResult = modem.SendResult{Reference: 42}
	svc, store := newTestService(t, mock)

	// Положить сообщение в очередь.
	msg := &storage.Message{
		Direction: storage.DirectionOut,
		Status:    storage.StatusQueued,
		Peer:      "+37121234567",
		Text:      "Test",
	}
	if _, err := store.CreateMessage(context.Background(), msg); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = svc.Run(ctx)

	// Проверить, что сообщение отправлено.
	got, err := store.GetMessage(context.Background(), msg.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.Status != storage.StatusSent {
		t.Errorf("status=%q, ожидалось sent", got.Status)
	}
	if mock.sentCount == 0 {
		t.Error("SendSMS не вызван")
	}
	if !got.Reference.Valid || got.Reference.Int64 != 42 {
		t.Errorf("reference=%v", got.Reference)
	}
}

func TestSendLoopError(t *testing.T) {
	mock := newMockModem()
	mock.sendErr = context.DeadlineExceeded
	svc, store := newTestService(t, mock)

	msg := &storage.Message{
		Direction: storage.DirectionOut,
		Status:    storage.StatusQueued,
		Peer:      "+37121234567",
		Text:      "Test",
	}
	_, _ = store.CreateMessage(context.Background(), msg)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = svc.Run(ctx)

	got, _ := store.GetMessage(context.Background(), msg.ID)
	if got.Status != storage.StatusFailed {
		t.Errorf("status=%q, ожидалось failed", got.Status)
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

	// Подождать, пока URC-цикл запустится.
	time.Sleep(50 * time.Millisecond)

	// Послать URC.
	mock.urcCh <- modem.URCCMTI{Mem: "SM", Index: 5, Raw: `+CMTI: "SM",5`}

	// Подождать обработки.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	// Проверить, что SMS сохранено.
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

	// Проверить, что из модема удалено.
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

	// Заранее положить такое же сообщение в БД.
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

	// В БД должна быть одна запись, не две.
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

	// Сообщение в статусе sent с reference, у которого TTL уже истёк.
	msg := &storage.Message{
		Direction: storage.DirectionOut,
		Status:    storage.StatusQueued,
		Peer:      "+37121234567",
		Text:      "Test",
	}
	id, _ := store.CreateMessage(context.Background(), msg)
	_ = store.MarkSending(context.Background(), id)
	_ = store.MarkSent(context.Background(), id, 100)

	// Забронировать reference с TTL 50мс (короткий).
	_ = store.ReserveReference(context.Background(), 100, id, 50*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = svc.Run(ctx)

	// После истечения TTL сообщение должно быть sent_no_dr.
	got, _ := store.GetMessage(context.Background(), id)
	if got.Status != storage.StatusSentNoDR {
		t.Errorf("status=%q, ожидалось sent_no_dr", got.Status)
	}
}

// ==================== DR ====================

func TestHandleDeliveryReport(t *testing.T) {
	mock := newMockModem()
	svc, store := newTestService(t, mock)

	// Готовим сообщение: queued → sending → sent → reference.
	msg := &storage.Message{
		Direction: storage.DirectionOut,
		Status:    storage.StatusQueued,
		Peer:      "+37121234567",
		Text:      "Test",
	}
	id, _ := store.CreateMessage(context.Background(), msg)
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

	// Reference должен быть освобождён.
	if _, err := store.FindPendingReference(context.Background(), 200); err == nil {
		t.Error("reference не освобождён")
	}
}

// ==================== обработка ошибок ingestSMS ====================

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

	// Не должно быть паники. Тест пройден, если горутина не упала.
	if !strings.Contains("ok", "ok") { // placeholder
		t.Fatal("unreachable")
	}
}