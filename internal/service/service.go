// Package service — сервисный слой smsgateway.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ik-vidya/smsgateway/internal/diagnostics"
	"github.com/ik-vidya/smsgateway/internal/gammu"
	"github.com/ik-vidya/smsgateway/internal/modem"
	"github.com/ik-vidya/smsgateway/internal/notify"
	"github.com/ik-vidya/smsgateway/internal/storage"
)

const (
	DefaultSendMaxAttempts   = 3
	DefaultTestSMSTimeout    = 30 * time.Second
	DefaultTestSMSPollPeriod = 500 * time.Millisecond
)

var DefaultSendBackoff = []time.Duration{
	1 * time.Second,
	5 * time.Second,
}

// Service — координатор.
type Service struct {
	modem    Modem
	store    *storage.Store
	logger   *slog.Logger
	notifier notify.Notifier

	// gammu опционален: nil, если GAMMU_ROOT не задан или
	// файловый бэкенд отключён.
	gammu *gammu.Manager

	sendInterval    time.Duration
	pendingInterval time.Duration
	refTTL          time.Duration
	deleteAfterRead bool

	sendMaxAttempts int
	sendBackoff     []time.Duration

	selfNumber     string
	testSMSTimeout time.Duration

	mu          sync.RWMutex
	networkStat int
	lastCSQ     int
	startTime   time.Time

	wg sync.WaitGroup
}

// Modem — интерфейс модема, используемый сервисом.
type Modem interface {
	Init(ctx context.Context) error
	SendSMS(ctx context.Context, peer, text string, opts modem.SendOptions) (modem.SendResult, error)
	ReadSMS(ctx context.Context, filter modem.ReadFilter) ([]modem.SMSMessage, error)
	DeleteSMS(ctx context.Context, index int) error
	Exec(ctx context.Context, cmd string) (string, error)
	URCs() <-chan modem.URC
	Info() modem.Info
	Close() error
}

// Config — параметры сервиса.
type Config struct {
	SendInterval    time.Duration
	PendingInterval time.Duration
	ReferenceTTL    time.Duration
	DeleteAfterRead bool

	SendMaxAttempts int
	SendBackoff     []time.Duration

	SelfNumber     string
	TestSMSTimeout time.Duration

	Notifier notify.Notifier

	// Gammu — опциональный менеджер файлового бэкенда.
	// Если задан, Service.Run запускает его watcher и
	// экспортирует принятые SMS в inbox.
	Gammu *gammu.Manager
}

func (c Config) withDefaults() Config {
	if c.SendInterval <= 0 {
		c.SendInterval = time.Second
	}
	if c.PendingInterval <= 0 {
		c.PendingInterval = 30 * time.Second
	}
	if c.ReferenceTTL <= 0 {
		c.ReferenceTTL = time.Hour
	}
	if c.SendMaxAttempts <= 0 {
		c.SendMaxAttempts = DefaultSendMaxAttempts
	}
	if len(c.SendBackoff) == 0 {
		c.SendBackoff = DefaultSendBackoff
	}
	if c.TestSMSTimeout <= 0 {
		c.TestSMSTimeout = DefaultTestSMSTimeout
	}
	return c
}

// New создаёт сервис.
func New(m Modem, store *storage.Store, logger *slog.Logger, cfg Config) *Service {
	cfg = cfg.withDefaults()
	if cfg.Notifier == nil {
		cfg.Notifier = notify.Noop{}
	}
	return &Service{
		modem:           m,
		store:           store,
		logger:          logger,
		sendInterval:    cfg.SendInterval,
		pendingInterval: cfg.PendingInterval,
		refTTL:          cfg.ReferenceTTL,
		deleteAfterRead: cfg.DeleteAfterRead,
		sendMaxAttempts: cfg.SendMaxAttempts,
		sendBackoff:     cfg.SendBackoff,
		selfNumber:      cfg.SelfNumber,
		testSMSTimeout:  cfg.TestSMSTimeout,
		notifier:        cfg.Notifier,
		gammu:           cfg.Gammu,
		startTime:       time.Now(),
	}
}

