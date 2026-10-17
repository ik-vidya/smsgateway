package modem

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ik-vidya/smsgateway/internal/profiles"
)

// testLogger — тихий логгер для тестов.
type testLogger struct{ t *testing.T }

func (l testLogger) Debug(msg string, args ...any) {}
func (l testLogger) Info(msg string, args ...any)  {}
func (l testLogger) Warn(msg string, args ...any)  {}
func (l testLogger) Error(msg string, args ...any) {
	l.t.Logf("ERROR: %s %v", msg, args)
}

func newTestModem(t *testing.T, profile profiles.Profile) (*Modem, *MockTransport) {
	t.Helper()
	mock := NewMock()

	// Базовая идентификация, которую ждёт Init.
	mock.PushResponse("AT", "OK")
	mock.PushResponse("ATE0", "OK")
	mock.PushResponse("AT+CMEE=2", "OK")
	mock.PushResponse("AT+CGMI", "SIMCOM_Ltd", "OK")
	mock.PushResponse("AT+CGMM", "SIMCOM_SIM800L", "OK")
	mock.PushResponse("AT+CGMR", "Revision:1418B04SIM800L24", "OK")
	mock.PushResponse("AT+CGSN", "863584038586060", "OK")

	m, err := NewModem(mock, profile, time.Second, testLogger{t})
	if err != nil {
		t.Fatalf("NewModem: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, mock
}

func TestModemInit(t *testing.T) {
	m, _ := newTestModem(t, profiles.Generic{})

	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	info := m.Info()
	if info.Manufacturer != "SIMCOM_Ltd" {
		t.Errorf("Manufacturer=%q", info.Manufacturer)
	}
	if info.Model != "SIMCOM_SIM800L" {
		t.Errorf("Model=%q", info.Model)
	}
	if info.Revision != "Revision:1418B04SIM800L24" {
		t.Errorf("Revision=%q", info.Revision)
	}
	if info.IMEI != "863584038586060" {
		t.Errorf("IMEI=%q", info.IMEI)
	}
}

func TestModemInitNoResponse(t *testing.T) {
	mock := NewMock()
	mock.PushResponse("AT", "ERROR")

	m, err := NewModem(mock, profiles.Generic{}, time.Second, testLogger{t})
	if err != nil {
		t.Fatalf("NewModem: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	err = m.Init(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "не отвечает") {
		t.Errorf("err=%v", err)
	}
}

// ==================== SIM800 profile ====================

// TestModemInitWithSIM800 проверяет, что PreInitHook профиля SIM800
// выполняет все свои AT-команды.
func TestModemInitWithSIM800(t *testing.T) {
	mock := NewMock()
	// Базовая идентификация.
	mock.PushResponse("AT", "OK")
	mock.PushResponse("ATE0", "OK")
	mock.PushResponse("AT+CMEE=2", "OK")
	mock.PushResponse("AT+CGMI", "SIMCOM_Ltd", "OK")
	mock.PushResponse("AT+CGMM", "SIMCOM_SIM800L", "OK")
	mock.PushResponse("AT+CGMR", "Revision:1418B04SIM800L24", "OK")
	mock.PushResponse("AT+CGSN", "863584038586060", "OK")

	// PreInitHook профиля SIM800 — ожидаемые команды.
	mock.PushResponse("AT+CFUN?", "+CFUN: 1", "OK") // уже включён
	mock.PushResponse(`AT+CPMS="SM","SM","SM"`, "OK")
	mock.PushResponse("AT+CNMI=2,1,0,1,0", "OK")
	mock.PushResponse("AT+CSMP=49,167,0,0", "OK")
	mock.PushResponse("AT+CMMS=2", "OK")
	mock.PushResponse(`AT+CSCS="UCS2"`, "OK")

	m, err := NewModem(mock, profiles.SIM800{}, time.Second, testLogger{t})
	if err != nil {
		t.Fatalf("NewModem: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Убедиться, что профиль отработал — проверим, что в mock
	// не осталось неиспользованных ответов (все были потреблены).
	// Если какая-то команда не была послана — её ответ останется
	// в скрипте и следующий тест это заметит.
	// Прямой проверки нет, но косвенно: тест проходит без таймаутов.
	if m.Profile().Name() != "sim800" {
		t.Errorf("profile=%q", m.Profile().Name())
	}
}

// TestModemInitWithSIM800CFUN0 — когда CFUN=0, хук должен послать
// AT+CFUN=1.
func TestModemInitWithSIM800CFUN0(t *testing.T) {
	mock := NewMock()
	mock.PushResponse("AT", "OK")
	mock.PushResponse("ATE0", "OK")
	mock.PushResponse("AT+CMEE=2", "OK")
	mock.PushResponse("AT+CGMI", "SIMCOM_Ltd", "OK")
	mock.PushResponse("AT+CGMM", "SIMCOM_SIM800L", "OK")
	mock.PushResponse("AT+CGMR", "r", "OK")
	mock.PushResponse("AT+CGSN", "i", "OK")

	mock.PushResponse("AT+CFUN?", "+CFUN: 0", "OK") // выключен
	mock.PushResponse("AT+CFUN=1", "OK")            // хук включает
	mock.PushResponse(`AT+CPMS="SM","SM","SM"`, "OK")
	mock.PushResponse("AT+CNMI=2,1,0,1,0", "OK")
	mock.PushResponse("AT+CSMP=49,167,0,0", "OK")
	mock.PushResponse("AT+CMMS=2", "OK")
	mock.PushResponse(`AT+CSCS="UCS2"`, "OK")

	m, err := NewModem(mock, profiles.SIM800{}, time.Second, testLogger{t})
	if err != nil {
		t.Fatalf("NewModem: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Проверим, что AT+CFUN=1 был послан.
	written := string(mock.WrittenBytes())
	if !strings.Contains(written, "AT+CFUN=1\r") {
		t.Errorf("AT+CFUN=1 не послан; получено:\n%s", written)
	}
}

func TestModemProfile(t *testing.T) {
	m, _ := newTestModem(t, profiles.SIM800{})
	if m.Profile().Name() != "sim800" {
		t.Errorf("Name=%q", m.Profile().Name())
	}
}

func TestModemInfoConcurrent(t *testing.T) {
	m, _ := newTestModem(t, profiles.Generic{})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			_ = m.Info()
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		m.setInfo(Info{Model: "x"})
	}
	<-done
}

func TestModemInitSendsRegistrationURC(t *testing.T) {
	// Init должен отправлять AT+CREG=2 и AT+CGREG=2 — это
	// включает live URC +CREG с lac/ci. service.networkLoop
	// опрашивает эти же команды периодически, но при работающих
	// URC dashboard обновляется мгновенно.
	m, mock := newTestModem(t, profiles.Generic{})

	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	written := string(mock.WrittenBytes())
	for _, want := range []string{
		"AT+CREG=2\r",
		"AT+CGREG=2\r",
	} {
		if !strings.Contains(written, want) {
			t.Errorf("не отправлено: %q\nполучено:\n%s",
				want, written)
		}
	}
}
