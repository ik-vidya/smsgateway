package modem

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.vidya.lv/vidya/smsgateway/internal/profiles"
)

func TestParseCPMS(t *testing.T) {
	cases := []struct {
		in   string
		want cpmsState
	}{
		{
			`+CPMS: "ME",0,50,"ME",0,50,"SM",0,25`,
			cpmsState{Read: "ME", Write: "ME", Receive: "SM"},
		},
		{
			`+CPMS: "SM",3,25,"SM",3,25,"SM",3,25`,
			cpmsState{Read: "SM", Write: "SM", Receive: "SM"},
		},
		{
			`+CPMS: "ME",0,50,"SM",0,25,"ME",0,50`,
			cpmsState{Read: "ME", Write: "SM", Receive: "ME"},
		},
		{
			`garbage`,
			cpmsState{},
		},
	}
	for _, c := range cases {
		got := parseCPMS(c.in)
		if got != c.want {
			t.Errorf("parseCPMS(%q)=%+v, ожидалось %+v", c.in, got, c.want)
		}
	}
}

// TestMaybeAlignStorageSIM800: профиль SIM800 требует
// AlignReadStorage. Модем отдаёт расклад "read=ME, receive=SM".
// Ожидаем команду AT+CPMS="SM".
func TestMaybeAlignStorageSIM800(t *testing.T) {
	mock := NewMock()
	mock.PushResponse("AT", "OK")
	mock.PushResponse("ATE0", "OK")
	mock.PushResponse("AT+CMEE=2", "OK")
	mock.PushResponse("AT+CGMI", "SIMCOM_Ltd", "OK")
	mock.PushResponse("AT+CGMM", "SIMCOM_SIM800L", "OK")
	mock.PushResponse("AT+CGMR", "r", "OK")
	mock.PushResponse("AT+CGSN", "i", "OK")

	// Профиль SIM800 в PreInitHook сам вызывает AT+CFUN?, CPMS=..., CNMI, CSMP, CMMS, CSCS.
	mock.PushResponse("AT+CFUN?", "+CFUN: 1", "OK")
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

	// Сбросим записи, чтобы видеть только то, что пойдёт дальше.
	mock.ResetWritten()

	// Готовим ответы на вызов maybeAlignStorage:
	//   AT+CPMS? → read=ME, receive=SM
	//   AT+CPMS="SM" → OK
	mock.PushResponse("AT+CPMS?", `+CPMS: "ME",0,50,"ME",0,50,"SM",0,25`, "OK")
	mock.PushResponse(`AT+CPMS="SM"`, "OK")

	if err := m.maybeAlignStorage(context.Background()); err != nil {
		t.Fatalf("maybeAlignStorage: %v", err)
	}

	written := string(mock.WrittenBytes())
	if !strings.Contains(written, `AT+CPMS="SM"`) {
		t.Errorf("AT+CPMS=\"SM\" не отправлен; получено:\n%s", written)
	}
}

// TestMaybeAlignStorageGeneric: у generic профиля
// AlignReadStorage=false — команды не должно быть.
func TestMaybeAlignStorageGeneric(t *testing.T) {
	m, mock := newTestModem(t, profiles.Generic{})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	mock.ResetWritten()

	// Никаких ответов не готовим: если maybeAlignStorage попытается
	// послать AT+CPMS?, тест упадёт по таймауту.
	if err := m.maybeAlignStorage(context.Background()); err != nil {
		t.Fatalf("maybeAlignStorage: %v", err)
	}

	written := string(mock.WrittenBytes())
	if strings.Contains(written, "AT+CPMS?") {
		t.Errorf("AT+CPMS? отправлен, хотя generic не требует выравнивания")
	}
}

// TestReadSMSAlignsStorage: ReadSMS на профиле SIM800 должен
// послать AT+CPMS? и, при необходимости, AT+CPMS="SM" перед AT+CMGL=4.
func TestReadSMSAlignsStorage(t *testing.T) {
	mock := NewMock()
	// Идентификация + PreInitHook.
	mock.PushResponse("AT", "OK")
	mock.PushResponse("ATE0", "OK")
	mock.PushResponse("AT+CMEE=2", "OK")
	mock.PushResponse("AT+CGMI", "SIMCOM_Ltd", "OK")
	mock.PushResponse("AT+CGMM", "SIMCOM_SIM800L", "OK")
	mock.PushResponse("AT+CGMR", "r", "OK")
	mock.PushResponse("AT+CGSN", "i", "OK")
	mock.PushResponse("AT+CFUN?", "+CFUN: 1", "OK")
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

	// Готовим ответы на ReadSMS.
	mock.PushResponse("AT+CMGF=0", "OK")
	mock.PushResponse("AT+CPMS?", `+CPMS: "ME",0,50,"ME",0,50,"SM",0,25`, "OK")
	mock.PushResponse(`AT+CPMS="SM"`, "OK")
	mock.PushResponse("AT+CMGL=4", "OK")

	_, err = m.ReadSMS(context.Background(), ReadFilter{})
	if err != nil {
		t.Fatalf("ReadSMS: %v", err)
	}

	written := string(mock.WrittenBytes())
	if !strings.Contains(written, `AT+CPMS="SM"`) {
		t.Errorf("AT+CPMS=\"SM\" не отправлен перед AT+CMGL")
	}
	if !strings.Contains(written, "AT+CMGL=4") {
		t.Errorf("AT+CMGL=4 не отправлен")
	}
}