// Run запускает сервис. Блокируется до отмены ctx.
func (s *Service) Run(ctx context.Context) error {
	s.logger.Info("service: запуск")

	if err := s.modem.Init(ctx); err != nil {
		err = fmt.Errorf("инициализация модема: %w", err)
		s.notifyCritical(ctx, err)
		return err
	}

	// Восстановление «сирот»: сообщения source='gammu-outbox'
	// без записи в gammu_outbox (сервис упал между записью в БД
	// и созданием entry). Переводим их в failed.
	if s.gammu != nil {
		s.recoverGammuOrphans(ctx)
	}

	s.wg.Add(3)
	go s.urcLoop(ctx)
	go s.sendLoop(ctx)
	go s.pendingLoop(ctx)

	if s.gammu != nil {
		s.wg.Add(1)
		go s.gammuLoop(ctx)
	}

	<-ctx.Done()
	s.logger.Info("service: остановка, ждём горутины")

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.logger.Info("service: горутины завершены")
	case <-time.After(10 * time.Second):
		s.logger.Warn("service: таймаут ожидания горутин")
	}
	return nil
}

// gammuLoop запускает watcher gammu.Manager и ждёт его завершения
// вместе с остальными горутинами Service.
func (s *Service) gammuLoop(ctx context.Context) {
	defer s.wg.Done()
	s.logger.Info("service: gammu manager запущен")
	if err := s.gammu.Run(ctx); err != nil {
		s.logger.Error("service: gammu manager завершился с ошибкой",
			"err", err)
		s.notifyWarning(ctx, err)
	}
	s.logger.Info("service: gammu manager остановлен")
}

// recoverGammuOrphans переводит в failed исходящие сообщения
// source='gammu-outbox' без соответствующей записи в gammu_outbox.
// Вызывается один раз при старте Run.
func (s *Service) recoverGammuOrphans(ctx context.Context) {
	orphans, err := s.store.OrphanOutboxMessages(ctx)
	if err != nil {
		s.logger.Warn("service: OrphanOutboxMessages", "err", err)
		return
	}
	for _, m := range orphans {
		s.logger.Warn("service: gammu-сирота, помечаю failed",
			"id", m.ID, "peer", m.Peer)
		if err := s.store.MarkFailed(ctx, m.ID,
			"файл outbox потерян (сервис перезапущен)"); err != nil {
			s.logger.Warn("service: MarkFailed для сироты",
				"id", m.ID, "err", err)
		}
	}
}

// Uptime — время с момента старта.
func (s *Service) Uptime() time.Duration { return time.Since(s.startTime) }

// NetworkStatus — текущий статус сети.
func (s *Service) NetworkStatus() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.networkStat
}

// LastCSQ — последний измеренный уровень сигнала.
func (s *Service) LastCSQ() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastCSQ
}

// ModemInfo возвращает информацию о модеме.
func (s *Service) ModemInfo() modem.Info {
	if s.modem == nil {
		return modem.Info{}
	}
	return s.modem.Info()
}

// notifyCritical отправляет критическую ошибку через Notifier.
// Не блокирует вызывающего.
func (s *Service) notifyCritical(ctx context.Context, err error) {
	if s.notifier == nil || err == nil {
		return
	}
	s.notifier.Notify(ctx, notify.LevelCritical, err)
}

// notifyWarning отправляет предупреждение через Notifier.
func (s *Service) notifyWarning(ctx context.Context, err error) {
	if s.notifier == nil || err == nil {
		return
	}
	s.notifier.Notify(ctx, notify.LevelWarning, err)
}

// ==================== SendTestSMS ====================

// TestSMSResult — результат проверочной SMS.
type TestSMSResult struct {
	ID          int64
	Status      storage.Status
	Reference   int64
	SentAt      time.Time
	DeliveredAt time.Time
	Error       string
	TimedOut    bool
}

// SendTestSMS отправляет SMS на selfNumber (SELF_NUMBER из конфига)
// и ждёт терминального статуса или таймаута.
func (s *Service) SendTestSMS(ctx context.Context, text string) (TestSMSResult, error) {
	if s.selfNumber == "" {
		return TestSMSResult{}, errors.New("SELF_NUMBER не задан в конфиге")
	}
	if text == "" {
		text = "smsgateway selftest " + time.Now().Format("15:04:05")
	}

	msg := &storage.Message{
		Direction: storage.DirectionOut,
		Status:    storage.StatusQueued,
		Peer:      s.selfNumber,
		Text:      text,
	}
	id, err := s.store.CreateMessage(ctx, msg)
	if err != nil {
		return TestSMSResult{}, fmt.Errorf("CreateMessage: %w", err)
	}
	s.logger.Info("service: test-sms поставлен в очередь",
		"id", id, "peer", s.selfNumber)

	return s.waitForTerminal(ctx, id)
}

