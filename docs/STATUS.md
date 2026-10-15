# Статус проекта

Краткий список: что работает, что в процессе, что запланировано.

**Дата последнего обновления:** 2026-10-15.

---

## Реализовано

### Конфигурация (`internal/config`)

Загрузка `.env`, валидация, `ParseDuration` (`h/m/s/ms/d`, `1d12h`),
MODEM_PIN с валидацией 4-8 цифр. Тесты: 1.

### Логирование (`internal/logging`)

`log/slog`, `text` и `json`, уровни `debug|info|warn|error`.

### Хранилище (`internal/storage`)

SQLite через `modernc.org/sqlite` (без CGO), миграции через
`//go:embed`. Схема: `messages`, `delivery_reports`,
`pending_references`, `pending_parts`, `pending_part_bodies`,
`imported_files`, `schema_migrations`. Репозитории: CRUD,
`UpdateStatus` с проверкой переходов, DR, брони reference,
сборка multipart, retention. Тесты: 9.

### Модем (`internal/modem`)

Транспорт AT: reader-горутина (побайтово, prompt `> `, URC),
синхронные `Exec`/`ExecPrompt`, канал `URCs()`. Типы URC: `+CMTI`,
`+CDS`, `+CREG`, `+CGREG`, `+CUSD`, `+CBC`, `RING`, `NO CARRIER`,
`RDY`. Интерпретация CME/CMS-ошибок и текстовых SIM800.
`Modem.Init/Info/Close`. `SendSMS` (PDU, UCS-2/GSM-7, multipart),
`ReadSMS` (PDU-декодер через `sim800ctl/pkg/pdu`), `DeleteSMS`,
`DeleteAllSMS`. `OpenSerial`, `ListSerialPorts`. Выравнивание
storage (AlignReadStorage). Retry при транзиентных ошибках.
Разблокировка SIM по PIN (`MODEM_PIN`): обработка SIM PIN / SIM PUK /
NOT INSERTED, защита от последней попытки через `AT+SPIC`,
маскирование `AT+CPIN="****"` в логах. Тесты: 38.

### Профили (`internal/profiles`)

`Profile` интерфейс с `Workarounds/PreInitHook/PreSendHook`.
`Generic`, `SIM800` (ForceUCS2, UseCSMPForSRR, AlignReadStorage,
PinAttemptsAT). `Get(name)`, `Auto(info)`.

### Сервис (`internal/service`)

Интерфейс `Modem` для подмены в тестах. URC-цикл: `+CMTI` →
чтение → сохранение в БД → удаление из модема (дедупликация
по peer+text). Цикл отправки: `queued → sending → sent/failed`,
retry 3 попытки. Цикл pending references. `HandleDeliveryReport`.
`SendTestSMS` (синхронно, ждёт терминального статуса или таймаута).
`RunDiagnostics`. Тесты: 12.

### Диагностика (`internal/diagnostics`)

`Collect(ctx, modem)`: опрос набора AT-команд, парсинг в
структурированный `Result` (Manufacturer, Model, IMEI, Signal,
Registration, Operator, SMSC, Storage). Устойчивость: ошибка
отдельной команды не прерывает сбор. Тесты: 7.

### HTTP API (`internal/api`)

`net/http` + `ServeMux`, graceful shutdown. Middleware: `withAuth`
(constant-time), `withLogging`, `withRecover`. Эндпоинты:
`/healthz`, `/api/v1/status`, `/api/v1/messages` (CRUD),
`/api/v1/test-sms`, `/api/v1/diagnostics`. Формат ошибок
`{"error":{"code","message"}}`. Тесты: 21.

### CLI (`cmd/smsgateway`)

Команды: `version`, `migrate`, `list-ports`, запуск сервиса.
Режим без модема при `SERIAL_PORT=none` (nopModem).

---

## В процессе / частично

### Gammu file backend

`FILES_BACKEND_DIR`, `GAMMU_IMPORT_DIR`, `GAMMU_IMPORT_INTERVAL`
в конфиге есть, код отсутствует.

### Telegram-алерты

`TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` в конфиге есть, код
отсутствует.

### Web UI

`WEBUI_*` в конфиге есть, код отсутствует.

### Мультимодемность

Профиль `sim800`, `generic`. `quectel`, `telit` — в конфиге, но
кода нет.

---

## Другие запланированные задачи

1. Gammu file backend — импорт/экспорт.
2. Telegram-алерты для критических ошибок.
3. Web UI (htmx 4 + Bootstrap 5.2 + i18n ru/lv/en + OIDC).
4. Профили `quectel`, `telit`.
5. Команда `probe` для автогенерации профилей.

---

## Известные ограничения

- Один процесс = один модем.
- Не тестировано на живом SIM800L в составе `smsgateway`
  (логика основана на рабочей утилите `sim800ctl`).
