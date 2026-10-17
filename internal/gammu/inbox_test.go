package gammu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustWriter(t *testing.T, dir, format string) *InboxWriter {
	t.Helper()
	w, err := NewInboxWriter(dir, format, nil)
	if err != nil {
		t.Fatalf("NewInboxWriter: %v", err)
	}
	return w
}

// hasKeyValue проверяет, что в теле файла нет Key = Value
// (inbox должен быть plain text, как у gammu-smsd).
func hasKeyValue(body string) (string, bool) {
	for _, key := range []string{
		"SMSC =", "Number =", "Text =", "TextDecoded =",
		"Coding =", "UDH =", "Class =", "Sent =", "Received =",
	} {
		if strings.Contains(body, key) {
			return key, true
		}
	}
	return "", false
}

func TestNewInboxWriterErrors(t *testing.T) {
	if _, err := NewInboxWriter("", "utf8", nil); err == nil {
		t.Error("ожидалась ошибка для пустого dir")
	}
	if _, err := NewInboxWriter(t.TempDir(), "detail", nil); err == nil {
		t.Error("ожидалась ошибка для detail")
	}
	if _, err := NewInboxWriter(t.TempDir(), "cp1252", nil); err == nil {
		t.Error("ожидалась ошибка для неизвестного формата")
	}
}

func TestInboxWriterFormatNormalization(t *testing.T) {
	cases := map[string]string{
		"":         "utf8",
		"utf8":     "utf8",
		"UTF-8":    "utf8",
		"standard": "utf8",
		"unicode":  "unicode",
		"UTF-16":   "unicode",
		"UTF16LE":  "unicode",
	}
	for in, want := range cases {
		w, err := NewInboxWriter(t.TempDir(), in, nil)
		if err != nil {
			t.Errorf("формат %q: %v", in, err)
			continue
		}
		if w.Format() != want {
			t.Errorf("формат %q → %q, ожидалось %q", in, w.Format(), want)
		}
	}
}

// TestWriteInboxBasicUTF8 — тело файла это plain text,
// метаданные — только в имени.
func TestWriteInboxBasicUTF8(t *testing.T) {
	dir := t.TempDir()
	w := mustWriter(t, dir, "utf8")
	ts := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)

	full, err := w.Write(InboxMessage{
		Peer:     "+37121234567",
		Text:     "Привет",
		Encoding: "UCS-2",
		Received: ts,
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Имя файла содержит все метаданные.
	if filepath.Base(full) != "IN20261015_120034_00_+37121234567_00.txt" {
		t.Errorf("имя файла: %q", filepath.Base(full))
	}

	// Тело файла — только текст (UTF-8 без BOM).
	raw, _ := os.ReadFile(full)
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		t.Error("UTF-8 BOM не ожидался")
	}
	if string(raw) != "Привет" {
		t.Errorf("тело: %q, ожидалось %q", raw, "Привет")
	}

	// Не должно быть Key = Value.
	if key, ok := hasKeyValue(string(raw)); ok {
		t.Errorf("тело содержит %q (должно быть plain text):\n%s",
			key, raw)
	}
}

func TestWriteInboxStandardEqualsUTF8(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)

	w8 := mustWriter(t, dir, "utf8")
	p1, err := w8.Write(InboxMessage{Peer: "+371", Text: "x", Received: ts})
	if err != nil {
		t.Fatalf("utf8: %v", err)
	}
	wStd := mustWriter(t, dir, "standard")
	p2, err := wStd.Write(InboxMessage{Peer: "+371", Text: "x", Received: ts})
	if err != nil {
		t.Fatalf("standard: %v", err)
	}

	b1, _ := os.ReadFile(p1)
	b2, _ := os.ReadFile(p2)
	if string(b1) != string(b2) {
		t.Errorf("utf8 и standard дали разное содержимое:\n%q\n%q", b1, b2)
	}
}

// TestWriteInboxUnicode — тело в UTF-16LE с BOM, но всё ещё
// plain text (без Key = Value).
func TestWriteInboxUnicode(t *testing.T) {
	dir := t.TempDir()
	w := mustWriter(t, dir, "unicode")
	ts := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)

	full, err := w.Write(InboxMessage{
		Peer:     "+37121234567",
		Text:     "Привет",
		Encoding: "UCS-2",
		Received: ts,
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, _ := os.ReadFile(full)
	if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xFE {
		t.Fatalf("нет BOM UTF-16LE, первые байты: %x", raw[:4])
	}

	// Roundtrip через DecodeText: тело — это просто текст.
	content, err := DecodeText(raw)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if content != "Привет" {
		t.Errorf("после декодирования: %q, ожидалось %q",
			content, "Привет")
	}
	if key, ok := hasKeyValue(content); ok {
		t.Errorf("тело содержит %q (должно быть plain text):\n%s",
			key, content)
	}
}

// TestWriteInboxMultipart — sequence в имени файла, UDH в теле
// НЕ пишется (gammu-smsd так не делает).
func TestWriteInboxMultipart(t *testing.T) {
	dir := t.TempDir()
	w := mustWriter(t, dir, "utf8")
	ts := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)

	full, err := w.Write(InboxMessage{
		Peer:     "+37121234567",
		Text:     "part 2",
		Encoding: "GSM-7",
		Received: ts,
		Multipart: &Multipart{
			Reference: 42,
			Total:     3,
			Sequence:  2,
		},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// sequence в имени файла.
	if !strings.HasSuffix(full, "_02.txt") {
		t.Errorf("sequence не в имени: %q", full)
	}
	// Тело — plain text, без UDH.
	raw, _ := os.ReadFile(full)
	if string(raw) != "part 2" {
		t.Errorf("тело: %q", raw)
	}
	if strings.Contains(string(raw), "UDH") {
		t.Errorf("UDH не должен быть в теле:\n%s", raw)
	}
}

func TestWriteInboxSerialIncrements(t *testing.T) {
	dir := t.TempDir()
	w := mustWriter(t, dir, "utf8")
	ts := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)

	var names []string
	for i := 0; i < 3; i++ {
		full, err := w.Write(InboxMessage{
			Peer: "+371", Text: "x", Received: ts,
		})
		if err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
		names = append(names, filepath.Base(full))
	}

	want := []string{
		"IN20261015_120034_00_+371_00.txt",
		"IN20261015_120034_01_+371_00.txt",
		"IN20261015_120034_02_+371_00.txt",
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("[%d] %q, ожидалось %q", i, names[i], want[i])
		}
	}
}

