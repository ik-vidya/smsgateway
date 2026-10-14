package profiles

import (
	"context"
	"strings"
)

// SIM800 — профиль для SIMCOM SIM800L и совместимых (SIM800, SIM900).
//
// Обходит известные баги прошивок, обнаруженные при работе с
// Revision:1418B04SIM800L24. Полное описание — в репозитории sim800ctl:
// https://github.com/ik-vidya/sim800ctl/blob/main/docs/SIM800L-NOTES.md
type SIM800 struct{}

func (SIM800) Name() string { return "sim800" }

func (SIM800) Match(info Info) bool {
	m := strings.ToUpper(info.Manufacturer)
	mod := strings.ToUpper(info.Model)
	return strings.Contains(m, "SIMCOM") ||
		strings.Contains(mod, "SIM800") ||
		strings.Contains(mod, "SIM900")
}

func (SIM800) Workarounds() Workarounds {
	return Workarounds{
		// SIM800L искажает GSM-7 при чтении в text mode с CSCS=UCS2.
		// Отправляем всё как UCS-2, чтобы эхо читалось корректно.
		ForceUCS2ForAllSMS: true,

		// CSMP перебивает SRR из PDU: без AT+CSMP=49,167,0,0
		// delivery reports не приходят.
		UseCSMPForSRR: true,

		// Модем может сохранять входящие в SM, а читать из ME.
		// Перед чтением выравниваем AT+CPMS.
		AlignReadStorage: true,

		// Число оставшихся попыток PIN доступно через AT+SPIC.
		PinAttemptsAT: "AT+SPIC",
	}
}

func (SIM800) PreInitHook(ctx context.Context, m ModemHook) error {
	// Проверить CFUN: если уже 1, не трогать (частая отправка
	// AT+CFUN=1 приводит к залипанию RF-секции — см. SIM800L-NOTES п.7).
	if resp, err := m.Exec(ctx, "AT+CFUN?"); err == nil {
		if !strings.Contains(resp, "+CFUN: 1") {
			m.Log("CFUN не 1, отправляю AT+CFUN=1")
			_, _ = m.Exec(ctx, "AT+CFUN=1")
		}
	}

	// Storage во всех трёх слотах (read, write, receive).
	// Это обходит баг read≠receive.
	_, _ = m.Exec(ctx, `AT+CPMS="SM","SM","SM"`)

	// URC: +CMTI на входящие, +CDS на delivery reports.
	// Без CNMI модем не присылает +CDS.
	_, _ = m.Exec(ctx, "AT+CNMI=2,1,0,1,0")

	// CSMP: SRR=1, VPF=01, VP=24ч. Нужно до первой отправки.
	_, _ = m.Exec(ctx, "AT+CSMP=49,167,0,0")

	// Fast SMS sending (best-effort).
	_, _ = m.Exec(ctx, "AT+CMMS=2")

	// Кодировка для text-режима (если будем использовать).
	_, _ = m.Exec(ctx, `AT+CSCS="UCS2"`)

	return nil
}

func (SIM800) PreSendHook(ctx context.Context, m ModemHook) error {
	// Переустанавливаем CSMP перед каждой отправкой — некоторые
	// прошивки SIM800 сбрасывают его после AT+CFUN или AT+COPS.
	_, _ = m.Exec(ctx, "AT+CSMP=49,167,0,0")
	return nil
}
