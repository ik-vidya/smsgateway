package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockTelegram — HTTP-сервер, который считает запросы.
type mockTelegram struct {
	*httptest.Server
	mu       sync.Mutex
	requests []map[string]any
	failWith int // если >0 — отвечает этим кодом
}

func newMockTelegram(t *testing.T) *mockTelegram {
	t.Helper()
	m := &mockTelegram{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)

		m.mu.Lock()
		m.requests = append(m.requests, req)
		fail := m.failWith
		m.mu.Unlock()

		if fail > 0 {
			w.WriteHeader(fail)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *mockTelegram) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *mockTelegram) last() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return nil
	}
	return m.requests[len(m.requests)-1]
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newNotifier(t *testing.T, m *mockTelegram, cfg TelegramConfig) *TelegramNotifier {
	t.Helper()
	cfg.BotToken = "test-token"
	cfg.ChatID = "123456"
	cfg.APIBase = m.URL
	cfg.Logger = quietLogger()
	if cfg.RateLimit == 0 {
		cfg.RateLimit = 10 * time.Millisecond
	}
	if cfg.DedupWindow == 0 {
		cfg.DedupWindow = 100 * time.Millisecond
	}
	n, err := NewTelegram(cfg)
	if err != nil {
		t.Fatalf("NewTelegram: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// ==================== основные ====================

func TestTelegramRequiresTokenAndChatID(t *testing.T) {
	_, err := NewTelegram(TelegramConfig{})
	if err == nil {
		t.Fatal("ожидалась ошибка при пустых настройках")
	}
	_, err = NewTelegram(TelegramConfig{BotToken: "x"})
	if err == nil {
		t.Fatal("ожидалась ошибка при пустом ChatID")
	}
}

func TestTelegramNotify(t *testing.T) {
	m := newMockTelegram(t)
	n := newNotifier(t, m, TelegramConfig{})

	n.Notify(context.Background(), LevelCritical, errors.New("boom"))

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if m.count() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if m.count() != 1 {
		t.Fatalf("ожидался 1 запрос, получено %d", m.count())
	}
	last := m.last()
	if last["chat_id"] != "123456" {
		t.Errorf("chat_id=%v", last["chat_id"])
	}
	text, _ := last["text"].(string)
	if !strings.Contains(text, "boom") {
		t.Errorf("text=%q не содержит 'boom'", text)
	}
	if !strings.Contains(text, "CRITICAL") {
		t.Errorf("text=%q не содержит CRITICAL", text)
	}
}

func TestTelegramNotifyNilError(t *testing.T) {
	m := newMockTelegram(t)
	n := newNotifier(t, m, TelegramConfig{})

	n.Notify(context.Background(), LevelCritical, nil)
	time.Sleep(50 * time.Millisecond)

	if m.count() != 0 {
		t.Fatalf("nil error не должен отправляться, получено %d", m.count())
	}
}

func TestTelegramDeduplication(t *testing.T) {
	m := newMockTelegram(t)
	n := newNotifier(t, m, TelegramConfig{
		DedupWindow: 500 * time.Millisecond,
	})

	for i := 0; i < 5; i++ {
		n.Notify(context.Background(), LevelCritical, errors.New("same error"))
	}
	time.Sleep(200 * time.Millisecond)

	if m.count() != 1 {
		t.Fatalf("ожидался 1 запрос (дедупликация), получено %d", m.count())
	}
}

func TestTelegramRateLimit(t *testing.T) {
	m := newMockTelegram(t)
	n := newNotifier(t, m, TelegramConfig{
		RateLimit:   200 * time.Millisecond,
		DedupWindow: 10 * time.Millisecond,
	})

	// Три разных сообщения — все должны уйти, но с паузой.
	for i := 0; i < 3; i++ {
		n.Notify(context.Background(), LevelCritical,
			errors.New("err"+string(rune('0'+i))))
	}

	// Сразу должно быть мало.
	time.Sleep(50 * time.Millisecond)
	early := m.count()
	if early > 1 {
		t.Errorf("rate limit не работает: %d запросов за 50мс", early)
	}

	// К концу паузы — все три.
	time.Sleep(800 * time.Millisecond)
	if m.count() != 3 {
		t.Fatalf("ожидалось 3 запроса, получено %d", m.count())
	}
}

func TestTelegramHTTPError(t *testing.T) {
	m := newMockTelegram(t)
	m.failWith = http.StatusInternalServerError
	n := newNotifier(t, m, TelegramConfig{})

	n.Notify(context.Background(), LevelCritical, errors.New("boom"))
	time.Sleep(200 * time.Millisecond)

	// Запрос был (хоть и упал) — не должно быть паники.
	if m.count() != 1 {
		t.Fatalf("ожидался 1 запрос, получено %d", m.count())
	}
}

// TestTelegramQueueOverflow проверяет, что при переполнении
// очереди Notify не блокируется и не паникует, а лишние
// сообщения тихо дропаются.
//
// DedupWindow здесь 1ms — маленькое, но не экстремальное
// значение. Совсем крошечные окна (наносекунды) ломали
// time.NewTicker в dedupCleaner — этот случай закрыт клампом
// в коде и покрыт отдельным тестом TestTelegramTinyDedupWindow.
func TestTelegramQueueOverflow(t *testing.T) {
	m := newMockTelegram(t)
	n := newNotifier(t, m, TelegramConfig{
		QueueSize:   2,
		RateLimit:   200 * time.Millisecond,
		DedupWindow: time.Millisecond,
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n.Notify(context.Background(), LevelCritical,
				errors.New("err-"+string(rune('a'+i))))
		}(i)
	}
	wg.Wait()

	// Никаких блокировок и паник — уже успех.
	// Close() выполнится в t.Cleanup и не должен зависнуть.
}

// TestTelegramTinyDedupWindow — регрессионный тест на панику
// "non-positive interval for NewTicker": раньше при
// DedupWindow=time.Nanosecond dedupCleaner вызывал
// time.NewTicker(0) и ронял процесс.
func TestTelegramTinyDedupWindow(t *testing.T) {
	m := newMockTelegram(t)
	n := newNotifier(t, m, TelegramConfig{
		DedupWindow: time.Nanosecond,
	})

	n.Notify(context.Background(), LevelCritical, errors.New("boom"))
	time.Sleep(50 * time.Millisecond)

	if m.count() != 1 {
		t.Fatalf("ожидался 1 запрос, получено %d", m.count())
	}
	// Close в t.Cleanup не должен зависнуть.
}

// TestTelegramCloseIsIdempotent — повторный Close не паникует.
func TestTelegramCloseIsIdempotent(t *testing.T) {
	m := newMockTelegram(t)
	n := newNotifier(t, m, TelegramConfig{})

	n.Notify(context.Background(), LevelCritical, errors.New("boom"))
	time.Sleep(100 * time.Millisecond)

	if err := n.Close(); err != nil {
		t.Fatalf("первый Close: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("повторный Close: %v", err)
	}
}

// TestTelegramCloseDrainsQueue — Close должен дождаться отправки
// уже поставленных в очередь сообщений.
func TestTelegramCloseDrainsQueue(t *testing.T) {
	m := newMockTelegram(t)
	n, err := NewTelegram(TelegramConfig{
		BotToken:    "test-token",
		ChatID:      "123456",
		APIBase:     m.URL,
		Logger:      quietLogger(),
		QueueSize:   8,
		RateLimit:   10 * time.Millisecond,
		DedupWindow: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewTelegram: %v", err)
	}

	for i := 0; i < 3; i++ {
		n.Notify(context.Background(), LevelCritical,
			errors.New("drain-"+string(rune('a'+i))))
	}

	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// После Close всё, что было в очереди, должно быть отправлено.
	if m.count() != 3 {
		t.Fatalf("после Close отправлено %d, ожидалось 3", m.count())
	}
}

func TestNoopNotifier(t *testing.T) {
	var n Noop
	// Не должно паниковать.
	n.Notify(context.Background(), LevelCritical, errors.New("x"))
	if err := n.Close(); err != nil {
		t.Errorf("Noop.Close: %v", err)
	}
}

// ==================== formatMessage / dedupKey ====================

func TestFormatMessage(t *testing.T) {
	got := formatMessage(LevelCritical, "test error")
	if !strings.Contains(got, "CRITICAL") || !strings.Contains(got, "test error") {
		t.Errorf("formatMessage=%q", got)
	}
	got = formatMessage(LevelWarning, "warn")
	if !strings.Contains(got, "WARNING") {
		t.Errorf("formatMessage=%q", got)
	}
}

func TestDedupKey(t *testing.T) {
	a := dedupKey(LevelCritical, "x")
	b := dedupKey(LevelCritical, "x")
	c := dedupKey(LevelWarning, "x")
	if a != b {
		t.Error("одинаковые ключи должны совпадать")
	}
	if a == c {
		t.Error("разные level — разные ключи")
	}
}
