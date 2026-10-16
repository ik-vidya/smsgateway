// Package gammu, файл parse.go — разбор содержимого Gammu-файла.
package gammu

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ik-vidya/sim800ctl/pkg/pdu"
)

// Message — распарсенное содержимое одного Gammu-файла.
type Message struct {
	SMSC        string
	Number      string
	Text        string
	TextDecoded string
	Coding      string
	UDH         string
	Class       int
	Sent        time.Time
	Received    time.Time

	// Fields — все сырые поля файла, включая не задействованные.
	Fields map[string]string
}

// ParseFile разбирает содержимое файла.
//
// Формат — построчный "Key = Value". Кавычки вокруг значений
// опциональны. Пустые строки, комментарии (#) и секции ([...])
// игнорируются.
func ParseFile(content string) (*Message, error) {
	m := &Message{Fields: make(map[string]string)}

	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "[") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		m.Fields[key] = unquote(val)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("чтение содержимого: %w", err)
	}

	m.SMSC = m.Fields["SMSC"]
	m.Number = m.Fields["Number"]
	m.Text = m.Fields["Text"]
	m.TextDecoded = m.Fields["TextDecoded"]
	m.Coding = m.Fields["Coding"]
	m.UDH = m.Fields["UDH"]

	if v, ok := m.Fields["Class"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			m.Class = n
		}
	}
	m.Sent = parseTimestamp(m.Fields["Sent"])
	m.Received = parseTimestamp(m.Fields["Received"])

	return m, nil
}

// unquote снимает обрамляющие кавычки, если они есть.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		time.RFC3339,
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

// EffectiveText возвращает итоговый текст сообщения и источник.
//
// Приоритет:
//  1. TextDecoded — если задан.
//  2. Text как hex-PDU — если looksLikeHexPDU() и pdu.Decode справляется.
//  3. Text как обычный текст.
//
// Второе значение — "text_decoded" | "pdu" | "plain". Никогда
// не возвращает ошибку: если hex-подобный Text не декодируется
// как PDU, он трактуется как обычный текст. Gammu-файлы приходят
// из внешнего мира, падать на них нельзя.
func (m *Message) EffectiveText() (text, source string) {
	if m.TextDecoded != "" {
		return m.TextDecoded, "text_decoded"
	}
	if looksLikeHexPDU(m.Text) {
		if t, ok := decodePDUSafe(m.Text); ok {
			return t, "pdu"
		}
	}
	return m.Text, "plain"
}

// looksLikeHexPDU: непустая строка чётной длины, состоящая
// только из hex-символов (0-9, A-F, a-f).
//
// Порога по длине нет намеренно: короткие hex-строки типа "00AA"
// должны распознаваться как потенциальные PDU. Если pdu.Decode
// на них упадёт (а он упадёт — это не валидный PDU), вызывающий
// EffectiveText уйдёт в fallback "plain". Строки типа "hello"
// отсекаются тем, что содержат не-hex символы.
func looksLikeHexPDU(s string) bool {
	if len(s) == 0 || len(s)%2 != 0 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') ||
			(r >= 'A' && r <= 'F') ||
			(r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// decodePDUSafe вызывает pdu.Decode с защитой от паники.
// Сторонний декодер может паниковать на мусоре — импорт файлов
// не должен из-за этого ронять сервис.
func decodePDUSafe(hex string) (text string, ok bool) {
	defer func() {
		if recover() != nil {
			text, ok = "", false
		}
	}()
	d, err := pdu.Decode(hex)
	if err != nil {
		return "", false
	}
	return d.Text, true
}
