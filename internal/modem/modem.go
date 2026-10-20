// Package modem, файл modem.go — высокоуровневая обёртка над
// ATTransport.
//
// Modem даёт API уровня предметной области:
//   - Init(ctx)  — инициализация: идентификация, профиль, хуки.
//   - Info()     — данные о модеме (производитель, модель, IMEI).
//   - Exec(ctx, cmd) — прямой доступ к AT-команде (реализует
//     интерфейс profiles.ModemHook).
//   - Close()    — остановка транспорта.
//
// Send/Read/Delete добавятся в части 2б.
package modem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"git.vidya.lv/vidya/smsgateway/internal/profiles"
)

// Info — идентификация модема.
type Info struct {
	Manufacturer string // AT+CGMI
	Model        string // AT+CGMM
	Revision     string // AT+CGMR
	IMEI         string // AT+CGSN
}

// Modem — высокоуровневая обёртка над ATTransport.
type Modem struct {
	transport *ATTransport
	profile   profiles.Profile
	logger    Logger

	mu   sync.RWMutex
	pin  string
	info Info
}

// NewModem создаёт Modem поверх готового транспорта.
//
// Профиль определяет специфичные для модема команды и обходы
// (например, SIM800Profile включает AT+CNMI и AT+CSMP).
//
// CMGSTimeout равен CmdTimeout (для обратной совместимости).
// Для отдельного таймаута AT+CMGS используйте NewModemWithCMGS.
func NewModem(t Transport, profile profiles.Profile, cmdTimeout time.Duration, logger Logger) (*Modem, error) {
	return NewModemWithCMGS(t, profile, cmdTimeout, 0, logger)
}

// NewModemWithCMGS создаёт Modem с отдельным таймаутом для
// AT+CMGS.
//
// cmgsTimeout — таймаут AT+CMGS (prompt `> ` и финальный
// +CMGS/OK). 0 — 60s. Рекомендуется 60s для SIM800L: при
// занятой сети модем может отвечать медленно, и короткий
// таймаут приводит к отмене висящей CMGS и повторной отправке.
func NewModemWithCMGS(t Transport, profile profiles.Profile,
	cmdTimeout, cmgsTimeout time.Duration, logger Logger,
) (*Modem, error) {
	if profile == nil {
		profile = profiles.Generic{}
	}
	at, err := New(t, Config{
		CmdTimeout:  cmdTimeout,
		CMGSTimeout: cmgsTimeout,
		Profile:     profile,
		Logger:      logger,
	})
	if err != nil {
		return nil, err
	}
	return &Modem{
		transport: at,
		profile:   profile,
		logger:    logger,
	}, nil
}

// Init выполняет инициализацию модема:
//  1. Проверяет связь (AT).
//  2. Отключает эхо (ATE0), включает подробные ошибки (AT+CMEE=2).
//  3. Читает идентификацию (CGMI, CGMM, CGMR, CGSN).
//  4. Вызывает profile.PreInitHook — специфичные команды.
//
// Безопасен для повторного вызова.
func (m *Modem) Init(ctx context.Context) error {
	m.logger.Info("modem: инициализация")

	// 1. AT — базовая проверка связи.
	if _, err := m.Exec(ctx, "AT"); err != nil {
		return fmt.Errorf("модем не отвечает на AT: %w", err)
	}

	// 2. Echo off и verbose errors. Ошибки этих команд некритичны:
	// некоторые модемы могут не поддерживать CMEE=2.
	_, _ = m.transport.Exec(ctx, "ATE0")
	_, _ = m.transport.Exec(ctx, "AT+CMEE=2")

	// 2a. Расширенные URC регистрации. AT+CREG=2 включает live
	// URC +CREG с lac/ci (вместо голого stat), AT+CGREG=2 — для
	// GPRS-регистрации. service.networkLoop опрашивает эти же
	// команды периодически как fallback (раз в 30 секунд), но
	// при работающих URC dashboard обновляется мгновенно: в
	// handleURC уже есть обработка modem.URCCREG.
	//
	// Некоторые модемы не поддерживают расширенный режим —
	// ошибки игнорируются, чтобы не блокировать инициализацию.
	_, _ = m.transport.Exec(ctx, "AT+CREG=2")
	_, _ = m.transport.Exec(ctx, "AT+CGREG=2")

	// 3. Идентификация.
	info := Info{}
	if resp, err := m.transport.Exec(ctx, "AT+CGMI"); err == nil {
		info.Manufacturer = firstPayload(resp)
	}
	if resp, err := m.transport.Exec(ctx, "AT+CGMM"); err == nil {
		info.Model = firstPayload(resp)
	}
	if resp, err := m.transport.Exec(ctx, "AT+CGMR"); err == nil {
		info.Revision = firstPayload(resp)
	}
	if resp, err := m.transport.Exec(ctx, "AT+CGSN"); err == nil {
		info.IMEI = firstPayload(resp)
	}
	m.setInfo(info)

	// 5. Проверка PIN SIM-карты.
	if err := m.unlockSIM(ctx); err != nil {
		return fmt.Errorf("разблокировка SIM: %w", err)
	}

	m.logger.Info("modem: идентификация",
		"manufacturer", info.Manufacturer,
		"model", info.Model,
		"revision", info.Revision,
		"imei", info.IMEI,
	)

	// 4. Профиль — специфичные команды.
	// Реализуем profiles.ModemHook: Modem.Exec(ctx, cmd) (string, error).
	if err := m.profile.PreInitHook(ctx, m); err != nil {
		m.logger.Warn("modem: PreInitHook профиля вернул ошибку",
			"profile", m.profile.Name(),
			"err", err)
	}

	m.logger.Info("modem: инициализация завершена",
		"profile", m.profile.Name())
	return nil
}

// Info возвращает последние полученные данные о модеме.
func (m *Modem) Info() Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.info
}

func (m *Modem) setInfo(info Info) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.info = info
}

// Profile возвращает активный профиль.
func (m *Modem) Profile() profiles.Profile {
	return m.profile
}

// URCs возвращает канал URC. Пробрасывается напрямую из транспорта.
func (m *Modem) URCs() <-chan URC {
	return m.transport.URCs()
}

// Close останавливает транспорт.
func (m *Modem) Close() error {
	m.logger.Info("modem: остановка")
	return m.transport.Close()
}

// ==================== profiles.ModemHook ====================

// Exec выполняет AT-команду и возвращает её payload (строки ответа
// без терминатора). Реализует интерфейс profiles.ModemHook, чтобы
// профиль мог вызывать команды через Modem.
//
// Если модем ответил ERROR или таймаут — возвращает ошибку.
// Payload при этом всё равно возвращается (может быть полезно
// для диагностики).
func (m *Modem) Exec(ctx context.Context, cmd string) (string, error) {
	resp, err := m.transport.Exec(ctx, cmd)
	return resp.String(), err
}

// Log пишет сообщение от имени профиля. Реализует profiles.ModemHook.
func (m *Modem) Log(msg string) {
	m.logger.Info("modem: " + msg)
}

// ==================== вспомогательные ====================

// firstPayload возвращает первую непустую строку из payload ответа.
// Для команд вида AT+CGMI ответ — одна строка "SIMCOM_Ltd".
func firstPayload(resp Response) string {
	for _, line := range resp.Payload() {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// ErrNotInitialized — модем не был инициализирован.
var ErrNotInitialized = errors.New("модем не инициализирован")
