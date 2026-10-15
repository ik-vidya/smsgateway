package modem

import (
	"context"
	"testing"
	"time"
)

// Реальный PDU SMS-STATUS-REPORT с TP-MR=179, TP-ST=0x00 (доставлено).
// Из лога gammu-smsd для SIM800L.
const testCDSPDU = "0691735205991106B30B917352651999F9620131220473216201312204042100"

func TestURCCDSWithPDU(t *testing.T) {
	at, mock := newTestTransport(t)

	// +CDS header, затем PDU, затем ответ на команду.
	mock.PushResponse("AT+CPIN?",
		"+CDS: 25",
		testCDSPDU,
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

	// Ни +CDS, ни PDU не должны попасть в ответ.
	for _, line := range resp.Payload() {
		if line == "+CDS: 25" || line == testCDSPDU {
			t.Errorf("строка +CDS попала в resp: %q", line)
		}
	}

	select {
	case u := <-urcCh:
		cds, ok := u.(URCCDS)
		if !ok {
			t.Fatalf("не URCCDS: %T", u)
		}
		if cds.Reference != 179 {
			t.Errorf("Reference=%d, ожидалось 179", cds.Reference)
		}
		if cds.TPST != 0x00 {
			t.Errorf("TPST=0x%02X, ожидалось 0x00", cds.TPST)
		}
		if cds.Peer != "+37255691999" {
			t.Errorf("Peer=%q", cds.Peer)
		}
		if cds.PDU != testCDSPDU {
			t.Errorf("PDU=%q", cds.PDU)
		}
	case <-time.After(time.Second):
		t.Fatal("URC не пришёл")
	}
}

// Если после +CDS пришла не-hex строка, URC уходит без PDU.
func TestURCCDSWithoutPDU(t *testing.T) {
	at, mock := newTestTransport(t)

	mock.PushResponse("AT+CPIN?",
		"+CDS: 25",
		"+CPIN: READY", // не hex — PDU не будет
		"OK",
	)

	urcCh := at.URCs()
	_, err := at.Exec(context.Background(), "AT+CPIN?")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	select {
	case u := <-urcCh:
		cds, ok := u.(URCCDS)
		if !ok {
			t.Fatalf("не URCCDS: %T", u)
		}
		if cds.Length != 25 {
			t.Errorf("Length=%d", cds.Length)
		}
		if cds.PDU != "" {
			t.Errorf("PDU должен быть пуст, получено %q", cds.PDU)
		}
	case <-time.After(time.Second):
		t.Fatal("URC не пришёл")
	}
}

func TestIsHexLine(t *testing.T) {
	cases := map[string]bool{
		"0691735205991106B30B917352651999F9620131220473216201312204042100": true,
		"00AA":    true,
		"00aa":    true,
		"0":       false,
		"00A":     false,
		"00AG":    false,
		"+CPIN":   false,
		"OK":      false,
		"":        false,
	}
	for in, want := range cases {
		if got := isHexLine(in); got != want {
			t.Errorf("isHexLine(%q)=%v, ожидалось %v", in, got, want)
		}
	}
}

func TestInterpretTPST(t *testing.T) {
	cases := map[int]string{
		0x00: "доставлено",
		0x20: "не доставлено (нет абонента)",
		0x40: "ошибка SMSC",
	}
	for code, want := range cases {
		if got := InterpretTPST(code); got != want {
			t.Errorf("InterpretTPST(0x%02X)=%q, ожидалось %q", code, got, want)
		}
	}
}