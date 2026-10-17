package gammu

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// ==================== фикстуры ====================

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

// ==================== базовые тесты ====================

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

func TestParseFilePlainKV(t *testing.T) {
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
		"hello":          false,
		"00A":            false,
		"00AG":           false,
		"":               false,
	}
	for in, want := range cases {
		if got := looksLikeHexPDU(in); got != want {
			t.Errorf("looksLikeHexPDU(%q)=%v, want %v", in, got, want)
		}
	}
}

func TestEffectiveTextHexFallbackToPlain(t *testing.T) {
	// Text похож на hex, но PDU невалидный — должен уйти в plain.
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

// ==================== plain text fallback ====================

const plainRuText = "Тест с rsmodule-tallin1 (tallin1 +37255691999): 2026-10-17 23:41:25"

func TestParseFilePlaintextUTF8NoBOM(t *testing.T) {
	m, err := ParseFile(plainRuText)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(m.Fields) != 0 {
		t.Errorf("Fields=%v, ожидалось пусто (plain text)", m.Fields)
	}
	text, src := m.EffectiveText()
	if text != plainRuText {
		t.Errorf("EffectiveText=%q, ожидалось %q", text, plainRuText)
	}
	if src != "plain" {
		t.Errorf("source=%q, ожидалось plain", src)
	}
}

func TestParseFilePlaintextMultiline(t *testing.T) {
	src := "line1\nline2\nline3"
	m, err := ParseFile(src)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	text, _ := m.EffectiveText()
	if text != src {
		t.Errorf("EffectiveText=%q, ожидалось %q", text, src)
	}
}

func TestParseFilePlaintextWithEquals(t *testing.T) {
	// plain-текст содержит «=», но ни один ключ до «=» не
	// является известным gammu-полем — fallback должен сработать.
	src := "Body = some text\nSecond line"
	m, err := ParseFile(src)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	text, source := m.EffectiveText()
	if text != src {
		t.Errorf("EffectiveText=%q, ожидалось %q", text, src)
	}
	if source != "plain" {
		t.Errorf("source=%q, ожидалось plain", source)
	}
}

func TestParseFileKeyValueStillWins(t *testing.T) {
	// Если в файле есть известный gammu-ключ, fallback не
	// срабатывает.
	src := "Number = \"+371\"\nTextDecoded = \"hi\"\n"
	m, err := ParseFile(src)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if m.Number != "+371" {
		t.Errorf("Number=%q", m.Number)
	}
	text, source := m.EffectiveText()
	if text != "hi" {
		t.Errorf("EffectiveText=%q, ожидалось hi", text)
	}
	if source != "text_decoded" {
		t.Errorf("source=%q, ожидалось text_decoded", source)
	}
}

func TestParseFileKnownKeyOnly(t *testing.T) {
	// Один известный ключ (SMSC) без текста — fallback НЕ
	// срабатывает: это gammu-формат с пустым текстом.
	src := `SMSC = "+3725509911"`
	m, err := ParseFile(src)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if m.SMSC != "+3725509911" {
		t.Errorf("SMSC=%q", m.SMSC)
	}
	text, _ := m.EffectiveText()
	if text != "" {
		t.Errorf("EffectiveText=%q, ожидалось пусто", text)
	}
}

// ==================== полный путь: байты → DecodeText → ParseFile ====================
//
// Реальный путь в Manager: os.ReadFile → DecodeText → ParseFile.
// Тестируем end-to-end для разных кодировок plain-файла.

func encodeUTF16(s string, order binary.ByteOrder, bom bool) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(u16)*2+2)
	if bom {
		if order == binary.LittleEndian {
			out = append(out, 0xFF, 0xFE)
		} else {
			out = append(out, 0xFE, 0xFF)
		}
	}
	buf := make([]byte, 2)
	for _, v := range u16 {
		order.PutUint16(buf, v)
		out = append(out, buf...)
	}
	return out
}

func TestParseFileFullPathUTF8NoBOM(t *testing.T) {
	raw := []byte(plainRuText)
	content, err := DecodeText(raw)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	m, err := ParseFile(content)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	text, src := m.EffectiveText()
	if text != plainRuText {
		t.Errorf("EffectiveText=%q, ожидалось %q", text, plainRuText)
	}
	if src != "plain" {
		t.Errorf("source=%q, ожидалось plain", src)
	}
}

func TestParseFileFullPathUTF8WithBOM(t *testing.T) {
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte(plainRuText)...)
	content, err := DecodeText(raw)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	m, err := ParseFile(content)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	text, src := m.EffectiveText()
	if text != plainRuText {
		t.Errorf("EffectiveText=%q, ожидалось %q", text, plainRuText)
	}
	if src != "plain" {
		t.Errorf("source=%q, ожидалось plain", src)
	}
}

func TestParseFileFullPathUTF16LEWithBOM(t *testing.T) {
	// Точная симуляция реального файла от rssmspool:
	// ff fe 22 04 35 04 41 04 42 04 ...
	raw := encodeUTF16(plainRuText, binary.LittleEndian, true)
	content, err := DecodeText(raw)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	m, err := ParseFile(content)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	text, src := m.EffectiveText()
	if text != plainRuText {
		t.Errorf("EffectiveText=%q, ожидалось %q", text, plainRuText)
	}
	if src != "plain" {
		t.Errorf("source=%q, ожидалось plain", src)
	}
}

func TestParseFileFullPathUTF16BEWithBOM(t *testing.T) {
	raw := encodeUTF16(plainRuText, binary.BigEndian, true)
	content, err := DecodeText(raw)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	m, err := ParseFile(content)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	text, src := m.EffectiveText()
	if text != plainRuText {
		t.Errorf("EffectiveText=%q, ожидалось %q", text, plainRuText)
	}
	if src != "plain" {
		t.Errorf("source=%q, ожидалось plain", src)
	}
}
