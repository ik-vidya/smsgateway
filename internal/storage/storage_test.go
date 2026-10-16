package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrateIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("повторная Migrate: %v", err)
	}
}

func TestCreateAndGetMessage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m := &Message{
		Direction: DirectionOut,
		Status:    StatusQueued,
		Peer:      "+37121234567",
		Text:      "Привет",
	}
	id, err := s.CreateMessage(ctx, m)
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if id == 0 {
		t.Fatal("id не установлен")
	}

	got, err := s.GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.Peer != "+37121234567" || got.Text != "Привет" {
		t.Errorf("не совпадает: %+v", got)
	}
	if got.Direction != DirectionOut || got.Status != StatusQueued {
		t.Errorf("direction/status: %s/%s", got.Direction, got.Status)
	}
}

func TestMessageNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetMessage(context.Background(), 99999)
	if !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("ожидался ErrMessageNotFound, получено %v", err)
	}
}

func TestStatusTransitions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, _ := s.CreateMessage(ctx, &Message{
		Direction: DirectionOut, Status: StatusQueued,
		Peer: "+37121234567", Text: "test",
	})

	if err := s.MarkSending(ctx, id); err != nil {
		t.Fatalf("MarkSending: %v", err)
	}
	if err := s.MarkSent(ctx, id, 42); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	if err := s.MarkDelivered(ctx, id, time.Now().UTC()); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}

	// Из терминального delivered больше переходов нет.
	if err := s.MarkFailed(ctx, id, "oops"); err == nil {
		t.Fatal("ожидалась ошибка перехода из delivered")
	}

	got, _ := s.GetMessage(ctx, id)
	if got.Status != StatusDelivered {
		t.Errorf("status=%s, ожидалось delivered", got.Status)
	}
	if !got.DeliveredAt.Valid {
		t.Error("delivered_at не установлен")
	}
	if !got.Reference.Valid || got.Reference.Int64 != 42 {
		t.Errorf("reference=%v, ожидалось 42", got.Reference)
	}
}

func TestListMessagesFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, _ = s.CreateMessage(ctx, &Message{
			Direction: DirectionIn, Status: StatusReceived,
			Peer: "+3710000000" + string(rune('0'+i)),
			Text: "msg",
		})
	}

	list, total, err := s.ListMessages(ctx, Filter{
		Direction: DirectionIn, Limit: 3,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if total != 5 {
		t.Errorf("total=%d, ожидалось 5", total)
	}
	if len(list) != 3 {
		t.Errorf("вернулось %d, ожидалось 3", len(list))
	}
}

func TestDeliveryReport(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, _ := s.CreateMessage(ctx, &Message{
		Direction: DirectionOut, Status: StatusQueued,
		Peer: "+37121234567", Text: "test",
	})
	_ = s.MarkSending(ctx, id)
	_ = s.MarkSent(ctx, id, 100)

	dr := &DeliveryReport{
		MessageID:  sql.NullInt64{Int64: id, Valid: true},
		Reference:  100,
		TPST:       0x00,
		StatusText: "доставлено",
		Peer:       "+37121234567",
		RawPDU:     "0691...",
	}
	if _, err := s.CreateDeliveryReport(ctx, dr); err != nil {
		t.Fatalf("CreateDeliveryReport: %v", err)
	}

	got, _ := s.GetMessage(ctx, id)
	if got.Status != StatusDelivered {
		t.Errorf("status=%s, ожидалось delivered", got.Status)
	}

	drs, err := s.ListDeliveryReportsByMessage(ctx, id)
	if err != nil {
		t.Fatalf("ListDeliveryReportsByMessage: %v", err)
	}
	if len(drs) != 1 || drs[0].Reference != 100 {
		t.Errorf("DR не найден: %+v", drs)
	}
}

func TestPendingReference(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	msgID, _ := s.CreateMessage(ctx, &Message{
		Direction: DirectionOut, Status: StatusSending,
		Peer: "+37121234567", Text: "test",
	})

	if err := s.ReserveReference(ctx, 42, msgID, time.Hour); err != nil {
		t.Fatalf("ReserveReference: %v", err)
	}
	if err := s.ReserveReference(ctx, 42, msgID, time.Hour); !errors.Is(err, ErrReferenceBusy) {
		t.Fatalf("ожидалось ErrReferenceBusy, получено %v", err)
	}

	found, err := s.FindPendingReference(ctx, 42)
	if err != nil || found != msgID {
		t.Fatalf("FindPendingReference: %d, %v", found, err)
	}

	if err := s.ReleaseReference(ctx, 42); err != nil {
		t.Fatalf("ReleaseReference: %v", err)
	}
	if _, err := s.FindPendingReference(ctx, 42); !errors.Is(err, ErrReferenceBusy) {
		t.Fatal("reference не освобождён")
	}
}

func TestPendingPartsAssembly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	complete, _, err := s.SavePendingPart(ctx, 5, "+37121234567", 3, 1, "Hello ")
	if err != nil || complete {
		t.Fatalf("part 1: complete=%v, err=%v", complete, err)
	}
	complete, _, _ = s.SavePendingPart(ctx, 5, "+37121234567", 3, 3, "world")
	if complete {
		t.Fatal("собрано раньше времени")
	}
	complete, full, err := s.SavePendingPart(ctx, 5, "+37121234567", 3, 2, "cruel ")
	if err != nil {
		t.Fatalf("part 2: %v", err)
	}
	if !complete {
		t.Fatal("должно быть собрано")
	}
	if full != "Hello cruel world" {
		t.Errorf("итог: %q", full)
	}
}

