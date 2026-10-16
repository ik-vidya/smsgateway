# Статус проекта

**Дата последнего обновления:** 2026-10-15.

---

## Реализовано

### Конфигурация (`internal/config`)

Загрузка `.env`, валидация, `ParseDuration` (`h/m/s/ms/d`, `1d12h`),
MODEM_PIN (4-8 цифр), парная валидация TELEGRAM_BOT_TOKEN +
TELEGRAM_CHAT_ID. Тесты: 1.

### Логирование (`internal/logging`)

`log/slog`, `text` и `json`, уровни `debug|info|warn|error`.

### Хранилище (`internal/storage`)

SQLite через `modernc.org/sqlite` (без CGO), миграции через
`//go:embed`. Схема: `messages`, `delivery_reports`,
`pending_references`, `pending_parts`, `pending_part_bodies`,
`imported_files`, `schema_migrations`. Тесты: 9.

### Модем (`internal/modem`)

AT-транспорт (reader-горутина, prompt `> `, URC), `Exec`/`ExecPrompt`,
`URCs()`. URC: `+CMTI`, `+CDS`, `+CREG`, `+CGREG`, `+CUSD`, `+CBC`,
`RING`, `NO CARRIER`, `RDY`. `SendSMS` (PDU, UCS-2/GSM-7, multipart),
`ReadSMS`, `DeleteSMS`, `DeleteAllSMS`. `OpenSerial`, `ListSerialPorts`.
AlignReadStorage, retry при транзиентных ошибках, разблокировка
SIM по PIN. Тесты: 38.

### Профили (`internal/profiles`)

`Profile` с Workarounds/PreInitHook/PreSendHook. `Generic`, `SIM800`.

### Сервис (`internal/service`)

URC-цикл, отправка с retry, pending references, `HandleDeliveryReport`,
`SendTestSMS`, `RunDiagnostics`. Принимает `Notifier` — критические
ошибки уходят в Telegram. Тесты: 12.

### Диагностика (`internal/diagnostics`)

`Collect(ctx, modem)` — опрос AT-команд, парсинг в структурированный
`Result`. Тесты: 7.

### Notify (`internal/notify`)

- `Notifier` интерфейс, `Noop`.
- `TelegramNotifier`: асинхронная очередь, дедупликация (окно
  5 минут), rate limit (1 сообщение в секунду), форматирование
  с эмодзи по уровню.
- Тесты: 9 (mock HTTP-сервер).

### HTTP API (`internal/api`)

`net/http` + `ServeMux`, graceful shutdown. Middleware: `withAuth`,
`withLogging`, `withRecover` (паника → Telegram-алерт). Эндпоинты:
`/healthz`, `/api/v1/status`, `/api/v1/messages` (CRUD),
`/api/v1/test-sms`, `/api/v1/diagnostics`. Тесты: 21.

### Gammu file backend (`internal/gammu`)

Совместим с gammu-smsd Files backend. Каталоги inbox/outbox/sent/
error под `GAMMU_ROOT`. Форматы имён IN и все три формы OUT с
приоритетами A-Z. Автоопределение кодировки файла (UTF-8 default
и fallback, UTF-16LE/BE с BOM). Watcher outbox с retry и
понижением приоритета (`retries >= GAMMU_OUTBOX_MAX_ATTEMPTS`
или приоритет Z → error). CLI: `gammu-outbox [--dry-run]`.
Подробности — [GAMMU-FORMAT.md](GAMMU-FORMAT.md).

### CLI (`cmd/smsgateway`)

Команды: `version`, `migrate`, `list-ports`, `gammu-outbox`,
`convert-gammu-config`, сервис. Режим без модема
(`SERIAL_PORT=none`). Telegram-нотификатор создаётся в `main`
и передаётся в `service` и `api`.

---

## В процессе / частично

### (удалено — Gammu реализован, см. «Реализовано»)

### Web UI

`WEBUI_*` в конфиге есть, код отсутствует.

### Мультимодемность

Профили `sim800`, `generic`. `quectel`, `telit` — в конфиге, но
кода нет.

---

## Другие запланированные задачи

1. Web UI (htmx 4 + Bootstrap 5.2 + i18n ru/lv/en + OIDC).
2. Профили `quectel`, `telit`.
3. Команда `probe` для автогенерации профилей.
4. Команда `detect-modem` (udev-обнаружение порта).

---

## Известные ограничения

- Один процесс = один модем.
- Не тестировано на живом SIM800L в составе `smsgateway`.
