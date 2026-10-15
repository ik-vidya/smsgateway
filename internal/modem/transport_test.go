package modem

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newTestTransport(t *testing.T) (*ATTransport, *MockTransport) {
	t.Helper()
	mock := NewMock()
	at, err := New(mock, Config{
		CmdTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = at.Close() })
	return at, mock
}

func TestExecOK(t *testing.T) {
	at, mock := newTestTransport(t)
	mock.PushResponse("AT", "OK")

	resp, err := at.Exec(context.Background(), "AT")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !resp.OK() {
		t.Errorf("не OK: %+v", resp)
	}
}

func TestExecWithPayload(t *testing.T) {
	at, mock := newTestTransport(t)
	mock.PushResponse("AT+CSQ", "+CSQ: 17,0", "OK")

	resp, err := at.Exec(context.Background(), "AT+CSQ")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !resp.OK() {
		t.Fatalf("не OK: %+v", resp)
	}
	if got := resp.String(); got != "+CSQ: 17,0" {
		t.Errorf("payload=%q", got)
	}
}

func TestExecError(t *testing.T) {
	at, mock := newTestTransport(t)
	mock.PushResponse("AT+BAD", "ERROR")

	_, err := at.Exec(context.Background(), "AT+BAD")
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !IsATError(err) {
		t.Errorf("не ATError: %v", err)
	}
}

func TestExecCMEError(t *testing.T) {
	at, mock := newTestTransport(t)
	mock.PushResponse("AT+CPIN?", "+CME ERROR: 10")

	_, err := at.Exec(context.Background(), "AT+CPIN?")
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "SIM not inserted") {
		t.Errorf("не расшифровано: %v", err)
	}
}

func TestExecTimeout(t *testing.T) {
	at, _ := newTestTransport(t)
	// Нет ответа — должен быть таймаут.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := at.Exec(ctx, "AT+SLOW")
	if err == nil {
		t.Fatal("ожидался таймаут")
	}
}

func TestExecSerializes(t *testing.T) {
	at, mock := newTestTransport(t)
	mock.PushResponse("AT+CMD1", "OK")
	mock.PushResponse("AT+CMD2", "OK")

	done := make(chan struct{}, 2)
	go func() {
		_, _ = at.Exec(context.Background(), "AT+CMD1")
		done <- struct{}{}
	}()
	go func() {
		_, _ = at.Exec(context.Background(), "AT+CMD2")
		done <- struct{}{}
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout")
		}
	}
}

// ==================== URC ====================

func TestURCCMTI(t *testing.T) {
	at, mock := newTestTransport(t)
	// Сначала отдаём URC, потом ответ.
	mock.PushResponse("AT+CPIN?",
		`+CMTI: "SM",5`,
		"+CPIN: READY",
		"OK",
	)

	urcCh := at.URCs()
	resp, err := at.Exec(context.Background(), "AT+CPIN?")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !resp.OK() {
		t.Fatalf("не OK: %+v", resp)
	}
	// Ответ должен содержать +CPIN: READY, но не +CMTI.
	for _, line := range resp.Payload() {
		if strings.Contains(line, "+CMTI") {
			t.Errorf("+CMTI попал в resp: %v", line)
		}
	}

	// URC должен прийти в канал.
	select {
	case u := <-urcCh:
		cmti, ok := u.(URCCMTI)
		if !ok {
			t.Fatalf("не URCCMTI: %T", u)
		}
		if cmti.Mem != "SM" || cmti.Index != 5 {
			t.Errorf("CMTI=%+v", cmti)
		}
	case <-time.After(time.Second):
		t.Fatal("URC не пришёл")
	}
}

func TestURCCREG(t *testing.T) {
	at, mock := newTestTransport(t)
	mock.PushResponse("AT+CREG?",
		`+CREG: 2,5,"00DF","156B"`,
		"OK",
	)

	urcCh := at.URCs()
	_, err := at.Exec(context.Background(), "AT+CREG?")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	select {
	case u := <-urcCh:
		creg, ok := u.(URCCREG)
		if !ok {
			t.Fatalf("не URCCREG: %T", u)
		}
		if creg.Stat != 5 || creg.LAC != "00DF" {
			t.Errorf("CREG=%+v", creg)
		}
	case <-time.After(time.Second):
		t.Fatal("URC не пришёл")
	}
}

