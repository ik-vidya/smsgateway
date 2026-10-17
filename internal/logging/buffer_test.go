package logging

import (
	"log/slog"
	"sync"
	"testing"
	"time"
)

func entry(level slog.Level, msg string) Entry {
	return Entry{
		Time:  time.Now(),
		Level: level,
		Msg:   msg,
	}
}

func TestBufferSnapshotEmpty(t *testing.T) {
	b := NewBuffer(10)
	if got := b.Snapshot(); got != nil {
		t.Errorf("ожидалось nil, получено %v", got)
	}
}

func TestBufferSnapshotNotFull(t *testing.T) {
	b := NewBuffer(10)
	b.Add(entry(slog.LevelInfo, "a"))
	b.Add(entry(slog.LevelInfo, "b"))

	got := b.Snapshot()
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].Msg != "a" || got[1].Msg != "b" {
		t.Errorf("%v", got)
	}
}

func TestBufferSnapshotWrapped(t *testing.T) {
	b := NewBuffer(3)
	for i := 1; i <= 5; i++ {
		b.Add(entry(slog.LevelInfo, string(rune('a'+i-1))))
	}
	// Осталось: c, d, e (в порядке c → e).
	got := b.Snapshot()
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	want := []string{"c", "d", "e"}
	for i, e := range got {
		if e.Msg != want[i] {
			t.Errorf("[%d] %q, ожидалось %q", i, e.Msg, want[i])
		}
	}
}

func TestBufferSubscribeSnapshot(t *testing.T) {
	b := NewBuffer(10)
	b.Add(entry(slog.LevelInfo, "old"))

	id, snap, ch := b.Subscribe()
	defer b.Unsubscribe(id)

	if len(snap) != 1 || snap[0].Msg != "old" {
		t.Errorf("snap=%v", snap)
	}

	b.Add(entry(slog.LevelInfo, "new"))
	select {
	case e := <-ch:
		if e.Msg != "new" {
			t.Errorf("e.Msg=%q", e.Msg)
		}
	case <-time.After(time.Second):
		t.Fatal("не пришло новое сообщение")
	}
}

func TestBufferMultipleSubscribers(t *testing.T) {
	b := NewBuffer(10)
	id1, _, ch1 := b.Subscribe()
	id2, _, ch2 := b.Subscribe()
	defer b.Unsubscribe(id1)
	defer b.Unsubscribe(id2)

	b.Add(entry(slog.LevelInfo, "x"))

	for i, ch := range []<-chan Entry{ch1, ch2} {
		select {
		case e := <-ch:
			if e.Msg != "x" {
				t.Errorf("[%d] msg=%q", i, e.Msg)
			}
		case <-time.After(time.Second):
			t.Fatalf("[%d] не пришло", i)
		}
	}
}

func TestBufferUnsubscribe(t *testing.T) {
	b := NewBuffer(10)
	id, _, ch := b.Subscribe()

	if err := b.Unsubscribe(id); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	// Канал закрыт.
	_, ok := <-ch
	if ok {
		t.Error("канал не закрыт")
	}
	// Повторный Unsubscribe — ошибка.
	if err := b.Unsubscribe(id); err != ErrNoSubscription {
		t.Errorf("err=%v, ожидалось ErrNoSubscription", err)
	}
}

func TestBufferSlowSubscriberDrops(t *testing.T) {
	b := NewBuffer(10)
	id, _, _ := b.Subscribe()
	defer b.Unsubscribe(id)

	// Забиваем канал на 256 записей + сверху.
	for i := 0; i < 300; i++ {
		b.Add(entry(slog.LevelInfo, "flood"))
	}

	if d := b.Dropped(id); d == 0 {
		t.Error("Dropped=0, ожидалось >0 при переполнении")
	}
	// После Dropped счётчик сброшен.
	if d := b.Dropped(id); d != 0 {
		t.Errorf("второй Dropped=%d, ожидалось 0", d)
	}
}

func TestBufferConcurrentAddSubscribers(t *testing.T) {
	b := NewBuffer(100)

	var wg sync.WaitGroup
	// Писатели.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				b.Add(entry(slog.LevelInfo, "x"))
			}
		}()
	}
	// Подписчики.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				id, _, _ := b.Subscribe()
				_ = id
				time.Sleep(time.Millisecond)
				b.Unsubscribe(id)
			}
		}()
	}
	wg.Wait()
}

func TestEntryMarshalJSON(t *testing.T) {
	e := Entry{
		Time:  time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
		Level: slog.LevelWarn,
		Msg:   "hi",
		Attrs: map[string]string{"k": "v"},
	}
	raw, err := e.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	want := `{"time":"2026-10-17T12:00:00Z","level":"WARN","msg":"hi","attrs":{"k":"v"}}`
	if string(raw) != want {
		t.Errorf("got %s, want %s", raw, want)
	}
}
