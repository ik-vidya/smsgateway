package gammu

import (
	"strings"
	"testing"
	"time"
)

const sampleTextDecoded = `SMSC = "+3725509911"
Number = "+37121234567"
TextDecoded = "Привет, мир!"
Coding = "Default_No_Compression"
Received = "2026-10-15 12:00:34"
`

// PDU SMS-DELIVER: peer=+37255691999, text="Приве".
// Взят из тестов modem (та же последовательность).
const samplePDUDeliver = "00040B917352651999F90008621011111111000A041F0440043804320435"

const sampleHexPDU = `SMSC = "+3725509911"
Text = "` + samplePDUDeliver + `"
Coding = "Unicode_No_Compression"
Received = "2026-10-15 12:00:34"
`

const samplePlainText = `SMSC = "+3725509911"
Number = "+37121234567"
Text = "Hello world"
Received = "2026-10-15 12:00:34"
`

func TestParseFileTextDecoded(t *testing.T) {
	m, err := ParseFile(sampleTextDecoded)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if m.SMSC != "+3725509911" {
		t.Errorf("SMSC=%q", m.SMSC)
	}
	if m.Number != "+37121234567" {
		t.Errorf("Number=%q", m.Number)
	}
	if m.TextDecoded != "Привет, мир!" {
		t.Errorf("TextDecoded=%q", m.TextDecoded)
	}
	want := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)
	if !m.Received.Equal(want) {
		t.Errorf("Received=%v, want %v", m.Received, want)
	}

	text, src := m.EffectiveText()
	if text != "Привет, мир!" || src != "text_decoded" {
		t.Errorf("EffectiveText: %q, %q", text, src)
	}
}

func TestParseFileHexPDU(t *testing.T) {
	m, err := ParseFile(sampleHexPDU)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	text, src := m.EffectiveText()
	if src != "pdu" {
		t.Errorf("source=%q, want pdu", src)
	}
	if text != "Приве" {
		t.Errorf("text=%q, want Приве", text)
	}
}

func TestParseFilePlainText(t *testing.T) {
	m, err := ParseFile(samplePlainText)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	text, src := m.EffectiveText()
	if text != "Hello world" || src != "plain" {
		t.Errorf("EffectiveText: %q, %q", text, src)
	}
}

func TestParseFileUnquoted(t *testing.T) {
	src := "Number = +37121234567\nTextDecoded = hello\n"
	m, err := ParseFile(src)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if m.Number != "+37121234567" {
		t.Errorf("Number=%q", m.Number)
	}
	if m.TextDecoded != "hello" {
		t.Errorf("TextDecoded=%q", m.TextDecoded)
	}
}

func TestParseFileSkipsCommentsAndSections(t *testing.T) {
	src := "# comment\n[SMSBackup]\nNumber = \"+371\"\n\nTextDecoded = \"hi\"\n"
	m, err := ParseFile(src)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if m.Number != "+371" || m.TextDecoded != "hi" {
		t.Errorf("%+v", m)
	}
}

func TestParseFileClass(t *testing.T) {
	src := "Number = \"+371\"\nTextDecoded = \"x\"\nClass = 1\n"
	m, err := ParseFile(src)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if m.Class != 1 {
		t.Errorf("Class=%d", m.Class)
	}
}

func TestLooksLikeHexPDU(t *testing.T) {
	cases := map[string]bool{
		samplePDUDeliver: true,
		"00AA":           true,
		"00aa":           true,
		"hello":          false, // содержит 'h','l','o'
		"00A":            false, // нечётная длина
		"00AG":           false, // содержит 'G'
		"":               false,
	}
	for in, want := range cases {
		if got := looksLikeHexPDU(in); got != want {
			t.Errorf("looksLikeHexPDU(%q)=%v, want %v", in, got, want)
		}
	}
}

func TestEffectiveTextHexFallbackToPlain(t *testing.T) {
	// Text похож на hex, но PDU невалидный — должен уйти в plain,
	// не упасть.
	m := &Message{Text: strings.Repeat("AB", 20)}
	text, src := m.EffectiveText()
	if src != "plain" {
		t.Errorf("source=%q, want plain (fallback)", src)
	}
	if text != m.Text {
		t.Errorf("text != original")
	}
}

func TestParseTimestampLayouts(t *testing.T) {
	for _, in := range []string{
		"2026-10-15 12:00:34",
		"2026-10-15T12:00:34",
		"2026-10-15T12:00:34Z",
	} {
		ts := parseTimestamp(in)
		if ts.IsZero() {
			t.Errorf("%q: пустой результат", in)
		}
	}
	if !parseTimestamp("").IsZero() {
		t.Error("пустая строка должна давать zero time")
	}
	if !parseTimestamp("garbage").IsZero() {
		t.Error("мусор должен давать zero time")
	}
}
