# Профили модемов

## Статус

Реализован интерфейс `internal/profiles.Profile` и два профиля:
`generic` (по умолчанию) и `sim800`.

## Как это работает

Профиль задаётся в конфиге:

    MODEM_PROFILE=sim800

Возможные значения: `sim800`, `quectel`, `telit`, `generic`.

## Обходы (Workarounds)

Профиль возвращает набор флагов, которые меняют поведение
общего кода:

| Флаг | Что делает | Кому нужно |
|------|-----------|-----------|
| `ForceUCS2ForAllSMS` | Отправлять всё как UCS-2 | SIM800L (баг чтения GSM-7) |
| `UseCSMPForSRR` | `AT+CSMP=49,167,0,0` перед отправкой | SIM800L (CSMP перебивает SRR) |
| `AlignReadStorage` | `AT+CPMS` перед чтением | SIM800L (read≠receive) |
| `PinAttemptsAT` | AT-команда для попыток PIN | SIM800L: `AT+SPIC` |

## Готовый профиль SIM800L

См. `internal/profiles/sim800.go`. Все обходы снабжены ссылками на
`docs/SIM800L-NOTES.md` в репозитории
[sim800ctl](https://github.com/ik-vidya/sim800ctl).

## Добавление нового профиля

1. Создать `internal/profiles/<name>.go`.
2. Реализовать интерфейс `Profile`.
3. Зарегистрировать в `Get()` и `Auto()` в `internal/profiles/profile.go`.
4. Задокументировать обходы с ссылками на источники.
5. Протестировать на реальном железе.

## Команда `probe` (план)

Позже добавим `smsgateway probe --profile-out profiles/` — она
опросит модем (`ATI`, `AT+CGMI`, `AT+CGMM`, `AT+CGMR`, проверка
поддержки `AT+SPIC`, `AT+CBC`, `AT+CSMP`, `AT+CNMI`, `AT+CSCS`,
`AT+CPMS`) и сгенерирует скелет профиля с рекомендациями.
