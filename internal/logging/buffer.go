// Package logging, файл buffer.go — in-memory ring buffer для логов
// и pub/sub для SSE-клиентов.
//
// Буфер ограничен по размеру: при переполнении старые записи
// вытесняются новыми. Он не заменяет stdout-логгер, а дополняет
// его — используется для страницы /logs в Web UI.
package logging

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"log/slog"
)

// Entry — одна запись лога в буфере.
//
// Attrs — строковые представления атрибутов. Для передачи в JSON
// и для отображения. Не пытаемся сохранить исходные типы: логи
// читают люди, а не машины.
type Entry struct {
	Time  time.Time
	Level slog.Level
	Msg   string
	Attrs map[string]string
}

// MarshalJSON сериализует Entry в формате, ожидаемом SSE-клиентом.
// Уровень — строка ("DEBUG", "INFO", ...), не число.
func (e Entry) MarshalJSON() ([]byte, error) {
	type alias struct {
		Time  time.Time         `json:"time"`
		Level string            `json:"level"`
		Msg   string            `json:"msg"`
		Attrs map[string]string `json:"attrs,omitempty"`
	}
	return json.Marshal(alias{
		Time:  e.Time,
		Level: e.Level.String(),
		Msg:   e.Msg,
		Attrs: e.Attrs,
	})
}

// Buffer — кольцевой буфер последних логов с pub/sub.
//
// Потокобезопасен. Один Buffer обслуживает всех SSE-клиентов:
// Snapshot и Subscribe работают согласованно — подписчик не
// пропустит записи между снимком и подпиской.
type Buffer struct {
	mu      sync.Mutex
	entries []Entry
	pos     int
	full    bool

	subs   map[int64]*subscriber
	nextID int64
}

type subscriber struct {
	ch      chan Entry
	dropped atomic.Int64
}

// NewBuffer создаёт буфер на size записей.
//
// size <= 0 — берётся 1000.
func NewBuffer(size int) *Buffer {
	if size <= 0 {
		size = 1000
	}
	return &Buffer{
		entries: make([]Entry, size),
		subs:    make(map[int64]*subscriber),
	}
}

// Add добавляет запись и рассылает её подписчикам.
//
// Медленные подписчики не блокируют вызывающего: если их канал
// переполнен, запись дропается, а у подписчика инкрементируется
// счётчик dropped (SSE-клиент получит об этом предупреждение).
func (b *Buffer) Add(e Entry) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.entries[b.pos] = e
	b.pos++
	if b.pos >= len(b.entries) {
		b.pos = 0
		b.full = true
	}

	// Отправка в каналы подписчиков — под тем же мьютексом, что
	// и Unsubscribe. Иначе Add и Unsubscribe гоняются за канал:
	// Unsubscribe делает close(s.ch), а Add — s.ch <- e. Это
	// не только data race (детектируется -race), но и рано или
	// поздно panic: send on closed channel (select не защищает
	// от закрытого канала — default не спасает).
	//
	// Отправка неблокирующая: буфер канала 256, при переполнении
	// срабатывает default и инкрементится dropped. Так что
	// мьютекс держится микросекунды.
	for _, s := range b.subs {
		select {
		case s.ch <- e:
		default:
			s.dropped.Add(1)
		}
	}
}

// Snapshot возвращает все записи в хронологическом порядке
// (старые → новые). Возвращённый срез — копия; дальнейшие Add
// на него не влияют.
func (b *Buffer) Snapshot() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked()
}

func (b *Buffer) snapshotLocked() []Entry {
	n := b.pos
	if b.full {
		n = len(b.entries)
	}
	if n == 0 {
		return nil
	}
	out := make([]Entry, 0, n)
	if b.full {
		// Сначала хвост (старые), потом начало (новые).
		out = append(out, b.entries[b.pos:]...)
		out = append(out, b.entries[:b.pos]...)
	} else {
		out = append(out, b.entries[:b.pos]...)
	}
	return out
}

// Subscribe подписывается на новые записи и сразу получает снимок
// текущего состояния буфера.
//
// Возвращает идентификатор подписки и канал новых записей. Между
// снимком и каналом нет «дыры»: всё, что попало в буфер после
// снимка, придёт в канал.
//
// Канал закрывается при Unsubscribe.
func (b *Buffer) Subscribe() (int64, []Entry, <-chan Entry) {
	b.mu.Lock()
	defer b.mu.Unlock()

	snap := b.snapshotLocked()
	ch := make(chan Entry, 256)
	id := b.nextID
	b.nextID++
	b.subs[id] = &subscriber{ch: ch}
	return id, snap, ch
}

// Unsubscribe отписывает и закрывает канал.
//
// После вызова запись в канал прекращается. Безопасно вызывать
// повторно — повторный вызов вернёт ErrNoSubscription.
func (b *Buffer) Unsubscribe(id int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, ok := b.subs[id]
	if !ok {
		return ErrNoSubscription
	}
	delete(b.subs, id)
	close(s.ch)
	return nil
}

// Dropped возвращает, сколько записей было пропущено подписчику
// из-за переполнения его канала, и сбрасывает счётчик.
//
// Используется SSE-обработчиком, чтобы уведомить клиента.
func (b *Buffer) Dropped(id int64) int64 {
	b.mu.Lock()
	s, ok := b.subs[id]
	b.mu.Unlock()
	if !ok {
		return 0
	}
	return s.dropped.Swap(0)
}

// Filter — фильтр записей лога.
type Filter struct {
	MinLevel slog.Level // минимальный уровень (>=)
	Since    time.Time  // >= (нулевое — без ограничения)
	Until    time.Time  // <  (нулевое — без ограничения)
	Contains string     // подстрока в Msg (регистронезависимо)
}

// SnapshotFiltered возвращает отфильтрованную копию буфера.
func (b *Buffer) SnapshotFiltered(f Filter) []Entry {
	b.mu.Lock()
	all := b.snapshotLocked()
	b.mu.Unlock()

	needle := strings.ToLower(f.Contains)
	out := make([]Entry, 0, len(all))
	for _, e := range all {
		if e.Level < f.MinLevel {
			continue
		}
		if !f.Since.IsZero() && e.Time.Before(f.Since) {
			continue
		}
		if !f.Until.IsZero() && !e.Time.Before(f.Until) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(e.Msg), needle) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Size возвращает ёмкость ring buffer.
func (b *Buffer) Size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

// Count возвращает число записей (0..Size).
func (b *Buffer) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.full {
		return len(b.entries)
	}
	return b.pos
}

// OldestNewest возвращает время самой старой и самой новой записи.
func (b *Buffer) OldestNewest() (oldest, newest time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	all := b.snapshotLocked()
	if len(all) == 0 {
		return time.Time{}, time.Time{}
	}
	return all[0].Time, all[len(all)-1].Time
}

// Text возвращает запись в формате slog text-handler.
func (e Entry) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "time=%s level=%s msg=%q",
		e.Time.Format(time.RFC3339Nano), e.Level.String(), e.Msg)
	if len(e.Attrs) > 0 {
		keys := make([]string, 0, len(e.Attrs))
		for k := range e.Attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, " %s=%q", k, e.Attrs[k])
		}
	}
	return b.String()
}
