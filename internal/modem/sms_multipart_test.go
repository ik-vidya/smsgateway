package modem

import (
	"context"
	"strings"
	"testing"

	"github.com/ik-vidya/smsgateway/internal/profiles"
)

// Длинный текст (215 символов ASCII) → 2 части GSM-7.
// 160 символов — порог одиночной GSM-7 SMS.
const longGSM7 = "This is a very long test message that will " +
	"definitely exceed one hundred and sixty characters to force " +
	"the SMS encoder to split it into multiple parts with UDH. " +
	"Additional text to be sure we are well above the limit."

func TestSendSMSMultipart(t *testing.T) {
	m, mock := newTestModem(t, profiles.Generic{})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	mock.PushResponse("AT+CMGF=0", "OK")
	// Два ExecPrompt: сначала на первую часть, потом на вторую.
	mock.PushResponse("", "> ", "+CMGS: 10", "OK") // часть 1
	mock.PushResponse("", "> ", "+CMGS: 11", "OK") // часть 2

	res, err := m.SendSMS(context.Background(),
		"+37121234567", longGSM7, SendOptions{})
	if err != nil {
		t.Fatalf("SendSMS: %v", err)
	}
	if res.Parts != 2 {
		t.Errorf("Parts=%d, ожидалось 2", res.Parts)
	}
	if len(res.References) != 2 {
		t.Fatalf("len(References)=%d, ожидалось 2", len(res.References))
	}
	if res.References[0] != 10 || res.References[1] != 11 {
		t.Errorf("References=%v, ожидалось [10 11]", res.References)
	}
	if res.Reference != 10 {
		t.Errorf("Reference=%d, ожидалось 10 (первая часть)", res.Reference)
	}
	if len(res.RawPDUs) != 2 {
		t.Errorf("len(RawPDUs)=%d, ожидалось 2", len(res.RawPDUs))
	}

	// Два AT+CMGS должны быть в записанных командах.
	written := string(mock.WrittenBytes())
	if n := strings.Count(written, "AT+CMGS="); n != 2 {
		t.Errorf("AT+CMGS отправлен %d раз, ожидалось 2", n)
	}
	// Два Ctrl-Z.
	if n := strings.Count(written, "\x1a"); n != 2 {
		t.Errorf("Ctrl-Z отправлен %d раз, ожидалось 2", n)
	}
}

func TestSendSMSMultipartPartialFailure(t *testing.T) {
	m, mock := newTestModem(t, profiles.Generic{})
	_ = m.Init(context.Background())

	mock.PushResponse("AT+CMGF=0", "OK")
	// Первая часть OK.
	mock.PushResponse("", "> ", "+CMGS: 100", "OK")
	// Вторая падает.
	mock.PushResponse("", "> ", "+CMS ERROR: 305")

	res, err := m.SendSMS(context.Background(),
		"+37121234567", longGSM7, SendOptions{})
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	// Первая часть отправилась, в References только она.
	if len(res.References) != 1 || res.References[0] != 100 {
		t.Errorf("References=%v, ожидалось [100]", res.References)
	}
	if !strings.Contains(err.Error(), "часть 2/2") {
		t.Errorf("err=%v, ожидалось упоминание части 2/2", err)
	}
}

func TestSendSMSSinglePartStillWorks(t *testing.T) {
	m, mock := newTestModem(t, profiles.Generic{})
	_ = m.Init(context.Background())

	mock.PushResponse("AT+CMGF=0", "OK")
	mock.PushResponse("", "> ", "+CMGS: 1", "OK")

	res, err := m.SendSMS(context.Background(),
		"+37121234567", "Test", SendOptions{})
	if err != nil {
		t.Fatalf("SendSMS: %v", err)
	}
	if res.Parts != 1 {
		t.Errorf("Parts=%d, ожидалось 1", res.Parts)
	}
	if len(res.References) != 1 {
		t.Errorf("len(References)=%d, ожидалось 1", len(res.References))
	}
	if res.Reference != 1 {
		t.Errorf("Reference=%d", res.Reference)
	}
}
