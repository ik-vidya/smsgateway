package gammu

import (
	"testing"
	"time"
)

func TestParseIncomingBasic(t *testing.T) {
	fn, err := ParseIncomingFileName(
		"IN20261015_120034_00_+37121234567_00.txt")
	if err != nil {
		t.Fatalf("ParseIncomingFileName: %v", err)
	}
	if fn.Kind != KindIncoming {
		t.Errorf("Kind=%v", fn.Kind)
	}
	if fn.Serial != 0 || fn.Sequence != 0 {
		t.Errorf("Serial=%d Sequence=%d", fn.Serial, fn.Sequence)
	}
	if fn.Sender != "+37121234567" {
		t.Errorf("Sender=%q", fn.Sender)
	}
	if fn.Ext != "txt" {
		t.Errorf("Ext=%q", fn.Ext)
	}
	want := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)
	if !fn.Time.Equal(want) {
		t.Errorf("Time=%v, want %v", fn.Time, want)
	}
}

func TestParseIncomingMultipart(t *testing.T) {
	fn, err := ParseIncomingFileName(
		"IN20261015_120034_07_+37121234567_02.txt")
	if err != nil {
		t.Fatalf("ParseIncomingFileName: %v", err)
	}
	if fn.Serial != 7 || fn.Sequence != 2 {
		t.Errorf("Serial=%d Sequence=%d", fn.Serial, fn.Sequence)
	}
}

func TestParseIncomingAlphaSender(t *testing.T) {
	fn, err := ParseIncomingFileName(
		"IN20261015_120034_00_TELE2_00.txt")
	if err != nil {
		t.Fatalf("ParseIncomingFileName: %v", err)
	}
	if fn.Sender != "TELE2" {
		t.Errorf("Sender=%q", fn.Sender)
	}
}

func TestParseIncomingSenderWithUnderscore(t *testing.T) {
	fn, err := ParseIncomingFileName(
		"IN20261015_120034_00_MY_BANK_00.txt")
	if err != nil {
		t.Fatalf("ParseIncomingFileName: %v", err)
	}
	if fn.Sender != "MY_BANK" {
		t.Errorf("Sender=%q", fn.Sender)
	}
}

func TestParseIncomingErrors(t *testing.T) {
	cases := []string{
		"",
		"OUTA_+371_00.txt",
		"IN20261015.txt",
		"IN20261015_120034_00.txt",
		"IN2026AB15_120034_00_+371_00.txt",
		"IN20261015_1200_00_+371_00.txt",
		"IN20261015_120034_xx_+371_00.txt",
		"IN20261015_120034_00_+371.txt",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := ParseIncomingFileName(c); err == nil {
				t.Errorf("%q: ожидалась ошибка", c)
			}
		})
	}
}

// ==================== OUT ====================

func TestParseOutgoingSimple(t *testing.T) {
	fn, err := ParseOutgoingFileName("OUT+37121234567.txt")
	if err != nil {
		t.Fatalf("ParseOutgoingFileName: %v", err)
	}
	if fn.Priority != "A" {
		t.Errorf("Priority=%q, ожидалось A", fn.Priority)
	}
	if fn.Recipient != "+37121234567" {
		t.Errorf("Recipient=%q", fn.Recipient)
	}
	if fn.RequestDR || fn.Flash {
		t.Errorf("флаги: DR=%v Flash=%v", fn.RequestDR, fn.Flash)
	}
}

func TestParseOutgoingShort(t *testing.T) {
	fn, err := ParseOutgoingFileName("OUTB_+37121234567_42.txt")
	if err != nil {
		t.Fatalf("ParseOutgoingFileName: %v", err)
	}
	if fn.Priority != "B" {
		t.Errorf("Priority=%q", fn.Priority)
	}
	if fn.Recipient != "+37121234567" {
		t.Errorf("Recipient=%q", fn.Recipient)
	}
	if fn.Serial != 42 {
		t.Errorf("Serial=%d", fn.Serial)
	}
}

func TestParseOutgoingShortNoSerial(t *testing.T) {
	fn, err := ParseOutgoingFileName("OUTC_+37121234567.txt")
	if err != nil {
		t.Fatalf("ParseOutgoingFileName: %v", err)
	}
	if fn.Priority != "C" || fn.Recipient != "+37121234567" {
		t.Errorf("%+v", fn)
	}
}

