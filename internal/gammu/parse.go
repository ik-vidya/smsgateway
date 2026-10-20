// Package gammu, файл parse.go — разбор содержимого Gammu-файла.
//
// Поддерживается два формата:
//
//  1. Классический gammu: построчный "Key = Value" (SMSC, Number,
//     Text, TextDecoded, Coding, UDH, Class, Sent, Received).
//  2. Plain text: тело SMS как есть, без структуры (используется
//     rssmspool и очень старыми gammu-smsd с TransmitFormat=text).
//
// Формат определяется автоматически по содержимому (см.
// looksLikeGammu). Кодировка снимается раньше, в DecodeText:
// ParseFile получает уже нормализованную UTF-8 строку.
package gammu

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.vidya.lv/vidya/sim800ctl/pkg/pdu"
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

// knownGammuKeys — список известных ключей gammu-формата.
//
// Если в файле нет ни одного из них — это plain text
// (см. looksLikeGammu). «Посторонние» Key = Value (например,
// «Body = text» в plain-сообщении) не считаются признаком
// gammu-формата.
var knownGammuKeys = []string{
	"SMSC", "Number", "Text", "TextDecoded",
	"Coding", "UDH", "Class", "Sent", "Received",
}

// ParseFile разбирает содержимое файла.
//
// Формат "Key = Value": кавычки вокруг значений опциональны,
// пустые строки, комментарии (#) и секции ([...]) игнорируются.
//
// Fallback: если ни одного известного gammu-ключа не найдено,
// всё содержимое считается телом SMS (m.Text). Адресат в этом
// случае берётся из имени файла (Manager.processFile).
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

	// Fallback на plain text: если в файле нет ни одного
	// известного gammu-ключа — всё содержимое это текст SMS.
	//
	// Проверяем известные ключи (а не просто наличие «=»):
	// plain-текст может содержать «=» (например, «Body = text»),
	// и тогда fallback не должен ломаться.
	//
	// Кодировка здесь уже UTF-8 (DecodeText вызван до ParseFile),
	// поэтому fallback одинаково работает для UTF-8, UTF-16LE/BE
	// с BOM и UTF-16LE без BOM.
	if !m.looksLikeGammu() {
		if trimmed := strings.TrimSpace(content); trimmed != "" {
			m.Text = trimmed
		}
	}

	return m, nil
}

// looksLikeGammu проверяет, есть ли в файле хотя бы один
// известный gammu-ключ.
func (m *Message) looksLikeGammu() bool {
	for _, key := range knownGammuKeys {
		if _, ok := m.Fields[key]; ok {
			return true
		}
	}
	return false
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
// как PDU, он трактуется как обычный текст.
//
// В plain-режиме (файл без Key = Value) ParseFile записывает
// всё содержимое в m.Text, и EffectiveText вернёт его как
// "plain". Адресат в этом случае берётся из имени файла.
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
