// Package profiles — профили AT-модемов: набор специфичных команд,
// обходов багов прошивок и правил разбора URC.
//
// Профиль задаётся в конфиге (MODEM_PROFILE). "generic" — только
// общие AT-команды, без специфичных надстроек. Специфичные профили
// (sim800, quectel, telit) включают дополнительные обходы.
package profiles

import (
	"context"
	"fmt"
)

// Info — идентификация модема, полученная через ATI/AT+CGMI/AT+CGMM/AT+CGMR.
type Info struct {
	Manufacturer string
	Model        string
	Revision     string
}

// Workarounds — флаги, включающие обходы багов прошивок.
type Workarounds struct {
	// ForceUCS2ForAllSMS — всегда кодировать SMS как UCS-2, даже
	// если текст укладывается в GSM-7. Нужно для SIM800L: иначе
	// модем искажает GSM-7 при чтении в text mode.
	ForceUCS2ForAllSMS bool

	// UseCSMPForSRR — перед каждой отправкой выставлять
	// AT+CSMP=49,167,0,0. Нужно для SIM800L: CSMP перебивает SRR
	// из PDU, и без этого delivery reports не приходят.
	UseCSMPForSRR bool

	// AlignReadStorage — перед чтением SMS выравнивать read-storage
	// с receive-storage (AT+CPMS). Нужно для SIM800L: модем может
	// сохранять входящие в SM, а читать из ME.
	AlignReadStorage bool

	// PinAttemptsAT — AT-команда для получения числа оставшихся
	// попыток PIN. Для SIM800L — "AT+SPIC". Пусто — команда не
	// поддерживается.
	PinAttemptsAT string
}

// ModemHook — минимальный интерфейс для хуков профиля: выполнить
// AT-команду и залогировать. Реализуется пакетом internal/modem.
type ModemHook interface {
	Exec(ctx context.Context, cmd string) (string, error)
	Log(msg string)
}

// Profile — интерфейс профиля модема.
type Profile interface {
	// Name — короткое имя ("sim800", "generic", ...).
	Name() string

	// Match — подходит ли профиль для данного модема.
	// Используется в команде probe для автоподбора.
	Match(info Info) bool

	// Workarounds — активные обходы.
	Workarounds() Workarounds

	// PreInitHook — вызывается до общих команд инициализации.
	// Может выставить специфичные параметры (CFUN, CPMS, CNMI...).
	// Ошибка не критична: общая инициализация продолжается.
	PreInitHook(ctx context.Context, m ModemHook) error

	// PreSendHook — вызывается перед каждой отправкой SMS.
	// Например, SIM800L-профиль тут досылает AT+CSMP.
	PreSendHook(ctx context.Context, m ModemHook) error
}

// Get возвращает профиль по имени. Если имя неизвестно — ошибка.
func Get(name string) (Profile, error) {
	switch name {
	case "generic", "":
		return Generic{}, nil
	case "sim800":
		return SIM800{}, nil
	}
	return nil, fmt.Errorf("неизвестный профиль %q", name)
}

// Auto определить профиль по информации о модеме. Возвращает
// первый подходящий или Generic, если ни один не подошёл.
func Auto(info Info) Profile {
	all := []Profile{
		SIM800{},
	}
	for _, p := range all {
		if p.Match(info) {
			return p
		}
	}
	return Generic{}
}
