// Package notify, файл telegram.go — Telegram-нотификатор.
//
// Асинхронный: Notify кладёт сообщение в ограниченную очередь,
// отдельная горутина отправляет через Bot API. Дедупликация по
// тексту ошибки (окно DedupWindow), rate limit (минимальный
// интервал между отправками).
//
// Порядок остановки в Close():
//  1. close(queue)   — Notify после этого дропается/паникует.
//  2. workerWg.Wait() — воркер домётывает очередь и выходит.
//  3. close(stopDedup) + dedupWg.Wait() — уборщик выходит.
//  4. cancel()        — отменяет контекст HTTP-запросов.
//
// Именно такой порядок: dedupCleaner ждёт ctx.Done(), а ctx
// отменяется в самом конце. Если ждать его в общем wg.Wait(),
// получится вечный дедлок (проверено на практике).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// minCleanupInterval — нижняя граница периода уборки кэша
// дедупликации. Нужна, потому что DedupWindow может быть
// меньше 2 наносекунд (в тестах), и DedupWindow/2 даст 0 —
// time.NewTicker(0) паникует.
const minCleanupInterval = 100 * time.Millisecond

// TelegramConfig — параметры нотификатора.
type TelegramConfig struct {
	// Обязательные.
	BotToken string
	ChatID   string

	// Логгер. Если nil — slog.Default().
	Logger *slog.Logger

	// Очередь. 0 — 64.
	QueueSize int

	// Rate limit: минимальный интервал между отправками.
	// 0 — 1 секунда.
	RateLimit time.Duration

	// Окно дедупликации: одинаковые ошибки в этом окне
	// отправляются один раз. 0 — 5 минут.
	DedupWindow time.Duration

	// Таймаут HTTP-запроса к Telegram. 0 — 10 секунд.
	RequestTimeout time.Duration

	// APIBase — базовый URL Bot API (для тестов).
	// Пусто — https://api.telegram.org.
	APIBase string
}

func (c TelegramConfig) withDefaults() TelegramConfig {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 64
	}
	if c.RateLimit <= 0 {
		c.RateLimit = time.Second
	}
	if c.DedupWindow <= 0 {
		c.DedupWindow = 5 * time.Minute
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 10 * time.Second
	}
	if c.APIBase == "" {
		c.APIBase = "https://api.telegram.org"
	}
	return c
}

// TelegramNotifier — реализация Notifier поверх Telegram Bot API.
type TelegramNotifier struct {
	cfg    TelegramConfig
	logger *slog.Logger
	client *http.Client

	queue chan outgoing

	mu       sync.Mutex
	dedup    map[string]time.Time
	lastSent time.Time

	ctx    context.Context
	cancel context.CancelFunc

	// workerWg — воркер отправки.
	// dedupWg  — уборщик кэша дедупликации.
	// Разделены, чтобы Close мог сначала дождаться воркера
	// (домести очередь), и только потом остановить уборщик.
	workerWg  sync.WaitGroup
	dedupWg   sync.WaitGroup
	stopDedup chan struct{}
	closeOnce sync.Once
}

type outgoing struct {
	level Level
	text  string
}

// NewTelegram создаёт нотификатор. Возвращает ошибку, если
// BotToken или ChatID пустые.
func NewTelegram(cfg TelegramConfig) (*TelegramNotifier, error) {
	if strings.TrimSpace(cfg.BotToken) == "" {
		return nil, errors.New("Telegram: BotToken не задан")
	}
	if strings.TrimSpace(cfg.ChatID) == "" {
		return nil, errors.New("Telegram: ChatID не задан")
	}
	cfg = cfg.withDefaults()

	ctx, cancel := context.WithCancel(context.Background())
	n := &TelegramNotifier{
		cfg:       cfg,
		logger:    cfg.Logger,
		client:    &http.Client{Timeout: cfg.RequestTimeout},
		queue:     make(chan outgoing, cfg.QueueSize),
		dedup:     make(map[string]time.Time),
		ctx:       ctx,
		cancel:    cancel,
		stopDedup: make(chan struct{}),
	}

	n.workerWg.Add(1)
	go n.worker()
	n.dedupWg.Add(1)
	go n.dedupCleaner()

	return n, nil
}

