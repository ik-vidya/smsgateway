# Профили модемов

## Статус

Реализованы профили: `generic` (по умолчанию), `sim800`,
`quectel`, `telit`, `huawei`.

## Как это работает

Профиль задаётся в конфиге:

    MODEM_PROFILE=sim800

Возможные значения: `sim800`, `quectel`, `telit`, `huawei`,
`generic`. Регистр не важен: `SIM800` и `sim800` эквивалентны.

Профили `quectel`, `telit`, `huawei` сейчас — минимальные обёртки
над `generic`: специфических обходов прошивок для них пока не
известно. Явное имя профиля даёт две вещи:

1. **Намерение** — пользователь явно говорит «у меня Quectel», а
   не «generic».
2. **Задел на будущее** — когда появятся тесты на реальном
   железе, обходы добавятся в тот же файл без ломающих изменений
   в конфиге.

## Обходы (Workarounds)

Профиль возвращает набор флагов, которые меняют поведение
общего кода:

| Флаг | Что делает | Кому нужно |
|------|-----------|-----------|
| `ForceUCS2ForAllSMS` | Отправлять всё как UCS-2 | SIM800L (баг чтения GSM-7) |
| `UseCSMPForSRR` | `AT+CSMP=49,167,0,0` перед отправкой | SIM800L (CSMP перебивает SRR) |
| `AlignReadStorage` | `AT+CPMS` перед чтением | SIM800L (read≠receive) |
| `PinAttemptsAT` | AT-команда для попыток PIN | SIM800L: `AT+SPIC` |

У `quectel`, `telit`, `huawei` все флаги выключены.

## Автодетект

`profiles.Auto(info)` выбирает профиль по `Manufacturer`/`Model`
(используется командой `probe`):

| Профиль | Match по Manufacturer | Match по Model (префиксы) |
|---|---|---|
| `sim800` | содержит `SIMCOM` | `SIM800`, `SIM900` |
| `quectel` | содержит `QUECTEL` | `EC`, `EG`, `M9`, `MC6`, `BG9` |
| `telit` | содержит `TELIT` | `GL8`, `HE9`, `LE9`, `ME9` |
| `huawei` | содержит `HUAWEI` | `MS2`, `MU6`, `E3`, `E8`, `E5` |

Если ни один не подошёл — `generic`.

## Готовый профиль SIM800L

См. `internal/profiles/sim800.go`. Все обходы снабжены ссылками на
`docs/SIM800L-NOTES.md` в репозитории
[sim800ctl](https://github.com/ik-vidya/sim800ctl).

## Добавление нового профиля

1. Создать `internal/profiles/<name>.go`.
2. Реализовать интерфейс `Profile`.
3. Зарегистрировать в `Get()` и `Auto()` в `internal/profiles/profile.go`.
4. Добавить имя в валидацию `internal/config/config.go`.
5. Задокументировать обходы с ссылками на источники.
6. Протестировать на реальном железе.

## Команда `probe` (план)

Позже добавим `smsgateway probe --profile-out profiles/` — она
опросит модем (`ATI`, `AT+CGMI`, `AT+CGMM`, `AT+CGMR`, проверка
поддержки `AT+SPIC`, `AT+CBC`, `AT+CSMP`, `AT+CNMI`, `AT+CSCS`,
`AT+CPMS`) и сгенерирует скелет профиля с рекомендациями.
