package gammu

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

func utf16Bytes(s string, order binary.ByteOrder, bom bool) []byte {
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

func TestDecodeUTF8NoBOM(t *testing.T) {
	in := []byte("Number = \"+37121234567\"\nTextDecoded = \"Привет\"\n")
	got, err := DecodeText(in)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if string(in) != got {
		t.Errorf("got %q, want %q", got, string(in))
	}
}

func TestDecodeUTF8BOM(t *testing.T) {
	bom := []byte{0xEF, 0xBB, 0xBF}
	in := append(bom, []byte("Number = \"+371\"\n")...)
	got, err := DecodeText(in)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if got != "Number = \"+371\"\n" {
		t.Errorf("got %q", got)
	}
}

func TestDecodeUTF16LEWithBOM(t *testing.T) {
	want := "Number = \"+37121234567\"\nTextDecoded = \"Привет\"\n"
	in := utf16Bytes(want, binary.LittleEndian, true)
	got, err := DecodeText(in)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecodeUTF16BEWithBOM(t *testing.T) {
	want := "Number = \"+37121234567\"\nTextDecoded = \"Привет\"\n"
	in := utf16Bytes(want, binary.BigEndian, true)
	got, err := DecodeText(in)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecodeUTF16LENoBOM(t *testing.T) {
	// Байты невалидны как UTF-8 → fallback в UTF-16LE.
	want := "Number = \"+37121234567\"\n"
	in := utf16Bytes(want, binary.LittleEndian, false)
	got, err := DecodeText(in)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecodeUTF16OddLength(t *testing.T) {
	if _, err := decodeUTF16([]byte{0x41}, binary.LittleEndian); err == nil {
		t.Fatal("ожидалась ошибка для нечётной длины")
	}
}

func TestDecodeTextAsUTF8(t *testing.T) {
	in := []byte("Number = \"+371\"\n")
	got, err := DecodeTextAs(in, "utf8")
	if err != nil {
		t.Fatalf("DecodeTextAs: %v", err)
	}
	if got != string(in) {
		t.Errorf("got %q", got)
	}
}

func TestDecodeTextAsStandardEqualsUTF8(t *testing.T) {
	in := []byte("Number = \"+371\"\n")
	a, _ := DecodeTextAs(in, "utf8")
	b, _ := DecodeTextAs(in, "standard")
	if a != b {
		t.Errorf("standard=%q != utf8=%q", b, a)
	}
}

func TestDecodeTextAsUnicode(t *testing.T) {
	want := "Number = \"+371\"\n"
	in := utf16Bytes(want, binary.LittleEndian, true)
	got, err := DecodeTextAs(in, "unicode")
	if err != nil {
		t.Fatalf("DecodeTextAs: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecodeTextAsDetailRejected(t *testing.T) {
	if _, err := DecodeTextAs([]byte("<xml/>"), "detail"); err == nil {
		t.Fatal("ожидалась ошибка для detail")
	}
}

func TestDecodeTextAsUnknownEncoding(t *testing.T) {
	if _, err := DecodeTextAs([]byte("x"), "cp1252"); err == nil {
		t.Fatal("ожидалась ошибка для неизвестной кодировки")
	}
}

func TestDecodeTextAsInvalidUTF8Strict(t *testing.T) {
	// Валидный UTF-16LE без BOM, переданный как "utf8".
	raw := utf16Bytes("abc", binary.LittleEndian, false)
	if _, err := DecodeTextAs(raw, "utf8"); err == nil {
		t.Fatal("ожидалась ошибка: содержимое не UTF-8")
	}
}
