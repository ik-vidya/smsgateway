// Package service, файл gammu_adapter.go — адаптеры между
// internal/gammu и конкретными реализациями modem/storage.
//
// gammu.Manager развязан от storage и modem через интерфейсы
// Sender и OutboxStore. Здесь — тонкие адаптеры, которые
// реализуют эти интерфейсы поверх *modem.Modem и *storage.Store.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"git.vidya.lv/vidya/smsgateway/internal/gammu"
	"git.vidya.lv/vidya/smsgateway/internal/modem"
	"git.vidya.lv/vidya/smsgateway/internal/storage"
)

// ==================== Sender ====================

// ModemSender — gammu.Sender поверх *modem.Modem.
type ModemSender struct {
	modem  *modem.Modem
	logger *slog.Logger
}

// NewModemSender создаёт адаптер.
func NewModemSender(m *modem.Modem, logger *slog.Logger) *ModemSender {
	if logger == nil {
		logger = slog.Default()
	}
	return &ModemSender{modem: m, logger: logger}
}

// Send реализует gammu.Sender.
//
// Возвращает reference первой части (для multipart — первой из
// нескольких). Flash в pdu.Build из sim800ctl не поддержан,
// поэтому флаг только логируется и отправка идёт как обычная SMS.
func (s *ModemSender) Send(ctx context.Context, peer, text string,
	requestDR, flash bool) (int64, error) {

	if s.modem == nil {
		return 0, errors.New("модем не задан")
	}
	if flash {
		s.logger.Warn("gammu: flash SMS не поддерживается, "+
			"отправляем как обычное", "peer", peer)
	}

	res, err := s.modem.SendSMS(ctx, peer, text,
		modem.SendOptions{RequestDR: requestDR})
	if err != nil {
		return 0, err
	}
	if len(res.References) > 0 {
		return res.References[0], nil
	}
	if res.Reference != 0 {
		return res.Reference, nil
	}
	return 0, errors.New("модем не вернул reference")
}

var _ gammu.Sender = (*ModemSender)(nil)

// ==================== OutboxStore ====================

// StorageOutboxStore — gammu.OutboxStore поверх *storage.Store.
type StorageOutboxStore struct {
	store *storage.Store
}

// NewStorageOutboxStore создаёт адаптер.
func NewStorageOutboxStore(store *storage.Store) *StorageOutboxStore {
	return &StorageOutboxStore{store: store}
}

// CreateOutboxMessage создаёт исходящее сообщение со
// source='gammu-outbox' и status='queued'.
func (a *StorageOutboxStore) CreateOutboxMessage(ctx context.Context,
	peer, text string) (int64, error) {

	m := &storage.Message{
		Direction: storage.DirectionOut,
		Status:    storage.StatusQueued,
		Source:    storage.SourceGammuOutbox,
		Peer:      peer,
		Text:      text,
	}
	return a.store.CreateMessage(ctx, m)
}

// MarkOutboxSending: queued → sending.
func (a *StorageOutboxStore) MarkOutboxSending(ctx context.Context, id int64) error {
	return a.store.MarkSending(ctx, id)
}

// MarkOutboxSent: sending → sent, с сохранением reference.
//
// Дополнительно резервирует reference в pending_references —
// чтобы delivery report от этого сообщения можно было связать
// с messages. Регулярный sendLoop делает это в
// service.sendOne, но gammu-путь идёт другим маршрутом и без
// этого шага HandleDeliveryReport не находит сообщение по ref
// (warn "reference занят" в логе).
//
// Best-effort: если reservation не удался (например, ref уже
// занят другим сообщением, 8-битный ref — 256 вариантов),
// логируем warning и продолжаем. Сообщение уже отправлено,
// messages.reference уже установлен.
func (a *StorageOutboxStore) MarkOutboxSent(ctx context.Context, id, ref int64) error {
	if err := a.store.MarkSent(ctx, id, ref); err != nil {
		return err
	}
	const refTTL = time.Hour
	if err := a.store.ReserveReference(ctx, ref, id, refTTL); err != nil {
		slog.Default().Warn(
			"gammu: не удалось зарезервировать reference для DR",
			"ref", ref, "message_id", id, "err", err)
	}
	return nil
}

