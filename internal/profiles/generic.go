package profiles

import "context"

// Generic — базовый профиль. Только общие AT-команды из 3GPP TS 27.007,
// без обходов. Подходит для большинства современных модемов
// (Quectel, Telit, Huawei), у которых нет известных багов прошивок,
// специфичных для нашей работы.
type Generic struct{}

func (Generic) Name() string { return "generic" }

func (Generic) Match(Info) bool { return false } // никогда не выбирается автоматически

func (Generic) Workarounds() Workarounds {
	return Workarounds{}
}

func (Generic) PreInitHook(context.Context, ModemHook) error { return nil }

func (Generic) PreSendHook(context.Context, ModemHook) error { return nil }