// Notify кладёт сообщение в очередь. Если ошибка встречалась в
// окне дедупликации или очередь переполнена — сообщение
// отбрасывается, но не блокирует вызывающего.
func (n *TelegramNotifier) Notify(_ context.Context, level Level, err error) {
	if err == nil {
		return
	}
	text := err.Error()
	key := dedupKey(level, text)

	now := time.Now()
	n.mu.Lock()
	if t, ok := n.dedup[key]; ok && now.Sub(t) < n.cfg.DedupWindow {
		n.mu.Unlock()
		return
	}
	n.dedup[key] = now
	n.mu.Unlock()

	select {
	case n.queue <- outgoing{level: level, text: text}:
	default:
		n.logger.Warn("telegram: очередь переполнена, дропаю сообщение",
			"level", level)
	}
}

// Close: graceful shutdown. Идемпотентен.
func (n *TelegramNotifier) Close() error {
	n.closeOnce.Do(func() {
		close(n.queue)
		n.workerWg.Wait()

		close(n.stopDedup)
		n.dedupWg.Wait()

		n.cancel()
	})
	return nil
}

func (n *TelegramNotifier) worker() {
	defer n.workerWg.Done()

	for msg := range n.queue {
		// Rate limit.
		n.mu.Lock()
		elapsed := time.Since(n.lastSent)
		wait := n.cfg.RateLimit - elapsed
		n.mu.Unlock()

		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-n.ctx.Done():
				return
			}
		}

		n.send(msg)

		n.mu.Lock()
		n.lastSent = time.Now()
		n.mu.Unlock()
	}
}

// send выполняет HTTP-запрос к Bot API.
func (n *TelegramNotifier) send(msg outgoing) {
	text := formatMessage(msg.level, msg.text)

	body := map[string]any{
		"chat_id":                  n.cfg.ChatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		n.logger.Error("telegram: marshal", "err", err)
		return
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", n.cfg.APIBase, n.cfg.BotToken)
	req, err := http.NewRequestWithContext(n.ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		n.logger.Error("telegram: request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		n.logger.Error("telegram: HTTP", "err", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		n.logger.Error("telegram: неожиданный статус",
			"status", resp.StatusCode)
		return
	}
	n.logger.Info("telegram: уведомление отправлено", "level", msg.level)
}

// dedupCleaner выходит либо по stopDedup (нормальный Close),
// либо по ctx.Done (аварийная отмена контекста).
//
// Период уборки — DedupWindow/2, но не меньше minCleanupInterval.
// DedupWindow может быть очень маленьким (в тестах — 1 наносекунда),
// и DedupWindow/2 даст 0 — time.NewTicker(0) паникует.
func (n *TelegramNotifier) dedupCleaner() {
	defer n.dedupWg.Done()

	interval := n.cfg.DedupWindow / 2
	if interval < minCleanupInterval {
		interval = minCleanupInterval
	}

	tick := time.NewTicker(interval)
	defer tick.Stop()

	for {
		select {
		case <-n.stopDedup:
			return
		case <-n.ctx.Done():
			return
		case <-tick.C:
			now := time.Now()
			n.mu.Lock()
			for k, t := range n.dedup {
				if now.Sub(t) >= n.cfg.DedupWindow {
					delete(n.dedup, k)
				}
			}
			n.mu.Unlock()
		}
	}
}

// formatMessage форматирует текст уведомления.
func formatMessage(level Level, text string) string {
	prefix := "ℹ️"
	switch level {
	case LevelCritical:
		prefix = "🚨"
	case LevelError:
		prefix = "❌"
	case LevelWarning:
		prefix = "⚠️"
	case LevelOK:
		prefix = "✅"
	}
	return fmt.Sprintf("%s [%s] smsgateway\n%s",
		prefix, strings.ToUpper(string(level)), text)
}

// dedupKey — ключ дедупликации.
func dedupKey(level Level, text string) string {
	return string(level) + "|" + text
}