func TestURCNoCarrier(t *testing.T) {
	at, mock := newTestTransport(t)
	mock.PushResponse("AT", "NO CARRIER", "OK")

	urcCh := at.URCs()
	_, _ = at.Exec(context.Background(), "AT")

	select {
	case u := <-urcCh:
		if _, ok := u.(URCNoCarrier); !ok {
			t.Fatalf("не URCNoCarrier: %T", u)
		}
	case <-time.After(time.Second):
		t.Fatal("URC не пришёл")
	}
}

func TestParseStandardURC(t *testing.T) {
	cases := []struct {
		line string
		kind string
	}{
		{`+CMTI: "SM",5`, "cmti"},
		{`+CDS: 25`, "cds"},
		{`+CREG: 2,5,"00DF","156B"`, "creg"},
		{`+CGREG: 2,5,"00DF","156B"`, "cgreg"},
		{`+CUSD: 0,"Balance: 10 EUR",15`, "cusd"},
		{`+CBC: 0,100,4411`, "cbc"},
		{`RING`, "ring"},
		{`NO CARRIER`, "no_carrier"},
		{`RDY`, "rdy"},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			u, ok := parseStandardURC(c.line)
			if !ok {
				t.Fatalf("не распознан: %q", c.line)
			}
			if u.Kind() != c.kind {
				t.Errorf("kind=%q, ожидалось %q", u.Kind(), c.kind)
			}
		})
	}
}

func TestParseNonURC(t *testing.T) {
	cases := []string{
		"OK",
		"ERROR",
		"+CSQ: 17,0",
		"+CPIN: READY",
		"ATE0",
	}
	for _, line := range cases {
		if u, ok := parseStandardURC(line); ok {
			t.Errorf("%q ошибочно распознан как URC: %T", line, u)
		}
	}
}

func TestInterpretErrorLine(t *testing.T) {
	cases := map[string]string{
		"+CME ERROR: 10":  "SIM not inserted",
		"+CMS ERROR: 500": "нет баланса",
		"+CMS ERROR: 2171": "нет сети",
		"+CMS ERROR: Temporary failure": "SMSC недоступен",
	}
	for in, want := range cases {
		got := InterpretErrorLine(in)
		if !strings.Contains(got, want) {
			t.Errorf("%q → %q, ожидалось содержащее %q", in, got, want)
		}
	}
}

// ==================== ExecPrompt ====================

func TestExecPromptOK(t *testing.T) {
    at, mock := newTestTransport(t)

    // Модем: "\r\n> " без \n, потом после payload вернёт +CMGS и OK.
    // Мы не можем разделить два ответа через mock по команде,
    // потому что mock срабатывает на Write целиком. Придётся
    // объединить: на Write "AT+CMGS=25\r" отдать "> " без \n,
    // а payload обрабатываем отдельно.
    // Проще: push-нуть оба ответа на первый Write.
    mock.PushResponse("AT+CMGS=25",
	"> ", // это отдастся после первого Write
	"+CMGS: 42",
	"OK",
    )

    resp, err := at.ExecPrompt(context.Background(), "AT+CMGS=25",
	[]byte("0011AA\r\x1a"))
    if err != nil {
	t.Fatalf("ExecPrompt: %v", err)
    }
    if !resp.OK() {
	t.Fatalf("не OK: %+v", resp)
    }
    // Проверим, что +CMGS: 42 в payload.
    found := false
    for _, l := range resp.Payload() {
	if l == "+CMGS: 42" {
	    found = true
	}
    }
    if !found {
	t.Errorf("+CMGS: 42 не найден: %v", resp.Lines)
    }
}

func TestExecPromptErrorBeforePrompt(t *testing.T) {
    at, mock := newTestTransport(t)
    mock.PushResponse("AT+CMGS=999", "+CMS ERROR: 500")

    _, err := at.ExecPrompt(context.Background(), "AT+CMGS=999",
	[]byte("ignored"))
    if err == nil {
	t.Fatal("ожидалась ошибка")
    }
}

func TestIsPrompt(t *testing.T) {
    if !isPrompt("> ") {
	t.Error("'> ' должен быть prompt")
    }
    if !isPrompt(">") {
	t.Error("'>' должен быть prompt")
    }
    if isPrompt(">> ") {
	t.Error("'>> ' не prompt")
    }
    if isPrompt("OK") {
	t.Error("'OK' не prompt")
    }
}

