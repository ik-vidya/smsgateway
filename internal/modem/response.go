// Package modem, файл response.go — типы ответа на AT-команду.
package modem

import (
	"errors"
	"fmt"
	"strings"
)

// Response — результат выполнения AT-команды.
type Response struct {
	Command string   // исходная команда (без \r)
	Lines   []string // все строки, включая терминатор
}

// OK — команда завершилась успешно.
func (r Response) OK() bool {
	if len(r.Lines) == 0 {
		return false
	}
	return r.Lines[len(r.Lines)-1] == "OK"
}

// Payload возвращает строки ответа без терминатора.
func (r Response) Payload() []string {
	if len(r.Lines) == 0 {
		return nil
	}
	return r.Lines[:len(r.Lines)-1]
}

// String возвращает payload, склеенный через \n.
func (r Response) String() string {
	return strings.Join(r.Payload(), "\n")
}

// Error — ошибка выполнения AT-команды.
type ATError struct {
	Line string
}

func (e *ATError) Error() string {
	return InterpretErrorLine(e.Line)
}

// ==================== классификация ====================

type terminator int

const (
	termNone terminator = iota
	termOK
	termError
)

// classifyTerminator проверяет, является ли строка терминатором.
func classifyTerminator(line string) (terminator, bool) {
	switch {
	case line == "OK":
		return termOK, true
	case line == "ERROR":
		return termError, true
	case strings.HasPrefix(line, "+CME ERROR:"):
		return termError, true
	case strings.HasPrefix(line, "+CMS ERROR:"):
		return termError, true
	}
	return termNone, false
}

func errorFromTerminator(line string) error {
	return &ATError{Line: line}
}

// IsTimeout сообщает, что ошибка — таймаут ожидания.
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "таймаут")
}

// IsATError — ошибка от модема (+CME/+CMS/ERROR).
func IsATError(err error) bool {
	var e *ATError
	return errors.As(err, &e)
}

// nopLogger — логгер-заглушка.
type nopLogger struct{}

func (nopLogger) Debug(string, ...any) {}
func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// ==================== интерпретация ошибок ====================

// cmеErrors — расшифровки CME-кодов (3GPP TS 27.007).
var cmeErrors = map[int]string{
	0:   "phone failure",
	3:   "operation not allowed",
	10:  "SIM not inserted",
	11:  "SIM PIN required",
	12:  "SIM PUK required",
	13:  "SIM failure",
	14:  "SIM busy",
	15:  "SIM wrong",
	16:  "incorrect password",
	17:  "SIM PIN2 required",
	18:  "SIM PUK2 required",
	20:  "memory full",
	21:  "invalid index",
	22:  "not found",
	23:  "memory failure",
	24:  "text string too long",
	25:  "invalid characters in text string",
	30:  "no network service",
	31:  "network timeout",
	32:  "network not allowed — emergency calls only",
	40:  "network personalization PIN required",
	100: "unknown (команда не поддерживается модемом)",
}

// cmsErrors — расшифровки CMS-кодов (3GPP TS 27.005).
var cmsErrors = map[int]string{
	300: "ME failure",
	301: "SMS service of ME reserved",
	302: "operation not allowed",
	303: "operation not supported",
	304: "invalid PDU mode parameter",
	305: "invalid text mode parameter",
	310: "SIM not inserted",
	311: "SIM PIN required",
	312: "PH-SIM PIN required",
	313: "SIM failure",
	314: "SIM busy",
	315: "SIM wrong",
	320: "memory failure",
	321: "invalid memory index",
	322: "memory full",
	330: "SMSC address unknown",
	331: "no network service",
	332: "network timeout",
	500: "unknown error (часто: нет баланса или SMS-центр недоступен)",
	// SIMCom proprietary.
	2171: "нет сети (SIMCom): модем не зарегистрирован",
	2172: "нет сети (SIMCom): сеть недоступна",
}

// simcomTextErrors — текстовые коды SIM800.
var simcomTextErrors = map[string]string{
	"Temporary failure":        "временный сбой: SMSC недоступен (повторить)",
	"No network service":       "нет сети: модем не зарегистрирован",
	"Network not allowed":      "сеть не разрешена для этой SIM",
	"Unassigned number":        "номер не назначен",
	"Operator barred":          "оператор заблокирован",
	"Memory capacity exceeded": "память SIM/модема переполнена",
	"Memory failure":           "ошибка памяти модема",
	"SIM failure":              "сбой SIM-карты",
}

// InterpretErrorLine расшифровывает строку вида "+CME ERROR: 10",
// "+CMS ERROR: 500" или текст SIM800.
func InterpretErrorLine(line string) string {
	line = strings.TrimSpace(line)

	for _, prefix := range []string{"+CME ERROR:", "+CMS ERROR:"} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, prefix))

		if code, err := parseAtoi(body); err == nil {
			if prefix == "+CMS ERROR:" {
				if txt, ok := cmsErrors[code]; ok {
					return prefix + " " + body + " — " + txt
				}
			} else {
				if txt, ok := cmeErrors[code]; ok {
					return prefix + " " + body + " — " + txt
				}
			}
		}
		if txt, ok := simcomTextErrors[body]; ok {
			return prefix + " " + body + " — " + txt
		}
	}
	return line
}

func parseAtoi(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errors.New("пусто")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("не число: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}