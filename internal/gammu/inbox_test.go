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
	if filepath.Base(full) != "IN20261015_120034_00_+37121234567_00.txt" {
		t.Errorf("имя файла: %q", filepath.Base(full))
	}

	raw, _ := os.ReadFile(full)
	body := string(raw)

	// UTF-8 без BOM.
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		t.Error("UTF-8 BOM не ожидался")
	}
	for _, want := range []string{
		`Number = "+37121234567"`,
		`TextDecoded = "Привет"`,
		`Coding = "Unicode_No_Compression"`,
		`Received = "2026-10-15 12:00:34"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("нет строки %q в:\n%s", want, body)
		}
	}
	if strings.Contains(body, "SMSC =") {
		t.Error("SMSC не должен писаться при пустом значении")
	}
	if strings.Contains(body, "UDH =") {
		t.Error("UDH не должен писаться без multipart")
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
	// serial для того же timestamp будет 1 (0 уже занят).
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

	// Roundtrip через DecodeText.
	content, err := DecodeText(raw)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if !strings.Contains(content, `TextDecoded = "Привет"`) {
		t.Errorf("после декодирования нет строки с текстом:\n%s", content)
	}
}

func TestWriteInboxMultipartUDH(t *testing.T) {
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
	if !strings.HasSuffix(full, "_02.txt") {
		t.Errorf("sequence не в имени: %q", full)
	}
	raw, _ := os.ReadFile(full)
	body := string(raw)
	if !strings.Contains(body, `UDH = "0500032A0302"`) {
		t.Errorf("UDH неверный:\n%s", body)
	}
	if !strings.Contains(body, `Coding = "Default_No_Compression"`) {
		t.Errorf("Coding для GSM-7 неверный:\n%s", body)
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

	// Занять _00 снаружи (не через writer).
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
	if text != "Привет, мир!" || src != "text_decoded" {
		t.Errorf("roundtrip: text=%q src=%q", text, src)
	}
	if m.Number != "+37121234567" {
		t.Errorf("Number=%q", m.Number)
	}
	if !m.Received.Equal(ts) {
		t.Errorf("Received=%v, ожидалось %v", m.Received, ts)
	}
}

func TestWriteInboxQuoteInsideText(t *testing.T) {
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
	body := string(raw)

	// Внутренние кавычки заменены на одинарные — файл остался
	// структурно валидным (значение обёрнуто в двойные кавычки).
	if !strings.Contains(body, `TextDecoded = "say 'hi'"`) {
		t.Errorf("кавычки не нормализованы:\n%s", body)
	}

	// Парсер снова прочитает.
	m, _ := ParseFile(body)
	if m.TextDecoded != `say 'hi'` {
		t.Errorf("после парсинга: %q", m.TextDecoded)
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
	// Пустой текст — валидный сценарий (некоторые SMS пусты).
	dir := t.TempDir()
	w := mustWriter(t, dir, "utf8")
	ts := time.Date(2026, 10, 15, 12, 0, 34, 0, time.Local)

	full, err := w.Write(InboxMessage{Peer: "+371", Received: ts})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, _ := os.ReadFile(full)
	if !strings.Contains(string(raw), `TextDecoded = ""`) {
		t.Errorf("ожидалась пустая TextDecoded:\n%s", raw)
	}
}

func TestWriteInboxSMSC(t *testing.T) {
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
	if !strings.Contains(string(raw), `SMSC = "+3725509911"`) {
		t.Errorf("SMSC не записан:\n%s", raw)
	}
}
