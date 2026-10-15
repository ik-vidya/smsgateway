// Package modem, файл pin.go — разблокировка SIM-карты по PIN.
//
// Логика:
//  1. Прочитать AT+CPIN?.
//  2. Если READY — SIM уже разблокирована.
//  3. Если SIM PIN — нужен PIN.
//     - без PIN → ErrPINRequired («задайте MODEM_PIN»)
//     - с PIN → проверить оставшиеся попытки (AT+SPIC),
//     если <= 1 — ErrPINLastAttempt
//     - отправить AT+CPIN="<pin>", ждать READY
//  4. Если SIM PUK — ErrSIMLockedPUK (нужен PUK).
//  5. Если NOT INSERTED — ErrSIMNotInserted.
package modem

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrPINRequired — SIM требует PIN, но он не задан в конфиге.
	ErrPINRequired = errors.New("SIM требует PIN, задайте MODEM_PIN")
	// ErrPINRejected — модем отклонил PIN.
	ErrPINRejected = errors.New("модем отклонил PIN")
	// ErrSIMLockedPUK — SIM заблокирована, нужен PUK.
	ErrSIMLockedPUK = errors.New("SIM заблокирована и требует PUK; разблокируйте её в телефоне")
	// ErrPINLastAttempt — осталась последняя попытка ввода PIN.
	ErrPINLastAttempt = errors.New("осталась последняя попытка ввода PIN; введите PIN в телефоне")
	// ErrSIMNotInserted — SIM не вставлена.
	ErrSIMNotInserted = errors.New("SIM не вставлена")
)

// reSPIC разбирает "+SPIC: 3,3,10,10" (PIN1, PIN2, PUK1, PUK2).
var reSPIC = regexp.MustCompile(`\+SPIC:\s*(\d+)\s*,`)

// SetPIN сохраняет PIN SIM-карты. Пустая строка = PIN не требуется.
func (m *Modem) SetPIN(pin string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pin = pin
}

// getPIN возвращает текущий PIN (для внутреннего использования).
func (m *Modem) getPIN() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pin
}

// unlockSIM проверяет статус SIM и, если нужно, вводит PIN.
func (m *Modem) unlockSIM(ctx context.Context) error {
	resp, err := m.Exec(ctx, "AT+CPIN?")
	if err != nil {
		return fmt.Errorf("AT+CPIN?: %w", err)
	}

	switch {
	case strings.Contains(resp, "READY"):
		return nil
	case strings.Contains(resp, "SIM PIN"):
		// продолжаем ниже
	case strings.Contains(resp, "SIM PUK"):
		return ErrSIMLockedPUK
	case strings.Contains(resp, "NOT INSERTED"):
		return ErrSIMNotInserted
	case strings.Contains(resp, "NOT READY"):
		return fmt.Errorf("SIM не готова (NOT READY)")
	default:
		return fmt.Errorf("неожиданный статус SIM: %q", resp)
	}

	pin := m.getPIN()
	if pin == "" {
		return ErrPINRequired
	}

	// Проверка оставшихся попыток — если профиль поддерживает.
	if at := m.pinAttemptsAT(); at != "" {
		left := m.readPINAttempts(ctx, at)
		if left == 1 {
			return ErrPINLastAttempt
		}
		if left >= 0 {
			m.logger.Info("modem: попыток ввода PIN", "left", left)
		}
	}

	m.logger.Info("modem: разблокировка SIM по PIN")
	if _, err := m.Exec(ctx, fmt.Sprintf(`AT+CPIN="%s"`, pin)); err != nil {
		return fmt.Errorf("%w: %v", ErrPINRejected, err)
	}

	// Ждём READY до 15 секунд.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		resp, err := m.Exec(ctx, "AT+CPIN?")
		if err != nil {
			continue
		}
		if strings.Contains(resp, "READY") {
			m.logger.Info("modem: PIN принят, SIM готова")
			return nil
		}
	}
	return fmt.Errorf("SIM не перешла в READY после ввода PIN")
}

// pinAttemptsAT возвращает команду для чтения оставшихся попыток
// PIN, если профиль её знает. Иначе пусто.
func (m *Modem) pinAttemptsAT() string {
	if m.profile == nil {
		return ""
	}
	return m.profile.Workarounds().PinAttemptsAT
}

// readPINAttempts читает число попыток PIN через AT+SPIC.
// Возвращает -1, если не удалось.
func (m *Modem) readPINAttempts(ctx context.Context, cmd string) int {
	resp, err := m.Exec(ctx, cmd)
	if err != nil {
		return -1
	}
	mm := reSPIC.FindStringSubmatch(resp)
	if len(mm) < 2 {
		return -1
	}
	n, err := strconv.Atoi(mm[1])
	if err != nil {
		return -1
	}
	return n
}
