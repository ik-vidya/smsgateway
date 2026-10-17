package profiles

import (
	"context"
	"strings"
)

// Telit — профиль для модемов Telit (GL865, HE910, LE910, ME910,
// и т.п.).
//
// Специфических обходов прошивок пока не известно: модемы Telit
// ведут себя по 3GPP TS 27.007. Профиль — минимальная обёртка над
// Generic.
type Telit struct{}

func (Telit) Name() string { return "telit" }

func (Telit) Match(info Info) bool {
	m := strings.ToUpper(info.Manufacturer)
	mod := strings.ToUpper(info.Model)
	if strings.Contains(m, "TELIT") {
		return true
	}
	// Типичные модели: GL865, HE910, LE910, ME910.
	for _, prefix := range []string{"GL8", "HE9", "LE9", "ME9"} {
		if strings.Contains(mod, prefix) {
			return true
		}
	}
	return false
}

func (Telit) Workarounds() Workarounds {
	return Generic{}.Workarounds()
}

func (Telit) PreInitHook(ctx context.Context, m ModemHook) error {
	return Generic{}.PreInitHook(ctx, m)
}

func (Telit) PreSendHook(ctx context.Context, m ModemHook) error {
	return Generic{}.PreSendHook(ctx, m)
}