func TestParseOutgoingFull(t *testing.T) {
	fn, err := ParseOutgoingFileName(
		"OUTG20040620_193810_123_+4512345678_xpq.txt")
	if err != nil {
		t.Fatalf("ParseOutgoingFileName: %v", err)
	}
	if fn.Priority != "G" {
		t.Errorf("Priority=%q", fn.Priority)
	}
	if fn.Recipient != "+4512345678" {
		t.Errorf("Recipient=%q", fn.Recipient)
	}
	if fn.Serial != 123 {
		t.Errorf("Serial=%d", fn.Serial)
	}
	if fn.Note != "xpq" {
		t.Errorf("Note=%q", fn.Note)
	}
	want := time.Date(2004, 6, 20, 19, 38, 10, 0, time.Local)
	if !fn.Time.Equal(want) {
		t.Errorf("Time=%v, want %v", fn.Time, want)
	}
}

func TestParseOutgoingFlags(t *testing.T) {
	cases := map[string]struct{ dr, flash bool }{
		"OUTA_+371_00.txt":   {false, false},
		"OUTA_+371_00.txtd":  {true, false},
		"OUTA_+371_00.txtf":  {false, true},
		"OUTA_+371_00.txtdf": {true, true},
		"OUTA_+371_00.txtfd": {true, true},
	}
	for name, want := range cases {
		fn, err := ParseOutgoingFileName(name)
		if err != nil {
			t.Errorf("%q: %v", name, err)
			continue
		}
		if fn.RequestDR != want.dr || fn.Flash != want.flash {
			t.Errorf("%q: DR=%v Flash=%v, ожидалось %v/%v",
				name, fn.RequestDR, fn.Flash, want.dr, want.flash)
		}
	}
}

func TestParseOutgoingUnsupportedExt(t *testing.T) {
	for _, name := range []string{
		"OUTA_+371_00.b",
		"OUTA_+371_00.smsbackup",
		"OUT+371.bin",
	} {
		if _, err := ParseOutgoingFileName(name); err == nil {
			t.Errorf("%q: ожидалась ошибка", name)
		}
	}
}

func TestParseOutgoingErrors(t *testing.T) {
	cases := []string{
		"",
		"IN20261015_120034_00_+371_00.txt",
		"OUT.txt",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := ParseOutgoingFileName(c); err == nil {
				t.Errorf("%q: ожидалась ошибка", c)
			}
		})
	}
}

// ==================== Build ====================

func TestBuildIncomingRoundtrip(t *testing.T) {
	ts := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)
	name := BuildIncomingFileName(ts, 5, "+37121234567", 0, "txt")
	want := "IN20261015_120034_05_+37121234567_00.txt"
	if name != want {
		t.Fatalf("name=%q, want %q", name, want)
	}
	fn, err := ParseIncomingFileName(name)
	if err != nil {
		t.Fatalf("ParseIncomingFileName: %v", err)
	}
	if fn.Serial != 5 || fn.Sender != "+37121234567" || fn.Sequence != 0 {
		t.Errorf("roundtrip: %+v", fn)
	}
}

func TestBuildOutgoingRoundtrip(t *testing.T) {
	name := BuildOutgoingFileName("C", "+37121234567", 42, "txtd")
	want := "OUTC_+37121234567_42.txtd"
	if name != want {
		t.Fatalf("name=%q, want %q", name, want)
	}
	fn, err := ParseOutgoingFileName(name)
	if err != nil {
		t.Fatalf("ParseOutgoingFileName: %v", err)
	}
	if fn.Priority != "C" || fn.Recipient != "+37121234567" ||
		fn.Serial != 42 || !fn.RequestDR || fn.Flash {
		t.Errorf("roundtrip: %+v", fn)
	}
}

func TestLower(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"A", "B", true},
		{"B", "C", true},
		{"Y", "Z", true},
		{"Z", "Z", false},
		{"", "A", true},
		{"x", "A", true}, // невалидный → начинаем с A
	}
	for _, c := range cases {
		got, ok := Lower(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("Lower(%q)=(%q,%v), ожидалось (%q,%v)",
				c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSanitizeSender(t *testing.T) {
	cases := map[string]string{
		"+37121234567": "+37121234567",
		"abc-123/x":    "abc_123_x",
		"":             "",
		"Ä+1":          "_+1",
	}
	for in, want := range cases {
		if got := sanitizeSender(in); got != want {
			t.Errorf("sanitizeSender(%q)=%q, ожидалось %q", in, got, want)
		}
	}
}
