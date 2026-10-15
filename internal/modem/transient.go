// Package modem, файл transient.go — классификация ошибок отправки
// на транзиентные (имеет смысл повторить) и постоянные.
package modem

import (
	"errors"
	"fmt"
	"strings"
)

// IsTransientError возвращает true, если ошибку отправки имеет смысл
// повторить: SMSC временно недоступен, сеть занята, таймаут.
//
// Транзиентными считаются:
//   - таймаут команды (IsTimeout);
//   - текстовые ответы SIM800: "Temporary failure", "No network service",
//     "Network timeout", "Network busy";
//   - числовые CMS-коды: 38 (network out of order), 41 (temporary failure),
//     42 (congestion), 47 (resources unavailable), 331 (no network service),
//     332 (network timeout), 500 (unknown — часто транзиентный).
func IsTransientError(err error) bool {
	if err == nil {
		return false
	}
	// Таймаут — транзиентная.
	if IsTimeout(err) {
		return true
	}
	// Ошибки отмены контекста — не транзиентные.
	if errors.Is(err, errors.ErrUnsupported) {
		return false
	}

	var atErr *ATError
	if !errors.As(err, &atErr) {
		return false
	}

	s := strings.ToLower(atErr.Line)

	// Текстовые ответы SIM800.
	for _, marker := range []string{
		"temporary failure",
		"no network service",
		"network timeout",
		"network busy",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}

	// Числовые CMS-коды.
	for _, code := range []int{38, 41, 42, 47, 331, 332, 500} {
		if strings.Contains(s, fmt.Sprintf("+cms error: %d", code)) {
			return true
		}
	}

	return false
}
