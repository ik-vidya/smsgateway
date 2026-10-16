// Package gammu, файл decode.go — определение кодировки файла.
package gammu

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"unicode/utf16"
	"unicode/utf8"
)

// DecodeText декодирует содержимое файла Gammu в строку Go.
//
// Порядок определения кодировки:
//
//  1. BOM UTF-8 (EF BB BF)          → UTF-8, BOM отбрасывается.
//  2. BOM UTF-16LE (FF FE)          → UTF-16LE, BOM отбрасывается.
//  3. BOM UTF-16BE (FE FF)          → UTF-16BE, BOM отбрасывается.
//  4. BOM нет, валидный UTF-8 без NUL → UTF-8.
//  5. Иначе                         → UTF-16LE (типичная запись
//     Windows-версии Gammu в UCS-2 без BOM).
//
// Проверка на NUL (0x00) критична: байты UTF-16LE-строки
// «Number = ...» — это 4E 00 75 00 ... — формально валидный
// UTF-8 (NUL — допустимый кодпоинт). Без этой проверки
// utf8.Valid() возвращал бы true, и fallback в UTF-16 не
// срабатывал. Текст SMS и конфиг Gammu NUL не содержат, так
// что NUL-байт в файле — надёжный признак UTF-16.
func DecodeText(b []byte) (string, error) {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return string(b[3:]), nil
	}
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		return decodeUTF16(b[2:], binary.LittleEndian)
	}
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		return decodeUTF16(b[2:], binary.BigEndian)
	}
	if utf8.Valid(b) && !bytes.ContainsRune(b, 0) {
		return string(b), nil
	}
	return decodeUTF16(b, binary.LittleEndian)
}

// DecodeTextAs декодирует файл в заданной кодировке.
//
// Поддерживаются: "utf8", "utf-8", "standard", "unicode",
// "utf16le", "utf16be". Неизвестная или пустая кодировка
// равнозначна автоопределению (DecodeText). "detail" возвращает
// ошибку — SMS Backup XML не поддерживается.
func DecodeTextAs(b []byte, encoding string) (string, error) {
	switch normalizeEncoding(encoding) {
	case "", "auto":
		return DecodeText(b)
	case "utf8":
		return decodeUTF8Strict(b)
	case "utf16le":
		return decodeUTF16(stripBOM(b), binary.LittleEndian)
	case "utf16be":
		return decodeUTF16(stripBOM(b), binary.BigEndian)
	case "detail":
		return "", fmt.Errorf(
			"формат SMS Backup XML (detail) не поддерживается")
	}
	return "", fmt.Errorf("неизвестная кодировка %q", encoding)
}

func normalizeEncoding(s string) string {
	switch s {
	case "utf8", "utf-8", "standard", "UTF-8", "UTF8":
		return "utf8"
	case "unicode", "utf16", "utf-16", "utf16le", "utf-16le":
		return "utf16le"
	case "utf16be", "utf-16be":
		return "utf16be"
	case "detail", "smsbackup", "SMSBackup":
		return "detail"
	case "", "auto":
		return ""
	}
	return s
}

func decodeUTF8Strict(b []byte) (string, error) {
	if !utf8.Valid(b) {
		return "", fmt.Errorf("содержимое не является валидным UTF-8")
	}
	if bytes.ContainsRune(b, 0) {
		// NUL-байт — почти наверняка UTF-16 без BOM, а не UTF-8.
		return "", fmt.Errorf(
			"содержимое содержит NUL — вероятно, это UTF-16, а не UTF-8")
	}
	// Отбрасываем BOM, если он есть.
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:]
	}
	return string(b), nil
}

func stripBOM(b []byte) []byte {
	if len(b) >= 2 {
		if (b[0] == 0xFF && b[1] == 0xFE) || (b[0] == 0xFE && b[1] == 0xFF) {
			return b[2:]
		}
	}
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// decodeUTF16 собирает строку из пары байт на символ.
// Порядок байт (LE/BE) задаётся вызывающим.
func decodeUTF16(b []byte, order binary.ByteOrder) (string, error) {
	if len(b)%2 != 0 {
		return "", fmt.Errorf("UTF-16: нечётная длина %d байт", len(b))
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = order.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u16)), nil
}
