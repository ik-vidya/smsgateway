package profiles

import (
	"context"
	"strings"
)

// Huawei — профиль для модемов Huawei (MS2131, MU609, E3372,
// E8372, и т.п.).
//
// Специфических обходов прошивок пока не известно. Huawei-модемы
// часто имеют «хитрые» AT^HUAWEI-команды для переключения режимов,
// но для отправки SMS/приёма по 3GPP TS 27.007 стандартных
// AT-команд достаточно. Профиль — минимальная обёртка над Generic.
//
// Некоторые модели (E3372 «HiLink», E8372) при включении работают
// как USB-модем в режиме «виртуальный Ethernet» и не отдают
// последовательный порт для AT-команд. Это вопрос конфигурации
// устройства, а не профиля.
type Huawei struct{}

func (Huawei) Name() string { return "huawei" }

func (Huawei) Match(info Info) bool {
	m := strings.ToUpper(info.Manufacturer)
	mod := strings.ToUpper(info.Model)
	if strings.Contains(m, "HUAWEI") {
		return true
	}
	// Типичные модели: MS2131, MU609, E3372, E8372, E353.
	for _, prefix := range []string{"MS2", "MU6", "E3", "E8", "E5"} {
		if strings.Contains(mod, prefix) {
			return true
		}
	}
	return false
}

func (Huawei) Workarounds() Workarounds {
	return Generic{}.Workarounds()
}

func (Huawei) PreInitHook(ctx context.Context, m ModemHook) error {
	return Generic{}.PreInitHook(ctx, m)
}

func (Huawei) PreSendHook(ctx context.Context, m ModemHook) error {
	return Generic{}.PreSendHook(ctx, m)
}