// MarkOutboxFailed: любой нетерминальный → failed.
func (a *StorageOutboxStore) MarkOutboxFailed(ctx context.Context,
	id int64, errMsg string) error {
	return a.store.MarkFailed(ctx, id, errMsg)
}

// CreateOutboxEntry создаёт запись о файле в gammu_outbox.
func (a *StorageOutboxStore) CreateOutboxEntry(ctx context.Context,
	path string, messageID int64, priority string) error {
	return a.store.CreateOutboxEntry(ctx, &storage.OutboxEntry{
		Path:      path,
		MessageID: messageID,
		Priority:  priority,
	})
}

// GetOutboxEntry возвращает запись. Транслирует
// storage.ErrOutboxEntryNotFound → gammu.ErrOutboxEntryNotFound.
func (a *StorageOutboxStore) GetOutboxEntry(ctx context.Context,
	path string) (*gammu.OutboxEntry, error) {

	e, err := a.store.GetOutboxEntry(ctx, path)
	if err != nil {
		if errors.Is(err, storage.ErrOutboxEntryNotFound) {
			return nil, gammu.ErrOutboxEntryNotFound
		}
		return nil, err
	}
	return &gammu.OutboxEntry{
		Path:      e.Path,
		MessageID: e.MessageID,
		Retries:   e.Retries,
		LastError: e.LastError,
		Priority:  e.Priority,
	}, nil
}

// UpdateOutboxFailure обновляет запись после неудачной попытки.
// Транслирует storage.ErrOutboxEntryNotFound → gammu.ErrOutboxEntryNotFound.
func (a *StorageOutboxStore) UpdateOutboxFailure(ctx context.Context,
	oldPath, newPath string, retries int,
	priority, lastError string) error {

	err := a.store.UpdateOutboxAfterFailure(ctx, oldPath, newPath,
		retries, priority, lastError)
	if errors.Is(err, storage.ErrOutboxEntryNotFound) {
		return gammu.ErrOutboxEntryNotFound
	}
	return err
}

// DeleteOutboxEntry удаляет запись. Не ошибка, если её нет.
func (a *StorageOutboxStore) DeleteOutboxEntry(ctx context.Context, path string) error {
	return a.store.DeleteOutboxEntry(ctx, path)
}

var _ gammu.OutboxStore = (*StorageOutboxStore)(nil)

// ==================== Inbox helper ====================

// multipartFromModem конвертирует modem.MultipartInfo в
// gammu.Multipart. Возвращает nil, если multipart отсутствует.
func multipartFromModem(mp *modem.MultipartInfo) *gammu.Multipart {
	if mp == nil {
		return nil
	}
	return &gammu.Multipart{
		Reference: mp.Reference,
		Total:     mp.Total,
		Sequence:  mp.Sequence,
	}
}

// writeToGammuInbox экспортирует принятое SMS в inbox-файл.
// Ошибка логируется, но не пробрасывается — БД важнее файла.
func (s *Service) writeToGammuInbox(msg *modem.SMSMessage, storageID int64) {
	if s.gammu == nil || msg == nil {
		return
	}
	_, err := s.gammu.Inbox().Write(gammu.InboxMessage{
		Peer:      msg.Peer,
		Text:      msg.Text,
		Encoding:  msg.Encoding,
		Received:  msg.Timestamp,
		Multipart: multipartFromModem(msg.Multipart),
	})
	if err != nil {
		s.logger.Warn("service: экспорт SMS в Gammu inbox",
			"err", err, "id", storageID, "peer", msg.Peer)
	}
}

// Ensure unused import isn't removed (fmt используется ниже
// при добавлении диагностики).
var _ = fmt.Sprintf
