package profiles

import (
	"context"
	"strings"
)

// Quectel — профиль для модемов Quectel (EC21, EC25, M95, MC60,
// BG95 и т.п.).
//
// Специфических обходов прошивок пока не известно: модемы Quectel
// ведут себя по 3GPP TS 27.007. Профиль — минимальная обёртка над
// Generic, чтобы пользователь мог явно указать «quectel» в
// MODEM_PROFILE, и чтобы Auto() мог выбрать его по Manufacturer.
//
// Когда появятся тесты на реальном железе — добавим сюда обходы
// (по аналогии с SIM800).
type Quectel struct{}

func (Quectel) Name() string { return "quectel" }

func (Quectel) Match(info Info) bool {
	m := strings.ToUpper(info.Manufacturer)
	mod := strings.ToUpper(info.Model)
	if strings.Contains(m, "QUECTEL") {
		return true
	}
	// Типичные модели: EC21, EC25, M95, MC60, BG95, EG25.
	for _, prefix := range []string{"EC", "EG", "M9", "MC6", "BG9"} {
		if strings.Contains(mod, prefix) {
			return true
		}
	}
	return false
}

func (Quectel) Workarounds() Workarounds {
	return Generic{}.Workarounds()
}

func (Quectel) PreInitHook(ctx context.Context, m ModemHook) error {
	return Generic{}.PreInitHook(ctx, m)
}

func (Quectel) PreSendHook(ctx context.Context, m ModemHook) error {
	return Generic{}.PreSendHook(ctx, m)
}
