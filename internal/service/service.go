// Package service — сервисный слой smsgateway.
//
// Связывает:
//   - modem (AT-транспорт и высокоуровневые операции);
//   - storage (SQLite);
//   - API (который кладёт сообщения в очередь через storage).
//
// Запускает фоновые горутины:
//   - URC-цикл: реакция на +CMTI (новая SMS), +CDS (delivery report),
//     +CREG/+CGREG (состояние сети), RDY (перезапуск модема).
//   - Отправка: выборка queued-сообщений, отправка через SendSMS.
//   - Просрочка pending_references: перевод в sent_no_dr.
package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ik-vidya/smsgateway/internal/modem"
	"github.com/ik-vidya/smsgateway/internal/storage"
)

// Service — координатор.
type Service struct {
	modem  Modem
	store  *storage.Store
	logger *slog.Logger

	// Настройки.
	sendInterval    time.Duration
	pendingInterval time.Duration
	refTTL          time.Duration
	deleteAfterRead bool

	// Состояние.
	mu          sync.RWMutex
	networkStat int
	lastCSQ     int
	startTime   time.Time

	wg sync.WaitGroup
}

// Modem — интерфейс модема, используемый сервисом.
// Позволяет подменить на mock в тестах.
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
	// SendInterval — как часто проверять очередь queued.
	// 0 — 1 секунда.
	SendInterval time.Duration

	// PendingInterval — как часто проверять просроченные
	// pending_references. 0 — 30 секунд.
	PendingInterval time.Duration

	// ReferenceTTL — время, через которое неподтверждённый
	// reference считается просроченным. 0 — 1 час.
	ReferenceTTL time.Duration

	// DeleteAfterRead — удалять ли SMS из памяти модема после
	// сохранения в БД. true по умолчанию.
	DeleteAfterRead bool
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
	return c
}

// New создаёт сервис.
func New(m Modem, store *storage.Store, logger *slog.Logger, cfg Config) *Service {
	cfg = cfg.withDefaults()
	return &Service{
		modem:           m,
		store:           store,
		logger:          logger,
		sendInterval:    cfg.SendInterval,
		pendingInterval: cfg.PendingInterval,
		refTTL:          cfg.ReferenceTTL,
		deleteAfterRead: cfg.DeleteAfterRead,
		startTime:       time.Now(),
	}
}

// Run запускает сервис. Блокируется до отмены ctx.
//
// Порядок:
//  1. Init модема.
//  2. Запуск горутин: URC-цикл, отправка, pending-цикл.
//  3. Ожидание ctx.Done() или фатальной ошибки.
func (s *Service) Run(ctx context.Context) error {
	s.logger.Info("service: запуск")

	if err := s.modem.Init(ctx); err != nil {
		return fmt.Errorf("инициализация модема: %w", err)
	}

	s.wg.Add(3)
	go s.urcLoop(ctx)
	go s.sendLoop(ctx)
	go s.pendingLoop(ctx)

	<-ctx.Done()
	s.logger.Info("service: остановка, ждём горутины")

	// Ждём завершения горутин с таймаутом.
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

// Uptime — время с момента старта.
func (s *Service) Uptime() time.Duration {
	return time.Since(s.startTime)
}

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
	    // Не удалось получить PDU — не можем сопоставить.
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

// ingestSMS читает SMS по индексу из памяти модема, сохраняет в БД
// и, при настройке, удаляет из памяти.
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

	// Дедупликация: есть ли уже такое сообщение в БД?
	// Проверяем по peer + text (упрощённо).
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

	// Сохранить в БД.
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
		"interval", s.sendInterval)

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

// processQueue обрабатывает одно сообщение из очереди за раз.
// Обработка по одному выбрана сознательно: последовательная
// отправка упрощает бронирование reference и снижает нагрузку
// на модем.
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

	// 1. Пометить sending.
	if err := s.store.MarkSending(ctx, msg.ID); err != nil {
		s.logger.Error("service: MarkSending", "err", err, "id", msg.ID)
		return
	}

	// 2. Отправить.
	res, err := s.modem.SendSMS(ctx, msg.Peer, msg.Text,
		modem.SendOptions{RequestDR: true})
	if err != nil {
		s.logger.Warn("service: SendSMS не удалось",
			"id", msg.ID, "err", err)
		_ = s.store.MarkFailed(ctx, msg.ID, err.Error())
		return
	}

	// 3. Забронировать reference.
	if err := s.store.ReserveReference(ctx,
		res.Reference, msg.ID, s.refTTL); err != nil {
		s.logger.Warn("service: не удалось забронировать reference",
			"ref", res.Reference, "id", msg.ID, "err", err)
		// Не критично — просто не будет сопоставления DR.
	}

	// 4. Пометить sent.
	if err := s.store.MarkSent(ctx, msg.ID, res.Reference); err != nil {
		s.logger.Error("service: MarkSent", "err", err, "id", msg.ID)
		return
	}
	s.logger.Info("service: SMS отправлено",
		"id", msg.ID, "ref", res.Reference)
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

// processExpiredReferences переводит сообщения, для которых DR так
// и не пришёл, в статус sent_no_dr.
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

// ==================== Обработка delivery report ====================

// HandleDeliveryReport — вызывается извне (или из URC-цикла),
// когда из модема пришёл полный PDU отчёта о доставке.
//
// В текущей реализации URC +CDS приходит как две строки (header и
// PDU), которые читаются последовательно. Пока метод предоставляется
// как API для будущей интеграции.
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

// nullInt64 оборачивает int64 в sql.NullInt64 с Valid=true.
func nullInt64(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: true}
}

// ModemInfo возвращает информацию о модеме (или пустую Info,
// если модем не сконфигурирован).
func (s *Service) ModemInfo() modem.Info {
    if s.modem == nil {
	return modem.Info{}
    }
    return s.modem.Info()
}
