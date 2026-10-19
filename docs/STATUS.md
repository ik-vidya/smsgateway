# Статус проекта

**Дата последнего обновления:** 2026-10-19.

`smsgateway` — готовый к продакшену SMS-шлюз. Все заявленные
компоненты реализованы, покрыты тестами и проверены на живом
оборудовании (SIM800L × 2, CH340/CP2102). Открытым остаётся
только OIDC-авторизация для Web UI (B4) — осознанное решение
для v1.0.0.

## Реализовано

### Конфигурация (`internal/config`)
Загрузка `.env`, валидация, `ParseDuration`. In-place правка
`.env` (`envwrite.go`). `godotenv.Overload` при SIGHUP —
изменения применяются без потери env.

### Логирование (`internal/logging`)
`log/slog` с уровнями и форматом `text|json`. Кольцевой буфер
на 1000 записей + pub/sub для SSE. Безопасен под `-race`.

### Хранилище (`internal/storage`)
SQLite через `modernc.org/sqlite` (pure Go). Миграции, CRUD,
DR, pending-ссылки, Gammu outbox, retention.

### Модем (`internal/modem`)
AT-транспорт: reader-горутина с распознаванием URC. `SendSMS`
(PDU), `ReadSMS`, PIN-unlock, `discover`. `OpenSerialWithRetry`
(60 попыток, 1с) — ждёт появления `/dev/ttyUSBx` после переноса
в другой порт. Фиксы: `drainRespCh` (closed channel busy loop),
`readerLoop` (`(0, nil)` busy loop), CREG/CGREG/CBC — ответы,
не URC.

### Профили (`internal/profiles`)
`generic`, `sim800`, `quectel`, `telit`, `huawei`.

### Диагностика и Probe (`internal/diagnostics`, `internal/probe`)
`Collect` — опрос 14 AT-команд с таймаутами. `probe` проверен
на двух SIM800L. `--output` генерирует скелет профиля.

### Сервис (`internal/service`)
URC-цикл, отправка с retry, pending references, DR,
`networkLoop`. `urcLoop` при отключении модема сохраняет
фатальную ошибку и отменяет контекст. `SendTestSMSWithReceive`
— проверка сети в обе стороны (исходящая + входящая).

### HTTP API (`internal/api`)
REST API с `withAuth`, `/healthz`, `/readyz`, `/api/v1/status`,
`/api/v1/messages`, `/test-sms`, `/diagnostics`.

### Notify (`internal/notify`)
Telegram-нотификатор (async, dedup, rate limit).

### Gammu file backend (`internal/gammu`)
Совместим с `gammu-smsd` Files. Watcher, retry, конвертер
`gammu-smsdrc → .env`.

### Web UI (`internal/webui`)
Dashboard, inbox, outbox, compose, logs (SSE), setup.
На `/setup`: SSE-поток портов (с `EvalSymlinks` для кастомных
симлинков), «Диагностика», «Отправка + приём», heartbeat
(`/healthz`), бейдж «используется сервисом» на активном порту.
Hot-reload через SIGHUP + `syscall.Exec`.

### CLI и Systemd
Cobra-команды. `z.sh` с шагом 0 (чистота дерева). Правила udev
и unit `smsgateway@.service` для продакшена.

### CI
GitHub Actions: `gofmt`, `vet`, `test`, `test -race`, cross-compile.

---

## Известные ограничения версии 1.0.0

1. **Web UI без авторизации (`WEBUI_AUTH_MODE=none`).**
   Ответственность за доступ — firewall / reverse proxy / SSH-туннель.
   OIDC — в следующем релизе.

2. **`AT+CREG=1` вместо `=2`.** URC `+CREG` приходят часто,
   `networkLoop` опрашивает раз в 30s. Работает и так, менять
   не стали.

3. **`probe --output --name` на не-SIM800L не проверен на живом
   железе.** Код покрыт тестами, но Quectel/Telit/Huawei не было
   под рукой.

4. **Flash SMS** (флаг `f` в расширении OUT-файла) не
   поддерживается на уровне `pdu.Build` в `sim800ctl` —
   отправляется как обычное SMS.

---

## Грабли, которые не повторять

- Busy loop в `drainRespCh` при закрытом канале.
- `godotenv.Load` не перезаписывает env — для hot-reload нужен `Overload`.
- Строковое сравнение портов ломается на симлинках — нужен `EvalSymlinks`.
- `readerLoop` крутится при `Read == (0, nil)` — нужен `time.After`.
- При перестановке USB-порта ядру нужно время — нужен `OpenSerialWithRetry`.
