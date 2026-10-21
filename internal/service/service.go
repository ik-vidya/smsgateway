// Package service — сервисный слой smsgateway.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"git.vidya.lv/vidya/smsgateway/internal/diagnostics"
	"git.vidya.lv/vidya/smsgateway/internal/gammu"
	"git.vidya.lv/vidya/smsgateway/internal/modem"
	"git.vidya.lv/vidya/smsgateway/internal/notify"
	"git.vidya.lv/vidya/smsgateway/internal/storage"
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
	modem     Modem
	modemDesc string
	store     *storage.Store
	logger    *slog.Logger
	notifier  notify.Notifier

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

	mu              sync.RWMutex
	networkStat     int
	gprsNetworkStat int
	lastCSQ         int
	startTime       time.Time

	wg sync.WaitGroup

	fatalErrMu sync.Mutex
	fatalErr   error
}

func (s *Service) setFatalErr(err error) {
	s.fatalErrMu.Lock()
	defer s.fatalErrMu.Unlock()
	if s.fatalErr == nil {
		s.fatalErr = err
	}
}

func (s *Service) getFatalErr() error {
	s.fatalErrMu.Lock()
	defer s.fatalErrMu.Unlock()
	return s.fatalErr
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

	SelfNumber       string
	TestSMSTimeout   time.Duration
	ModemDescription string

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
		modemDesc:       cfg.ModemDescription,
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

	info := s.ModemInfo()
	s.notifier.Notify(ctx, notify.LevelOK, fmt.Errorf(
		"%s: modem initialized (IMEI: %s)",
		s.modemDesc, info.IMEI))

	// Восстановление «сирот»: сообщения source='gammu-outbox'
	// без записи в gammu_outbox (сервис упал между записью в БД
	// и созданием entry). Переводим их в failed.
	if s.gammu != nil {
		s.recoverGammuOrphans(ctx)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.wg.Add(4)
	go s.urcLoop(ctx, cancel)
	go s.sendLoop(ctx)
	go s.pendingLoop(ctx)
	go s.networkLoop(ctx)

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
	return s.getFatalErr()
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

// EnqueueOutgoing создаёт исходящее сообщение и ставит его в
// очередь отправки. Используется HTTP API и Web UI.
//
// Проверки формата — на вызывающем.
func (s *Service) EnqueueOutgoing(ctx context.Context,
	peer, text string) (int64, error) {

	m := &storage.Message{
		Direction: storage.DirectionOut,
		Status:    storage.StatusQueued,
		Source:    storage.SourceAPI,
		Peer:      peer,
		Text:      text,
	}
	return s.store.CreateMessage(ctx, m)
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

	res, err := s.waitForTerminal(ctx, id)
	if err != nil {
		return res, err
	}
	s.notifyTestResult(ctx, res)
	return res, nil
}

// notifyTestResult логирует результат самопроверки через Notifier.
//
// Используется SendTestSMS (и, через неё, POST /api/v1/test-sms и
// POST /setup/test в Web UI): инженеру, настраивающему модем без
// клавиатуры, важно видеть результат и в браузере, и в Telegram.
// Успех — Warning (не ошибка, но требует внимания); провал —
// Critical.
func (s *Service) notifyTestResult(ctx context.Context, res TestSMSResult) {
	if s.notifier == nil {
		return
	}
	switch {
	case res.TimedOut:
		s.notifier.Notify(ctx, notify.LevelWarning, fmt.Errorf(
			"selftest: timeout waiting for DR (status=%s, id=%d)",
			res.Status, res.ID))
	case res.Status == storage.StatusDelivered:
		s.notifier.Notify(ctx, notify.LevelOK, fmt.Errorf(
			"selftest OK: delivered (id=%d, ref=%d)",
			res.ID, res.Reference))
	case res.Status == storage.StatusFailed:
		msg := res.Error
		if msg == "" {
			msg = "unknown error"
		}
		s.notifier.Notify(ctx, notify.LevelError, fmt.Errorf(
			"selftest FAILED: %s (id=%d)", msg, res.ID))
	default:
		s.notifier.Notify(ctx, notify.LevelWarning, fmt.Errorf(
			"selftest: status=%s (id=%d)", res.Status, res.ID))
	}
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

func (s *Service) urcLoop(ctx context.Context, cancel context.CancelFunc) {
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
				s.logger.Error("service: URC-канал закрыт (модем отключен)")
				s.setFatalErr(fmt.Errorf("URC канал закрыт (модем отключен)"))
				cancel()
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
		if err := s.ingestSMS(ctx); err != nil {
			s.logger.Error("service: ingestSMS", "err", err)
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
		// Логируем только при изменении stat: SIM800L с
		// AT+CREG=2 присылает +CREG периодически (обновление
		// lac/cid от сети) даже без смены stat. Иначе лог
		// засоряется одной строкой каждые ~30 секунд.
		s.mu.Lock()
		changed := s.networkStat != v.Stat
		s.networkStat = v.Stat
		s.mu.Unlock()
		if changed {
			s.logger.Info("service: регистрация (URC)",
				"stat", v.Stat, "lac", v.LAC, "cid", v.CID)
		}
	case modem.URCCGREG:
		// Аналогично: логируем только при изменении stat.
		// lac/cid GPRS в /status не используются, но полезны
		// в логе при смене соты. Хранить их в Service нет
		// смысла (нет поля), поэтому логируем только факт
		// изменения stat.
		s.mu.RLock()
		prevGPRS := s.gprsNetworkStat
		s.mu.RUnlock()
		if prevGPRS != v.Stat {
			s.mu.Lock()
			s.gprsNetworkStat = v.Stat
			s.mu.Unlock()
			s.logger.Info("service: GPRS-регистрация (URC)",
				"stat", v.Stat, "lac", v.LAC, "cid", v.CID)
		}
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

func (s *Service) ingestSMS(ctx context.Context) error {
	msgs, err := s.modem.ReadSMS(ctx, modem.ReadFilter{})
	if err != nil {
		return fmt.Errorf("ReadSMS: %w", err)
	}

	// Итерируемся с конца, чтобы удаление SMS не сдвигало индексы
	// оставшихся сообщений в памяти модема (баг SIM800L).
	for i := len(msgs) - 1; i >= 0; i-- {
		target := msgs[i]
		if err := s.processIncomingSMS(ctx, &target); err != nil {
			s.logger.Error("service: processIncomingSMS", "err", err, "index", target.Index)
		}
	}
	return nil
}

func (s *Service) processIncomingSMS(ctx context.Context, target *modem.SMSMessage) error {
	var finalText, finalRawPDU string

	// Проверяем, является ли SMS частью multipart-сообщения.
	if target.Multipart != nil {
		mp := target.Multipart
		complete, fullText, err := s.store.SavePendingPart(
			ctx, int64(mp.Reference), target.Peer, mp.Total, mp.Sequence, target.Text,
		)
		if err != nil {
			return fmt.Errorf("SavePendingPart: %w", err)
		}

		if !complete {
			// Ещё не все части пришли. Удаляем из памяти модема и выходим.
			s.logger.Info("service: получена часть multipart SMS",
				"ref", mp.Reference, "seq", mp.Sequence, "total", mp.Total)
			if s.deleteAfterRead {
				_ = s.modem.DeleteSMS(ctx, target.Index)
			}
			return nil
		}

		// Все части собраны.
		s.logger.Info("service: multipart SMS собрано",
			"ref", mp.Reference, "total", mp.Total, "len", len(fullText))
		finalText = fullText
		finalRawPDU = target.RawPDU // PDU последней части
	} else {
		// Обычное одиночное SMS.
		finalText = target.Text
		finalRawPDU = target.RawPDU
	}

	// Дедупликация (проверяем по финальному тексту).
	existing, _, err := s.store.ListMessages(ctx, storage.Filter{
		Direction: storage.DirectionIn,
		Peer:      target.Peer,
		Limit:     1,
	})
	if err != nil {
		return fmt.Errorf("дедупликация: %w", err)
	}
	if len(existing) > 0 && existing[0].Text == finalText {
		if s.deleteAfterRead {
			_ = s.modem.DeleteSMS(ctx, target.Index)
		}
		return nil
	}

	m := &storage.Message{
		Direction: storage.DirectionIn,
		Status:    storage.StatusReceived,
		Peer:      target.Peer,
		Text:      finalText,
		RawPDU:    finalRawPDU,
	}
	id, err := s.store.CreateMessage(ctx, m)
	if err != nil {
		return fmt.Errorf("CreateMessage: %w", err)
	}
	s.logger.Info("service: SMS сохранено",
		"id", id, "peer", target.Peer, "len", len(finalText))

	// Экспорт в Gammu inbox (если файловый бэкенд активен).
	s.writeToGammuInbox(target, id)

	if s.deleteAfterRead {
		if err := s.modem.DeleteSMS(ctx, target.Index); err != nil {
			s.logger.Warn("service: не удалось удалить SMS из модема",
				"index", target.Index, "err", err)
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
		// При ошибке отправки собираем контекст сети,
		// чтобы было понятно, почему сеть отказала.
		creg, _ := s.modem.Exec(ctx, "AT+CREG?")
		cops, _ := s.modem.Exec(ctx, "AT+COPS?")
		csq, _ := s.modem.Exec(ctx, "AT+CSQ")

		s.logger.Warn("service: SendSMS не удалось",
			"id", msg.ID, "attempts", attempts,
			"parts_sent", len(res.References),
			"err", err,
			"creg", creg, "cops", cops, "csq", csq)
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

	if attempts > 1 {
		s.notifier.Notify(ctx, notify.LevelOK, fmt.Errorf(
			"%s: SMS to %s sent after %d attempts (refs: %v)",
			s.modemDesc, msg.Peer, attempts, refs))
	}
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

	if msg, err := s.store.GetMessage(ctx, msgID); err == nil {
		if msg.GammuSerial.Valid && s.gammu != nil {
			delivered := (tpst == 0x00)
			_, err := s.gammu.Inbox().WriteDeliveryReport(int(msg.GammuSerial.Int64), msg.Peer, delivered)
			if err != nil {
				s.logger.Warn("service: WriteDeliveryReport", "err", err, "serial", msg.GammuSerial.Int64)
			}
		}
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

// ==================== SendTestSMSWithReceive ====================

// ReceivedSummary — краткая сводка о принятом SMS (для JSON).
type ReceivedSummary struct {
	ID        int64     `json:"id"`
	Peer      string    `json:"peer"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// TestSMSWithReceiveResult — результат «отправка + приём».
type TestSMSWithReceiveResult struct {
	Marker          string
	Sent            TestSMSResult
	Received        *ReceivedSummary
	ReceiveTimedOut bool
	ReceiveError    string
}

// SendTestSMSWithReceive проверяет сеть в обе стороны:
//
//  1. Генерирует уникальный текст с маркером.
//  2. Отправляет на SELF_NUMBER и ждёт sent/delivered/sent_no_dr
//     (в пределах s.testSMSTimeout).
//  3. Опрашивает БД на входящее с тем же маркером (до receiveTimeout).
//
// Входящее ищется среди direction=in, peer=SELF_NUMBER, created_at>=start.
// Тело должно содержать маркер — это отсекает чужие SMS от других
// отправителей с тем же номером.
//
// Возвращает результат даже при частичном успехе: если исходящее
// ушло, но входящее не пришло — ReceiveTimedOut=true. Ошибка
// возвращается только при проблемах самой отправки или контекста.
func (s *Service) SendTestSMSWithReceive(ctx context.Context,
	receiveTimeout time.Duration) (TestSMSWithReceiveResult, error) {

	if s.selfNumber == "" {
		return TestSMSWithReceiveResult{}, errors.New(
			"SELF_NUMBER не задан в конфиге")
	}
	if receiveTimeout <= 0 {
		receiveTimeout = 90 * time.Second
	}

	now := time.Now().UTC()
	// Уникальный маркер: timestamp + младшие 6 цифр UnixNano.
	// При повторных запусках в одну секунду маркеры различаются.
	marker := fmt.Sprintf("smsgateway selftest %s #%06d",
		now.Format("20060102-150405"),
		now.UnixNano()%1000000)
	start := now

	sent, err := s.SendTestSMS(ctx, marker)
	if err != nil {
		return TestSMSWithReceiveResult{Marker: marker, Sent: sent}, err
	}

	res := TestSMSWithReceiveResult{Marker: marker, Sent: sent}

	// Приём имеет смысл, только если исходящее реально ушло в сеть.
	// sent_no_dr — статус sent + истёк DR-wait: SMS отправлена,
	// но DR не пришёл. Приём может работать.
	switch sent.Status {
	case storage.StatusSent, storage.StatusDelivered, storage.StatusSentNoDR:
		// продолжаем
	default:
		return res, nil
	}

	deadline := time.Now().Add(receiveTimeout)
	tick := time.NewTicker(DefaultTestSMSPollPeriod)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			res.ReceiveError = ctx.Err().Error()
			return res, ctx.Err()
		case <-tick.C:
		}

		msgs, _, err := s.store.ListMessages(ctx, storage.Filter{
			Direction: storage.DirectionIn,
			Peer:      s.selfNumber,
			Since:     &start,
			Limit:     10,
		})
		if err != nil {
			res.ReceiveError = err.Error()
			return res, nil
		}
		for _, m := range msgs {
			if strings.Contains(m.Text, marker) {
				res.Received = &ReceivedSummary{
					ID:        m.ID,
					Peer:      m.Peer,
					Text:      m.Text,
					CreatedAt: m.CreatedAt,
				}
				s.logger.Info("service: test-receive OK",
					"marker", marker,
					"sent_id", sent.ID,
					"received_id", m.ID)
				return res, nil
			}
		}
		if time.Now().After(deadline) {
			res.ReceiveTimedOut = true
			s.logger.Warn("service: test-receive timeout",
				"marker", marker, "sent_id", sent.ID,
				"timeout", receiveTimeout)
			return res, nil
		}
	}
}