func (s *Service) waitForTerminal(ctx context.Context, id int64) (TestSMSResult, error) {
	deadline := time.Now().Add(s.testSMSTimeout)
	tick := time.NewTicker(DefaultTestSMSPollPeriod)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			m, _ := s.store.GetMessage(context.Background(), id)
			return resultFromMessage(id, m), ctx.Err()
		case <-tick.C:
		}

		m, err := s.store.GetMessage(ctx, id)
		if err != nil {
			return TestSMSResult{ID: id}, fmt.Errorf("GetMessage: %w", err)
		}
		if m.Status.IsTerminal() {
			res := resultFromMessage(id, m)
			s.logger.Info("service: test-sms завершён",
				"id", id, "status", m.Status)
			return res, nil
		}
		if time.Now().After(deadline) {
			res := resultFromMessage(id, m)
			res.TimedOut = true
			s.logger.Warn("service: test-sms timeout",
				"id", id, "status", m.Status)
			return res, nil
		}
	}
}

func resultFromMessage(id int64, m *storage.Message) TestSMSResult {
	if m == nil {
		return TestSMSResult{ID: id}
	}
	res := TestSMSResult{
		ID:     id,
		Status: m.Status,
		Error:  m.Error,
	}
	if m.Reference.Valid {
		res.Reference = m.Reference.Int64
	}
	if m.SentAt.Valid {
		res.SentAt = m.SentAt.Time
	}
	if m.DeliveredAt.Valid {
		res.DeliveredAt = m.DeliveredAt.Time
	}
	return res
}

// ==================== Diagnostics ====================

// RunDiagnostics собирает диагностику модема.
func (s *Service) RunDiagnostics(ctx context.Context) (diagnostics.Result, error) {
	return diagnostics.Collect(ctx, s.modem)
}

// ==================== URC-цикл ====================

func (s *Service) urcLoop(ctx context.Context) {
	defer s.wg.Done()
	s.logger.Info("service: URC-цикл запущен")

	urcCh := s.modem.URCs()
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("service: URC-цикл остановлен")
			return
		case u, ok := <-urcCh:
			if !ok {
				s.logger.Info("service: URC-канал закрыт")
				return
			}
			s.handleURC(ctx, u)
		}
	}
}

func (s *Service) handleURC(ctx context.Context, u modem.URC) {
	switch v := u.(type) {
	case modem.URCCMTI:
		s.logger.Info("service: новый SMS",
			"mem", v.Mem, "index", v.Index)
		if err := s.ingestSMS(ctx, v.Index); err != nil {
			s.logger.Error("service: ingestSMS", "err", err, "index", v.Index)
		}
	case modem.URCCDS:
		s.logger.Info("service: delivery report",
			"ref", v.Reference, "tpst", v.TPST, "peer", v.Peer)
		if v.PDU == "" {
			s.logger.Warn("service: +CDS без PDU, пропускаю")
			return
		}
		if err := s.HandleDeliveryReport(ctx,
			v.Reference, v.TPST, v.PDU); err != nil {
			s.logger.Warn("service: HandleDeliveryReport",
				"ref", v.Reference, "err", err)
		}
	case modem.URCCREG:
		s.mu.Lock()
		s.networkStat = v.Stat
		s.mu.Unlock()
		s.logger.Info("service: регистрация",
			"stat", v.Stat, "lac", v.LAC, "cid", v.CID)
	case modem.URCCGREG:
		s.logger.Info("service: GPRS-регистрация",
			"stat", v.Stat, "lac", v.LAC, "cid", v.CID)
	case modem.URCCBC:
		s.logger.Debug("service: батарея",
			"percent", v.Percent, "mv", v.MilliV)
	case modem.URCRDY:
		s.logger.Warn("service: модем перезапустился (RDY)")
	case modem.URCRing:
		s.logger.Info("service: входящий звонок")
	case modem.URCNoCarrier:
		s.logger.Info("service: соединение потеряно")
	case modem.URCCUSD:
		s.logger.Info("service: USSD", "stat", v.Stat, "text", v.Text)
	default:
		s.logger.Debug("service: неизвестный URC", "kind", u.Kind())
	}
}

