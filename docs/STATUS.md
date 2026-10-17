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

`Profile` с Workarounds/PreInitHook/PreSendHook. `Generic`,
`SIM800`, `Quectel`, `Telit`, `Huawei` (три последних —
минимальные обёртки над Generic; специфических обходов пока нет).
`Get()` case-insensitive, `Auto()` выбирает по Manufacturer/Model.
Тесты: 5.

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

Команды: `version`, `migrate`, `list-ports`, `detect-modem`
(`--write-config` правит `.env` in-place), `gammu-outbox`,
`convert-gammu-config`, сервис. Режим без модема
(`SERIAL_PORT=none`). Telegram-нотификатор создаётся
в `main` и передаётся в `service` и `api`.

---

## В процессе / частично

### (удалено — Gammu реализован, см. «Реализовано»)

### Web UI

Сервер на `net/http` + `html/template` (embed). Локализация
ru/lv/en (go-i18n/v2). Bootstrap 5.2 и htmx 4.0 хранятся локально
в `static/vendor` (скачиваются через `make fetch`), CSP —
`default-src 'self'` без CDN. CSRF: SameSite=Lax на cookie +
проверка Origin/Referer. Работает только при
`WEBUI_AUTH_MODE=none`; OIDC — отдельный этап.

Полное описание страниц, эндпоинтов, hot-reload через SIGHUP
и ограничений — [WEBUI.md](WEBUI.md).

Страницы:
- `GET /` — dashboard: состояние модема, БД, uptime, счётчики;
- `GET /inbox` — входящие с фильтрами (peer, status, since/until)
  и пагинацией; htmx-фрагмент `messages-table` через
  `HX-Request`;
- `GET /outbox` — исходящие, те же фильтры и пагинация;
- `GET /compose`, `POST /compose` — отправка;
- `GET /logs` — live-поток логов (SSE);
- `GET /logs/stream` — SSE-эндпоинт (JSON, keepalive 15s);
- `GET /lang/{code}` — переключение языка.

### Самопроверка (B5c)

`POST /setup/test` вызывает `Service.SendTestSMS` (синхронно,
ждёт терминального статуса или `TEST_SMS_TIMEOUT`).
`SendTestSMS` уведомляет `Notifier`:
  - delivered → Warning (`selftest OK`);
  - failed → Critical (`selftest FAILED`);
  - timeout → Warning (`timeout waiting for DR`);
  - прочие → Warning (`status=...`).
Страница `/setup` показывает кнопку «Отправить тестовую SMS»
при настроенном `SERIAL_PORT`; результат — в браузере и
(если Telegram включён) в боте.

### Логи через SSE

`internal/logging` содержит ring buffer на 1000 записей +
pub/sub. `NewWithBuffer` создаёт `slog.Handler`, который пишет
и в stdout, и в буфер. Страница `/logs` подключается к
`/logs/stream` через `EventSource`, рисует записи в DOM
(лимит 2000), фильтрует по уровню, умеет ставиться на паузу
и чиститься. Автопрокрутка вниз отключается, если пользователь
прокрутил вверх. При переполнении канала подписчика шлём
`event: warning` с числом пропущенных записей.

### Мультимодемность

Профили `sim800`, `generic`. `quectel`, `telit` — в конфиге, но
кода нет.

---

## Другие запланированные задачи

1. Web UI (htmx 4 + Bootstrap 5.2 + i18n ru/lv/en + OIDC).
   Включая Setup wizard (udev-подключение модема по кнопке).
2. Профили `quectel`, `telit`.
3. Команда `probe` для автогенерации профилей.

---

## Известные ограничения

- Один процесс = один модем.
- Не тестировано на живом SIM800L в составе `smsgateway`.
