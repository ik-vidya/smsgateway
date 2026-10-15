package modem

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ik-vidya/smsgateway/internal/profiles"
)

// newModemForPIN создаёт модем с базовой идентификацией и
// (опционально) PIN.
func newModemForPIN(t *testing.T, profile profiles.Profile, pin string) (*Modem, *MockTransport) {
	t.Helper()
	mock := NewMock()
	// Базовая идентификация.
	mock.PushResponse("AT", "OK")
	mock.PushResponse("ATE0", "OK")
	mock.PushResponse("AT+CMEE=2", "OK")
	mock.PushResponse("AT+CGMI", "SIMCOM_Ltd", "OK")
	mock.PushResponse("AT+CGMM", "SIMCOM_SIM800L", "OK")
	mock.PushResponse("AT+CGMR", "r", "OK")
	mock.PushResponse("AT+CGSN", "i", "OK")

	m, err := NewModem(mock, profile, time.Second, testLogger{t})
	if err != nil {
		t.Fatalf("NewModem: %v", err)
	}
	if pin != "" {
		m.SetPIN(pin)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, mock
}

func TestUnlockSIMReady(t *testing.T) {
	m, mock := newModemForPIN(t, profiles.Generic{}, "")
	mock.PushResponse("AT+CPIN?", "+CPIN: READY", "OK")

	if err := m.unlockSIM(context.Background()); err != nil {
		t.Fatalf("unlockSIM: %v", err)
	}

	// AT+CPIN="..." не должен был отправиться.
	written := string(mock.WrittenBytes())
	if strings.Contains(written, `AT+CPIN="`) {
		t.Errorf("AT+CPIN=\"...\" отправлен, хотя SIM уже READY")
	}
}

func TestUnlockSIMPINNoConfig(t *testing.T) {
	m, mock := newModemForPIN(t, profiles.Generic{}, "")
	mock.PushResponse("AT+CPIN?", "+CPIN: SIM PIN", "OK")

	err := m.unlockSIM(context.Background())
	if !errors.Is(err, ErrPINRequired) {
		t.Fatalf("err=%v, ожидалось ErrPINRequired", err)
	}
}

func TestUnlockSIMPINWithPIN(t *testing.T) {
	m, mock := newModemForPIN(t, profiles.Generic{}, "1234")
	mock.PushResponse("AT+CPIN?", "+CPIN: SIM PIN", "OK")
	mock.PushResponse(`AT+CPIN="1234"`, "OK")
	// После ввода PIN — READY.
	mock.PushResponse("AT+CPIN?", "+CPIN: READY", "OK")

	if err := m.unlockSIM(context.Background()); err != nil {
		t.Fatalf("unlockSIM: %v", err)
	}

	written := string(mock.WrittenBytes())
	if !strings.Contains(written, `AT+CPIN="1234"`) {
		t.Errorf("AT+CPIN=\"1234\" не отправлен")
	}
}

func TestUnlockSIMPUK(t *testing.T) {
	m, mock := newModemForPIN(t, profiles.Generic{}, "1234")
	mock.PushResponse("AT+CPIN?", "+CPIN: SIM PUK", "OK")

	err := m.unlockSIM(context.Background())
	if !errors.Is(err, ErrSIMLockedPUK) {
		t.Fatalf("err=%v, ожидалось ErrSIMLockedPUK", err)
	}
}

func TestUnlockSIMNotInserted(t *testing.T) {
	m, mock := newModemForPIN(t, profiles.Generic{}, "")
	mock.PushResponse("AT+CPIN?", "+CME ERROR: 10", "ERROR")

	// С CME ERROR: 10 от Exec придёт ошибка, не пустой ответ.
	err := m.unlockSIM(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	// Ошибка будет из Exec, а не наша ErrSIMNotInserted. Это OK.
}

func TestUnlockSIMPinRejected(t *testing.T) {
	m, mock := newModemForPIN(t, profiles.Generic{}, "9999")
	mock.PushResponse("AT+CPIN?", "+CPIN: SIM PIN", "OK")
	mock.PushResponse(`AT+CPIN="9999"`, "+CME ERROR: 16", "ERROR")

	err := m.unlockSIM(context.Background())
	if !errors.Is(err, ErrPINRejected) {
		t.Fatalf("err=%v, ожидалось ErrPINRejected", err)
	}
}

// TestUnlockSIMLastAttempt: профиль SIM800, AT+SPIC возвращает 1 —
// программа отказывается вводить PIN.
func TestUnlockSIMLastAttempt(t *testing.T) {
	m, mock := newModemForPIN(t, profiles.SIM800{}, "1234")
	mock.PushResponse("AT+CPIN?", "+CPIN: SIM PIN", "OK")
	mock.PushResponse("AT+SPIC", "+SPIC: 1,3,10,10", "OK")

	err := m.unlockSIM(context.Background())
	if !errors.Is(err, ErrPINLastAttempt) {
		t.Fatalf("err=%v, ожидалось ErrPINLastAttempt", err)
	}

	// AT+CPIN="..." не должен отправляться.
	written := string(mock.WrittenBytes())
	if strings.Contains(written, `AT+CPIN="1234"`) {
		t.Errorf("PIN отправлен, хотя попытка последняя")
	}
}

// TestUnlockSIMSPICShows3: попыток 3 — PIN отправляется.
func TestUnlockSIMSPICShows3(t *testing.T) {
	m, mock := newModemForPIN(t, profiles.SIM800{}, "1234")
	mock.PushResponse("AT+CPIN?", "+CPIN: SIM PIN", "OK")
	mock.PushResponse("AT+SPIC", "+SPIC: 3,3,10,10", "OK")
	mock.PushResponse(`AT+CPIN="1234"`, "OK")
	mock.PushResponse("AT+CPIN?", "+CPIN: READY", "OK")

	if err := m.unlockSIM(context.Background()); err != nil {
		t.Fatalf("unlockSIM: %v", err)
	}
}

// TestMaskPINCommand: маскирование в логах.
func TestMaskPINCommand(t *testing.T) {
	cases := map[string]string{
		`AT+CPIN="1234"`:     `AT+CPIN="****"`,
		`AT+CPIN?`:           `AT+CPIN?`,
		`AT+CMGF=0`:          `AT+CMGF=0`,
		`AT+CPIN="12345678"`: `AT+CPIN="****"`,
	}
	for in, want := range cases {
		if got := maskPINCommand(in); got != want {
			t.Errorf("maskPINCommand(%q)=%q, ожидалось %q", in, got, want)
		}
	}
}