func TestRetention(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	old := nowUTC().Add(-48 * time.Hour)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO messages (direction, status, peer, created_at, updated_at)
		VALUES ('in', 'received', '+371', ?, ?)
	`, old, old)
	if err != nil {
		t.Fatalf("insert old: %v", err)
	}

	_, _ = s.CreateMessage(ctx, &Message{
		Direction: DirectionIn, Status: StatusReceived,
		Peer: "+372", Text: "new",
	})

	s.runRetentionOnce(ctx, RetentionPolicy{
		Messages: 24 * time.Hour,
	}, nil)

	_, total, _ := s.ListMessages(ctx, Filter{})
	if total != 1 {
		t.Errorf("после retention осталось %d, ожидалось 1", total)
	}
}

// ==================== gammu_outbox ====================

func TestOutboxEntryCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	msgID, err := s.CreateMessage(ctx, &Message{
		Direction: DirectionOut, Status: StatusQueued,
		Source: SourceGammuOutbox,
		Peer:   "+37121234567", Text: "hi",
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	e := &OutboxEntry{
		Path:      "/tmp/outbox/OUTA_+37121234567_00.txt",
		MessageID: msgID,
		Priority:  "A",
	}
	if err := s.CreateOutboxEntry(ctx, e); err != nil {
		t.Fatalf("CreateOutboxEntry: %v", err)
	}

	got, err := s.GetOutboxEntry(ctx, e.Path)
	if err != nil {
		t.Fatalf("GetOutboxEntry: %v", err)
	}
	if got.MessageID != msgID || got.Priority != "A" || got.Retries != 0 {
		t.Errorf("got %+v", got)
	}

	// Понижение приоритета и переименование.
	newPath := "/tmp/outbox/OUTB_+37121234567_00.txt"
	if err := s.UpdateOutboxAfterFailure(ctx, e.Path, newPath,
		1, "B", "timeout"); err != nil {
		t.Fatalf("UpdateOutboxAfterFailure: %v", err)
	}

	got, err = s.GetOutboxEntry(ctx, newPath)
	if err != nil {
		t.Fatalf("GetOutboxEntry (new path): %v", err)
	}
	if got.Retries != 1 || got.Priority != "B" || got.LastError != "timeout" {
		t.Errorf("после обновления: %+v", got)
	}

	// Старый путь больше не существует.
	if _, err := s.GetOutboxEntry(ctx, e.Path); !errors.Is(err, ErrOutboxEntryNotFound) {
		t.Errorf("старый путь: ожидалось ErrOutboxEntryNotFound, получено %v", err)
	}

	// Удаление.
	if err := s.DeleteOutboxEntry(ctx, newPath); err != nil {
		t.Fatalf("DeleteOutboxEntry: %v", err)
	}
	if _, err := s.GetOutboxEntry(ctx, newPath); !errors.Is(err, ErrOutboxEntryNotFound) {
		t.Errorf("после удаления: ожидалось ErrOutboxEntryNotFound")
	}
}

func TestOutboxListOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mk := func(prio, path string) {
		msgID, _ := s.CreateMessage(ctx, &Message{
			Direction: DirectionOut, Status: StatusQueued,
			Source: SourceGammuOutbox, Peer: "+371", Text: "x",
		})
		if err := s.CreateOutboxEntry(ctx, &OutboxEntry{
			Path: path, MessageID: msgID, Priority: prio,
		}); err != nil {
			t.Fatalf("CreateOutboxEntry(%s): %v", path, err)
		}
	}
	mk("C", "/o/OUTС1.txt")
	mk("A", "/o/OUTA2.txt")
	mk("A", "/o/OUTA1.txt")
	mk("B", "/o/OUTB1.txt")

	list, err := s.ListOutboxEntries(ctx)
	if err != nil {
		t.Fatalf("ListOutboxEntries: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("len=%d, ожидалось 4", len(list))
	}
	want := []string{
		"/o/OUTA1.txt", "/o/OUTA2.txt",
		"/o/OUTB1.txt", "/o/OUTС1.txt",
	}
	for i, e := range list {
		if e.Path != want[i] {
			t.Errorf("[%d] path=%q, ожидалось %q", i, e.Path, want[i])
		}
	}
}

func TestOrphanOutboxMessages(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Сирота: source='gammu-outbox', status='queued', нет записи в gammu_outbox.
	orphanID, _ := s.CreateMessage(ctx, &Message{
		Direction: DirectionOut, Status: StatusQueued,
		Source: SourceGammuOutbox, Peer: "+371111", Text: "orphan",
	})

	// Нормальное: есть запись в gammu_outbox.
	okID, _ := s.CreateMessage(ctx, &Message{
		Direction: DirectionOut, Status: StatusQueued,
		Source: SourceGammuOutbox, Peer: "+372222", Text: "ok",
	})
	if err := s.CreateOutboxEntry(ctx, &OutboxEntry{
		Path: "/o/OUTA_372222_00.txt", MessageID: okID, Priority: "A",
	}); err != nil {
		t.Fatalf("CreateOutboxEntry: %v", err)
	}

	// Не из gammu — не считается сиротой.
	_, _ = s.CreateMessage(ctx, &Message{
		Direction: DirectionOut, Status: StatusQueued,
		Source: SourceAPI, Peer: "+373333", Text: "api",
	})

	orphans, err := s.OrphanOutboxMessages(ctx)
	if err != nil {
		t.Fatalf("OrphanOutboxMessages: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("len=%d, ожидалось 1", len(orphans))
	}
	if orphans[0].ID != orphanID {
		t.Errorf("id=%d, ожидалось %d", orphans[0].ID, orphanID)
	}
}

func TestMessageSourceRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, err := s.CreateMessage(ctx, &Message{
		Direction: DirectionOut, Status: StatusQueued,
		Source: SourceGammuOutbox,
		Peer:   "+37121234567", Text: "hello",
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	got, err := s.GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.Source != SourceGammuOutbox {
		t.Errorf("Source=%q, ожидалось %q", got.Source, SourceGammuOutbox)
	}

	// Фильтр по source.
	list, total, err := s.ListMessages(ctx, Filter{Source: SourceGammuOutbox})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Errorf("total=%d len=%d, ожидалось 1/1", total, len(list))
	}

	// Не тот source — пусто.
	_, total, _ = s.ListMessages(ctx, Filter{Source: SourceAPI})
	if total != 0 {
		t.Errorf("total=%d, ожидалось 0", total)
	}
}

func TestCreateMessageHonorsCreatedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	custom := time.Date(2026, 10, 15, 12, 0, 34, 0, time.UTC)
	id, err := s.CreateMessage(ctx, &Message{
		Direction: DirectionIn, Status: StatusReceived,
		Peer: "+37121234567", Text: "gammu import",
		CreatedAt: custom,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	got, _ := s.GetMessage(ctx, id)
	if !got.CreatedAt.Equal(custom) {
		t.Errorf("CreatedAt=%v, ожидалось %v", got.CreatedAt, custom)
	}
}
