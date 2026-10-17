// Package webui, файл ports.go — отслеживание списка
// последовательных портов для страницы /setup.
//
// Поллинг вместо udev/fsnotify: работает без cgo, без внешних
// зависимостей и одинаково на разных ядрах, в контейнерах и на
// Raspberry Pi. Раз в interval читает /dev/serial/by-id (или
// fallback /dev/ttyUSB*, /dev/ttyACM*), при изменении рассылает
// событие подписчикам.
package webui

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// PortEvent — снимок списка портов + новые с прошлого события.
type PortEvent struct {
	Ports []string `json:"ports"`
	New   []string `json:"new,omitempty"`
}

// PortWatcher — следит за списком последовательных портов.
type PortWatcher struct {
	listFn   func() []string
	interval time.Duration

	mu      sync.Mutex
	current []string
	subs    map[int64]chan PortEvent
	nextID  int64
}

// NewPortWatcher создаёт watcher с дефолтным listFn.
//
// interval <= 0 — берётся 1 секунда.
func NewPortWatcher(interval time.Duration) *PortWatcher {
	return NewPortWatcherWithLister(defaultListPorts, interval)
}

// NewPortWatcherWithLister создаёт watcher с инъектируемым
// listFn (используется в тестах).
func NewPortWatcherWithLister(
	listFn func() []string, interval time.Duration) *PortWatcher {

	if interval <= 0 {
		interval = time.Second
	}
	if listFn == nil {
		listFn = defaultListPorts
	}
	return &PortWatcher{
		listFn:   listFn,
		interval: interval,
		subs:     make(map[int64]chan PortEvent),
	}
}

// Run запускает цикл поллинга. Блокируется до закрытия stop.
//
// Первый опрос — сразу при старте, чтобы у подписчиков был
// актуальный снимок без ожидания интервала.
func (w *PortWatcher) Run(stop <-chan struct{}) {
	w.refresh()
	tick := time.NewTicker(w.interval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			w.refresh()
		}
	}
}

// Refresh форсирует немедленный опрос. Полезно для тестов.
func (w *PortWatcher) Refresh() { w.refresh() }

func (w *PortWatcher) refresh() {
	ports := w.listFn()

	w.mu.Lock()
	prev := w.current
	if equalStringSlices(prev, ports) {
		w.mu.Unlock()
		return
	}
	w.current = ports
	subs := make([]chan PortEvent, 0, len(w.subs))
	for _, ch := range w.subs {
		subs = append(subs, ch)
	}
	w.mu.Unlock()

	ev := PortEvent{
		Ports: append([]string(nil), ports...),
		New:   diffStringSlices(prev, ports),
	}
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
			// медленный подписчик — пропустим событие
		}
	}
}

// Subscribe возвращает (id, snapshot, канал). Snapshot — текущий
// список портов, канал получает события при изменениях.
func (w *PortWatcher) Subscribe() (int64, PortEvent, <-chan PortEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	snap := PortEvent{Ports: append([]string(nil), w.current...)}
	ch := make(chan PortEvent, 8)
	id := w.nextID
	w.nextID++
	w.subs[id] = ch
	return id, snap, ch
}

// Unsubscribe закрывает канал. Идемпотентно.
func (w *PortWatcher) Unsubscribe(id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch, ok := w.subs[id]
	if !ok {
		return
	}
	delete(w.subs, id)
	close(ch)
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func diffStringSlices(prev, cur []string) []string {
	seen := make(map[string]bool, len(prev))
	for _, p := range prev {
		seen[p] = true
	}
	var out []string
	for _, c := range cur {
		if !seen[c] {
			out = append(out, c)
		}
	}
	return out
}

// defaultListPorts читает /dev/serial/by-id, а если там пусто —
// fallback на /dev/ttyUSB* и /dev/ttyACM*.
//
// by-id — предпочтительно: путь привязан к конкретному устройству
// (по серийному номеру) и не переезжает между перезагрузками.
func defaultListPorts() []string {
	var out []string
	if entries, err := os.ReadDir("/dev/serial/by-id"); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				out = append(out, "/dev/serial/by-id/"+e.Name())
			}
		}
	}
	if len(out) == 0 {
		for _, pattern := range []string{
			"/dev/ttyUSB*", "/dev/ttyACM*",
		} {
			if matches, err := filepath.Glob(pattern); err == nil {
				out = append(out, matches...)
			}
		}
	}
	sort.Strings(out)
	return out
}
