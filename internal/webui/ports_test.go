package webui

import (
	"sync/atomic"
	"testing"
	"time"
)

// fakeList возвращает текущий список, управляемый атомарно.
type fakeList struct {
	ports atomic.Value // []string
}

func newFakeList(initial []string) *fakeList {
	f := &fakeList{}
	f.ports.Store(initial)
	return f
}

func (f *fakeList) Set(p []string) { f.ports.Store(p) }
func (f *fakeList) Get() []string  { return f.ports.Load().([]string) }

func TestPortWatcherSubscribeSnapshot(t *testing.T) {
	f := newFakeList([]string{"/dev/a", "/dev/b"})
	w := NewPortWatcherWithLister(f.Get, time.Hour)
	w.Refresh()

	id, snap, _ := w.Subscribe()
	defer w.Unsubscribe(id)

	if len(snap.Ports) != 2 || snap.Ports[0] != "/dev/a" {
		t.Errorf("snap=%v", snap.Ports)
	}
}

func TestPortWatcherEmitsOnChange(t *testing.T) {
	f := newFakeList(nil)
	w := NewPortWatcherWithLister(f.Get, time.Hour)
	w.Refresh()

	id, _, ch := w.Subscribe()
	defer w.Unsubscribe(id)

	f.Set([]string{"/dev/a"})
	w.Refresh()

	select {
	case ev := <-ch:
		if len(ev.Ports) != 1 || ev.Ports[0] != "/dev/a" {
			t.Errorf("ports=%v", ev.Ports)
		}
		if len(ev.New) != 1 || ev.New[0] != "/dev/a" {
			t.Errorf("new=%v", ev.New)
		}
	case <-time.After(time.Second):
		t.Fatal("не пришло событие")
	}
}

func TestPortWatcherNoEventOnSameList(t *testing.T) {
	f := newFakeList([]string{"/dev/a"})
	w := NewPortWatcherWithLister(f.Get, time.Hour)
	w.Refresh()

	id, _, ch := w.Subscribe()
	defer w.Unsubscribe(id)

	w.Refresh() // список не менялся

	select {
	case ev := <-ch:
		t.Errorf("неожиданное событие: %v", ev)
	case <-time.After(50 * time.Millisecond):
		// ок
	}
}

func TestPortWatcherMultipleSubscribers(t *testing.T) {
	f := newFakeList(nil)
	w := NewPortWatcherWithLister(f.Get, time.Hour)
	w.Refresh()

	id1, _, ch1 := w.Subscribe()
	id2, _, ch2 := w.Subscribe()
	defer w.Unsubscribe(id1)
	defer w.Unsubscribe(id2)

	f.Set([]string{"/dev/a"})
	w.Refresh()

	for i, ch := range []<-chan PortEvent{ch1, ch2} {
		select {
		case ev := <-ch:
			if len(ev.Ports) != 1 {
				t.Errorf("[%d] ev=%v", i, ev)
			}
		case <-time.After(time.Second):
			t.Fatalf("[%d] не пришло", i)
		}
	}
}

func TestPortWatcherUnsubscribe(t *testing.T) {
	f := newFakeList(nil)
	w := NewPortWatcherWithLister(f.Get, time.Hour)

	id, _, ch := w.Subscribe()
	w.Unsubscribe(id)

	// Канал закрыт.
	_, ok := <-ch
	if ok {
		t.Error("канал не закрыт после Unsubscribe")
	}
	// Повторный Unsubscribe — не паника.
	w.Unsubscribe(id)
}

func TestPortWatcherRunStops(t *testing.T) {
	f := newFakeList(nil)
	w := NewPortWatcherWithLister(f.Get, 10*time.Millisecond)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		w.Run(stop)
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)
	close(stop)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run не завершился")
	}
}

func TestDiffStringSlices(t *testing.T) {
	cases := []struct {
		prev, cur, want []string
	}{
		{nil, nil, nil},
		{nil, []string{"a"}, []string{"a"}},
		{[]string{"a"}, nil, nil},
		{[]string{"a"}, []string{"a"}, nil},
		{[]string{"a"}, []string{"a", "b"}, []string{"b"}},
		{[]string{"a", "b"}, []string{"b"}, nil},
	}
	for i, c := range cases {
		got := diffStringSlices(c.prev, c.cur)
		if len(got) != len(c.want) {
			t.Errorf("[%d] got %v, want %v", i, got, c.want)
			continue
		}
		for j := range got {
			if got[j] != c.want[j] {
				t.Errorf("[%d][%d] got %q, want %q", i, j, got[j], c.want[j])
			}
		}
	}
}