func TestWriteInboxRespectsExternalFile(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)

	busy := filepath.Join(dir, "IN20261015_120034_00_+371_00.txt")
	if err := os.WriteFile(busy, []byte("occupied"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	w := mustWriter(t, dir, "utf8")
	full, err := w.Write(InboxMessage{Peer: "+371", Text: "x", Received: ts})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.Contains(filepath.Base(full), "_01_") {
		t.Errorf("ожидался serial _01, получено %q", filepath.Base(full))
	}
}

// TestWriteInboxRoundtripParse — WriteInbox → ParseFile.
// Тело — plain text, значит ParseFile использует fallback
// и берёт всё содержимое как m.Text.
func TestWriteInboxRoundtripParse(t *testing.T) {
	dir := t.TempDir()
	w := mustWriter(t, dir, "utf8")
	ts := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)

	full, err := w.Write(InboxMessage{
		Peer:     "+37121234567",
		Text:     "Привет, мир!",
		Encoding: "UCS-2",
		Received: ts,
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, _ := os.ReadFile(full)
	content, err := DecodeText(raw)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	m, err := ParseFile(content)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	text, src := m.EffectiveText()
	if text != "Привет, мир!" || src != "plain" {
		t.Errorf("roundtrip: text=%q src=%q", text, src)
	}
	// Number из тела не восстанавливается (он в имени файла).
	if m.Number != "" {
		t.Errorf("Number=%q, ожидалось пусто (адресат в имени файла)",
			m.Number)
	}
	// Из имени файла Number извлекается через ParseIncomingFileName.
	fn, err := ParseIncomingFileName(filepath.Base(full))
	if err != nil {
		t.Fatalf("ParseIncomingFileName: %v", err)
	}
	if fn.Sender != "+37121234567" {
		t.Errorf("Sender из имени: %q", fn.Sender)
	}
	if !fn.Time.Equal(ts) {
		t.Errorf("Time из имени: %v, ожидалось %v", fn.Time, ts)
	}
}

// TestWriteInboxQuotesInText — кавычки в тексте сохраняются
// как есть (plain text, без escaping).
func TestWriteInboxQuotesInText(t *testing.T) {
	dir := t.TempDir()
	w := mustWriter(t, dir, "utf8")
	ts := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)

	full, err := w.Write(InboxMessage{
		Peer:     "+371",
		Text:     `say "hi"`,
		Received: ts,
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, _ := os.ReadFile(full)
	if string(raw) != `say "hi"` {
		t.Errorf("тело: %q, ожидалось %q", raw, `say "hi"`)
	}
}

func TestWriteInboxEmptyPeer(t *testing.T) {
	dir := t.TempDir()
	w := mustWriter(t, dir, "utf8")
	if _, err := w.Write(InboxMessage{Text: "x"}); err == nil {
		t.Fatal("ожидалась ошибка для пустого peer")
	}
}

func TestWriteInboxEmptyTextOK(t *testing.T) {
	dir := t.TempDir()
	w := mustWriter(t, dir, "utf8")
	ts := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)

	full, err := w.Write(InboxMessage{Peer: "+371", Received: ts})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, _ := os.ReadFile(full)
	// Пустой текст — пустой файл.
	if len(raw) != 0 {
		t.Errorf("тело: %q, ожидалось пусто", raw)
	}
}

// TestWriteInboxSMSCNotInBody — SMSC в теле не пишется
// (gammu-smsd так не делает).
func TestWriteInboxSMSCNotInBody(t *testing.T) {
	dir := t.TempDir()
	w := mustWriter(t, dir, "utf8")
	ts := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)

	full, err := w.Write(InboxMessage{
		Peer:     "+371",
		Text:     "x",
		SMSC:     "+3725509911",
		Received: ts,
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, _ := os.ReadFile(full)
	if strings.Contains(string(raw), "SMSC") {
		t.Errorf("SMSC не должен быть в теле:\n%s", raw)
	}
	if string(raw) != "x" {
		t.Errorf("тело: %q, ожидалось %q", raw, "x")
	}
}