func (s *Service) ingestSMS(ctx context.Context, index int) error {
	msgs, err := s.modem.ReadSMS(ctx, modem.ReadFilter{})
	if err != nil {
		return fmt.Errorf("ReadSMS: %w", err)
	}
	var target *modem.SMSMessage
	for i := range msgs {
		if msgs[i].Index == index {
			target = &msgs[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("SMS с index=%d не найдена", index)
	}

	existing, _, err := s.store.ListMessages(ctx, storage.Filter{
		Direction: storage.DirectionIn,
		Peer:      target.Peer,
		Limit:     1,
	})
	if err != nil {
		return fmt.Errorf("дедупликация: %w", err)
	}
	if len(existing) > 0 && existing[0].Text == target.Text {
		if s.deleteAfterRead {
			_ = s.modem.DeleteSMS(ctx, index)
		}
		return nil
	}

	m := &storage.Message{
		Direction: storage.DirectionIn,
		Status:    storage.StatusReceived,
		Peer:      target.Peer,
		Text:      target.Text,
		RawPDU:    target.RawPDU,
	}
	id, err := s.store.CreateMessage(ctx, m)
	if err != nil {
		return fmt.Errorf("CreateMessage: %w", err)
	}
	s.logger.Info("service: SMS сохранено",
		"id", id, "peer", target.Peer, "len", len(target.Text))

	// Экспорт в Gammu inbox (если файловый бэкенд активен).
	// Ошибка файла не блокирует приём — БД уже обновлена.
	s.writeToGammuInbox(target, id)

	if s.deleteAfterRead {
		if err := s.modem.DeleteSMS(ctx, index); err != nil {
			s.logger.Warn("service: не удалось удалить SMS из модема",
				"index", index, "err", err)
		}
	}
	return nil
}

// ==================== Отправка ====================

func (s *Service) sendLoop(ctx context.Context) {
	defer s.wg.Done()
	s.logger.Info("service: цикл отправки запущен",
		"interval", s.sendInterval,
		"max_attempts", s.sendMaxAttempts)

	ticker := time.NewTicker(s.sendInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("service: цикл отправки остановлен")
			return
		case <-ticker.C:
			s.processQueue(ctx)
		}
	}
}

func (s *Service) processQueue(ctx context.Context) {
	msgs, _, err := s.store.ListMessages(ctx, storage.Filter{
		Direction: storage.DirectionOut,
		Status:    storage.StatusQueued,
		Limit:     1,
	})
	if err != nil {
		s.logger.Error("service: выборка очереди", "err", err)
		return
	}
	if len(msgs) == 0 {
		return
	}
	s.sendOne(ctx, msgs[0])
}

func (s *Service) sendOne(ctx context.Context, msg *storage.Message) {
	s.logger.Info("service: отправка",
		"id", msg.ID, "peer", msg.Peer)

	if err := s.store.MarkSending(ctx, msg.ID); err != nil {
		s.logger.Error("service: MarkSending", "err", err, "id", msg.ID)
		return
	}

	res, attempts, err := s.sendWithRetry(ctx, msg)
	if err != nil {
		s.logger.Warn("service: SendSMS не удалось",
			"id", msg.ID, "attempts", attempts,
			"parts_sent", len(res.References),
			"err", err)
		_ = s.store.MarkFailed(ctx, msg.ID, err.Error())
		return
	}

	refs := res.References
	if len(refs) == 0 && res.Reference != 0 {
		refs = []int64{res.Reference}
	}
	if len(refs) == 0 {
		s.logger.Error("service: SendSMS вернул пустой References",
			"id", msg.ID)
		_ = s.store.MarkFailed(ctx, msg.ID, "нет reference от модема")
		return
	}

	for _, ref := range refs {
		if err := s.store.ReserveReference(ctx, ref, msg.ID, s.refTTL); err != nil {
			s.logger.Warn("service: не удалось забронировать reference",
				"ref", ref, "id", msg.ID, "err", err)
		}
	}

	if err := s.store.MarkSent(ctx, msg.ID, refs[0]); err != nil {
		s.logger.Error("service: MarkSent", "err", err, "id", msg.ID)
		return
	}
	s.logger.Info("service: SMS отправлено",
		"id", msg.ID, "parts", len(refs), "refs", refs,
		"attempts", attempts)
}

func (s *Service) sendWithRetry(ctx context.Context, msg *storage.Message) (modem.SendResult, int, error) {
	var lastErr error
	var lastRes modem.SendResult

	for attempt := 1; attempt <= s.sendMaxAttempts; attempt++ {
		res, err := s.modem.SendSMS(ctx, msg.Peer, msg.Text,
			modem.SendOptions{RequestDR: true})
		if err == nil {
			return res, attempt, nil
		}
		lastErr = err
		lastRes = res

		if !modem.IsTransientError(err) {
			s.logger.Warn("service: постоянная ошибка, retry не поможет",
				"id", msg.ID, "attempt", attempt, "err", err)
			return res, attempt, err
		}
		if attempt >= s.sendMaxAttempts {
			break
		}
		wait := s.backoffFor(attempt)
		if wait > 0 {
			s.logger.Warn("service: транзиентная ошибка, повтор",
				"id", msg.ID, "attempt", attempt, "max", s.sendMaxAttempts,
				"wait", wait, "err", err)
			select {
			case <-ctx.Done():
				return lastRes, attempt, ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	return lastRes, s.sendMaxAttempts, lastErr
}

func (s *Service) backoffFor(attempt int) time.Duration {
	idx := attempt - 1
	if idx < 0 || idx >= len(s.sendBackoff) {
		return 0
	}
	return s.sendBackoff[idx]
}

// ==================== Pending references ====================

func (s *Service) pendingLoop(ctx context.Context) {
	defer s.wg.Done()
	s.logger.Info("service: цикл pending-reference запущен",
		"interval", s.pendingInterval)

	ticker := time.NewTicker(s.pendingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("service: цикл pending-reference остановлен")
			return
		case <-ticker.C:
			s.processExpiredReferences(ctx)
		}
	}
}

func (s *Service) processExpiredReferences(ctx context.Context) {
	expired, err := s.store.ExpirePendingReferences(ctx)
	if err != nil {
		s.logger.Error("service: ExpirePendingReferences", "err", err)
		return
	}
	for _, p := range expired {
		s.logger.Info("service: reference просрочен",
			"ref", p.Reference, "message_id", p.MessageID)
		if err := s.store.MarkSentNoDR(ctx, p.MessageID); err != nil {
			s.logger.Warn("service: MarkSentNoDR",
				"id", p.MessageID, "err", err)
		}
	}
}

// ==================== Delivery reports ====================

func (s *Service) HandleDeliveryReport(ctx context.Context, ref int, tpst int, rawPDU string) error {
	msgID, err := s.store.FindPendingReference(ctx, int64(ref))
	if err != nil {
		return fmt.Errorf("FindPendingReference(%d): %w", ref, err)
	}

	dr := &storage.DeliveryReport{
		MessageID:  nullInt64(msgID),
		Reference:  int64(ref),
		TPST:       tpst,
		StatusText: interpretTPST(tpst),
		RawPDU:     rawPDU,
		ReceivedAt: time.Now().UTC(),
	}
	if _, err := s.store.CreateDeliveryReport(ctx, dr); err != nil {
		return fmt.Errorf("CreateDeliveryReport: %w", err)
	}
	if err := s.store.ReleaseReference(ctx, int64(ref)); err != nil {
		s.logger.Warn("service: ReleaseReference", "ref", ref, "err", err)
	}
	s.logger.Info("service: DR обработан",
		"ref", ref, "msg_id", msgID, "tpst", tpst)
	return nil
}

func interpretTPST(tpst int) string {
	if tpst == 0x00 {
		return "доставлено"
	}
	return fmt.Sprintf("не доставлено (TP-ST=0x%02X)", tpst)
}

func nullInt64(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: true}
}
