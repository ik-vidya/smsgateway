package modem

import (
	"context"
	"strings"
	"testing"

	"github.com/ik-vidya/smsgateway/internal/profiles"
)

// ==================== Send ====================

func TestSendSMS(t *testing.T) {
	m, mock := newTestModem(t, profiles.Generic{})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	mock.PushResponse("AT+CMGF=0", "OK")
	// Unsolicited: отдастся на первый Write без match.
	// Последовательность для ExecPrompt: prompt, +CMGS, OK.
	mock.PushResponse("", "> ", "+CMGS: 42", "OK")

	res, err := m.SendSMS(context.Background(),
		"+37121234567", "Test", SendOptions{})
	if err != nil {
		t.Fatalf("SendSMS: %v", err)
	}
	if res.Reference != 42 {
		t.Errorf("reference=%d, ожидалось 42", res.Reference)
	}
	if res.RawPDU == "" {
		t.Error("RawPDU пуст")
	}
	if res.TPDULength == 0 {
		t.Error("TPDULength=0")
	}

	// Проверим, что в порт действительно ушли AT+CMGS и \x1a.
	written := string(mock.WrittenBytes())
	if !strings.Contains(written, "AT+CMGS=") {
		t.Error("AT+CMGS не отправлен")
	}
	if !strings.Contains(written, "\x1a") {
		t.Error("Ctrl-Z не отправлен")
	}
	if !strings.Contains(written, res.RawPDU) {
		t.Error("PDU не найден в записях")
	}
}

func TestSendSMSWithRequestDR(t *testing.T) {
	m, mock := newTestModem(t, profiles.Generic{})
	_ = m.Init(context.Background())

	mock.PushResponse("AT+CMGF=0", "OK")
	mock.PushResponse("", "> ", "+CMGS: 1", "OK")

	res, err := m.SendSMS(context.Background(),
		"+37121234567", "Test", SendOptions{RequestDR: true})
	if err != nil {
		t.Fatalf("SendSMS: %v", err)
	}
	// Первый октет после SMSC должен иметь SRR=1 (0x31 или 0x71).
	// PDU начинается с "00" (SMSC), потом первый октет.
	if len(res.RawPDU) < 4 {
		t.Fatalf("PDU слишком короткий: %q", res.RawPDU)
	}
	firstOctet := res.RawPDU[2:4]
	if firstOctet != "31" && firstOctet != "71" {
		t.Errorf("первый октет=%q, ожидалось 31 или 71 (SRR=1)", firstOctet)
	}
}

func TestSendSMSError(t *testing.T) {
	m, mock := newTestModem(t, profiles.Generic{})
	_ = m.Init(context.Background())

	mock.PushResponse("AT+CMGF=0", "OK")
	mock.PushResponse("", "+CMS ERROR: 500")

	_, err := m.SendSMS(context.Background(),
		"+37121234567", "Test", SendOptions{})
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "нет баланса") {
		t.Errorf("не расшифровано: %v", err)
	}
}

// ==================== Read ====================

// Тестовый PDU SMS-DELIVER:
//
//	SMSC:  00 (не указан)
//	First: 04 (SMS-DELIVER, MTI=0)
//	OA:    0B 91 73 52 65 19 99 F9  (+37255691999)
//	PID:   00
//	DCS:   08 (UCS-2)
//	SCTS:  62 10 11 11 11 11 00  (26-10-11 11:11:11 +00)
//	UDL:   0A (10 байт)
//	UD:    041F0440043804320435  ("Приве")
const testPDUDeliver = "00040B917352651999F90008621011111111000A041F0440043804320435"

func TestReadSMS(t *testing.T) {
	m, mock := newTestModem(t, profiles.Generic{})
	_ = m.Init(context.Background())

	mock.PushResponse("AT+CMGF=0", "OK")
	mock.PushResponse("AT+CMGL=4",
		`+CMGL: 1,1,,30`,
		testPDUDeliver,
		"OK",
	)

	msgs, err := m.ReadSMS(context.Background(), ReadFilter{})
	if err != nil {
		t.Fatalf("ReadSMS: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len=%d, ожидалось 1", len(msgs))
	}
	got := msgs[0]
	if got.Index != 1 {
		t.Errorf("index=%d", got.Index)
	}
	if got.Status != "REC READ" {
		t.Errorf("status=%q", got.Status)
	}
	if got.Peer != "+37255691999" {
		t.Errorf("peer=%q", got.Peer)
	}
	if got.Text != "Приве" {
		t.Errorf("text=%q", got.Text)
	}
	if got.Encoding != "UCS-2" {
		t.Errorf("encoding=%q", got.Encoding)
	}
	if got.RawPDU != testPDUDeliver {
		t.Errorf("rawPDU=%q", got.RawPDU)
	}
}

func TestReadSMSUnread(t *testing.T) {
	m, mock := newTestModem(t, profiles.Generic{})
	_ = m.Init(context.Background())

	mock.PushResponse("AT+CMGF=0", "OK")
	mock.PushResponse("AT+CMGL=0",
		`+CMGL: 2,0,,30`,
		testPDUDeliver,
		"OK",
	)

	msgs, err := m.ReadSMS(context.Background(), ReadFilter{UnreadOnly: true})
	if err != nil {
		t.Fatalf("ReadSMS: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len=%d", len(msgs))
	}
	if msgs[0].Status != "REC UNREAD" {
		t.Errorf("status=%q", msgs[0].Status)
	}
}

func TestReadSMSEmpty(t *testing.T) {
	m, mock := newTestModem(t, profiles.Generic{})
	_ = m.Init(context.Background())

	mock.PushResponse("AT+CMGF=0", "OK")
	mock.PushResponse("AT+CMGL=4", "OK")

	msgs, err := m.ReadSMS(context.Background(), ReadFilter{})
	if err != nil {
		t.Fatalf("ReadSMS: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("len=%d, ожидалось 0", len(msgs))
	}
}

// ==================== Delete ====================

func TestDeleteSMS(t *testing.T) {
	m, mock := newTestModem(t, profiles.Generic{})
	_ = m.Init(context.Background())

	mock.PushResponse("AT+CMGD=5", "OK")

	if err := m.DeleteSMS(context.Background(), 5); err != nil {
		t.Fatalf("DeleteSMS: %v", err)
	}

	written := string(mock.WrittenBytes())
	if !strings.Contains(written, "AT+CMGD=5") {
		t.Error("AT+CMGD=5 не отправлен")
	}
}

func TestDeleteAllSMS(t *testing.T) {
	m, mock := newTestModem(t, profiles.Generic{})
	_ = m.Init(context.Background())

	mock.PushResponse("AT+CMGD=1,4", "OK")

	if err := m.DeleteAllSMS(context.Background()); err != nil {
		t.Fatalf("DeleteAllSMS: %v", err)
	}
}

// ==================== helpers ====================

func TestParseCMGSReference(t *testing.T) {
	cases := []struct {
		lines []string
		want  int64
		err   bool
	}{
		{[]string{"+CMGS: 42"}, 42, false},
		{[]string{"foo", "+CMGS: 100", "bar"}, 100, false},
		{[]string{"OK"}, 0, true},
		{[]string{}, 0, true},
	}
	for _, c := range cases {
		got, err := parseCMGSReference(c.lines)
		if c.err {
			if err == nil {
				t.Errorf("%v: ожидалась ошибка", c.lines)
			}
			continue
		}
		if err != nil {
			t.Errorf("%v: %v", c.lines, err)
			continue
		}
		if got != c.want {
			t.Errorf("%v: got %d, want %d", c.lines, got, c.want)
		}
	}
}
