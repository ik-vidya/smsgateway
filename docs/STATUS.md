# Статус проекта

Краткий список: что работает, что в процессе, что запланировано.
Обновляется по мере развития.

**Дата последнего обновления:** 2026-10-15.

---

## Реализовано

### Конфигурация (`internal/config`)

- Загрузка `.env` через `godotenv`.
- Валидация обязательных полей и допустимых значений.
- `ParseDuration` с поддержкой `h`, `m`, `s`, `ms`, `d`
  и комбинированных значений (`1d12h`).
- Тесты: `TestParseDuration`.

### Логирование (`internal/logging`)

- `log/slog`, форматы `text` и `json`.
- Уровни `debug`, `info`, `warn`, `error`.

### Хранилище (`internal/storage`)

- SQLite через `modernc.org/sqlite` (чистый Go, без CGO).
- Миграции через `//go:embed`, идемпотентны.
- Схема: `messages`, `delivery_reports`, `pending_references`,
  `pending_parts`, `pending_part_bodies`, `imported_files`,
  `schema_migrations`.
- Репозитории: `CreateMessage`, `GetMessage`, `ListMessages`,
  `DeleteMessage`, `UpdateStatus`, `MarkSending/Sent/Delivered/Failed/SentNoDR`,
  `CountByStatus`.
- Delivery reports: `CreateDeliveryReport`, `FindMessageByReference`,
  `ListDeliveryReportsByMessage/Reference`.
- Pending references: `ReserveReference`, `ReleaseReference`,
  `FindPendingReference`, `ListFreeReferences`, `ExpirePendingReferences`.
- Multipart сборка: `SavePendingPart`, `ExpirePendingParts`.
- Retention: `RunRetention` (фоновая очистка).
- Тесты: 9 (миграции, CRUD, переходы статусов, фильтры, DR,
  брони reference, multipart, retention).

### Модем (`internal/modem`)

- Транспорт AT через `Transport`-интерфейс (реальный порт —
  `go.bug.st/serial`, mock — для тестов).
- Reader-горутина: побайтовое чтение, классификация строк
  (URC / эхо / ответ), распознавание prompt `> `.
- Writer-горутина: сериализация записи в порт.
- `Exec(ctx, cmd)` — синхронный вызов с таймаутом.
- `ExecPrompt(ctx, cmd, payload)` — для AT+CMGS.
- `URCs()` — канал асинхронных URC.
- Типы URC: `+CMTI`, `+CDS`, `+CREG`, `+CGREG`, `+CUSD`, `+CBC`,
  `RING`, `NO CARRIER`, `RDY`.
- Интерпретация ошибок: `+CME ERROR`, `+CMS ERROR`, проприетарные
  текстовые SIM800.
- `Modem.Init/Info/Close/Exec/URCs`.
- `SendSMS` (PDU, UCS-2/GSM-7, AT+CMGS), `ReadSMS` (PDU-декодер
  через `sim800ctl/pkg/pdu`), `DeleteSMS`, `DeleteAllSMS`.
- `OpenSerial`, `ListSerialPorts`.
- Тесты: 30 (включая prompt, URC, Send/Read/Delete).

### Профили (`internal/profiles`)

- Интерфейс `Profile`: `Name/Match/Workarounds/PreInitHook/PreSendHook`.
- `Generic` — по умолчанию, только общие AT-команды.
- `SIM800` — со всеми обходами: ForceUCS2, UseCSMPForSRR,
  AlignReadStorage, PinAttemptsAT (AT+SPIC).
- `Get(name)` и `Auto(info)`.

### Сервис (`internal/service`)

- Интерфейс `Modem` для подмены в тестах.
- URC-цикл: `+CMTI` → чтение SMS → сохранение в БД → удаление из
  памяти модема (дедупликация по peer+text).
- Цикл отправки: выборка `queued` → `MarkSending` → `SendSMS` →
  `ReserveReference` → `MarkSent` / `MarkFailed`.
- Цикл pending references: `ExpirePendingReferences` → `MarkSentNoDR`.
- `HandleDeliveryReport(ref, tpst, rawPDU)`: находит сообщение,
  сохраняет DR, обновляет статус, освобождает reference.
- `ModemInfo()`, `NetworkStatus()`, `LastCSQ()`, `Uptime()`.
- Тесты: 8.

### HTTP API (`internal/api`)

- Сервер на `net/http` (`ServeMux` с method+path паттернами).
- Graceful shutdown.
- Middleware: `withAuth` (X-API-Key / Bearer, constant-time),
  `withLogging`, `withRecover`.
- Эндпоинты:
  - `GET /healthz` — без авторизации;
  - `GET /api/v1/status`;
  - `POST /api/v1/messages`;
  - `GET /api/v1/messages` с фильтрами;
  - `GET /api/v1/messages/{id}`;
  - `DELETE /api/v1/messages/{id}`.
- Формат ошибок `{"error":{"code":"...","message":"..."}}`.
- Тесты: 19.

### CLI (`cmd/smsgateway`)

- Команды: `version`, `migrate`, `list-ports`, запуск сервиса
  по умолчанию.
- Флаги: `--config`, `-v/--version`.
- Режим "без модема" при `SERIAL_PORT=none` (использует `nopModem`).

---

## В процессе / частично

### Delivery reports

- URC `+CDS` распознаётся, но полный PDU отчёта (следующая строка
  после заголовка) не связывается с URC автоматически.
- `HandleDeliveryReport` есть и работает, но вызывается вручную —
  нет автоматического парсинга PDU отчёта.

### Multipart

- Чтение: `pdu.Decode` возвращает `Multipart` для входящих,
  структура `MultipartInfo` есть в `SMSMessage`.
- Сборка: `storage.SavePendingPart` умеет собирать части.
- Отправка: `SendSMS` возвращает ошибку при >1 части — multipart
  отправка не реализована.

### Retry при ошибках отправки

- В `modem.SendSMS` есть базовая отправка; retry при транзиентных
  ошибках (`Temporary failure`) — не реализован в сервисном слое.
  `RetryCount` в схеме есть, не используется.

---

## Не реализовано

### Web UI

- `WEBUI_*` в конфиге есть, код отсутствует.
- План: htmx 4 + Bootstrap 5.2, локализация ru/lv/en, OIDC.

### Gammu file backend

- `FILES_BACKEND_DIR`, `GAMMU_IMPORT_DIR`, `GAMMU_IMPORT_INTERVAL`
  в конфиге есть, код отсутствует.
- Пакет `internal/gammu` — пустой (`.gitkeep`).

### Telegram-алерты

- `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` в конфиге есть, код
  отсутствует.
- Пакет `internal/notify` — пустой.

### Диагностика

- Пакет `internal/diagnostics` — пустой.
- Эндпоинт `GET /api/v1/diagnostics` — отсутствует.

### Проверочная SMS

- Эндпоинт `POST /api/v1/test-sms` — отсутствует.
- `SELF_NUMBER`, `TEST_SMS_TIMEOUT` в конфиге есть.

### PIN SIM-карты

- `MODEM_PIN` в конфиге отсутствует, разблокировка не реализована.
- Нет обработки `+CPIN: SIM PIN`.

### Хранение SMS в памяти модема (storage alignment)

- `AlignReadStorage` в обходах профиля SIM800 заявлен, но в
  `Modem.ReadSMS` не применяется (`AT+CPMS` перед чтением не
  выставляется). На реальном SIM800L может понадобиться.

### Мультимодемность

- Профиль захардкожен в `profiles.Get(name)` для `sim800`,
  `generic`. `quectel`, `telit` — в списке допустимых значений
  конфига, но кода нет.

### Автогенерация профиля (probe)

- Команда `smsgateway probe --profile-out profiles/` — в плане.

---

## Ближайшие задачи (по приоритету)

1. **Автоматический разбор `+CDS`** — связать header и PDU
   в readerLoop, вызывать `service.HandleDeliveryReport`.
2. **Проверка на реальном SIM800L** — `SendSMS`/`ReadSMS`/DR
   на живом железе.
3. **Retry при транзиентных ошибках** отправки (3 попытки).
4. **Multipart отправка** — по частям с UDH reference.
5. **`AT+CPMS` перед чтением** для SIM800 (AlignReadStorage).
6. **`POST /api/v1/test-sms`** — проверочная SMS на `SELF_NUMBER`.
7. **`GET /api/v1/diagnostics`** — самодиагностика модема.
8. **PIN SIM**: `MODEM_PIN`, разблокировка при старте.
9. **Gammu file backend** — импорт/экспорт.
10. **Web UI** — htmx + Bootstrap, локализация.

---

## Известные ограничения

- Один процесс = один модем.
- API не поддерживает multipart-отправку (SMS > 160 символов
  GSM-7 / 70 символов UCS-2 вернёт ошибку).
- URC `+CDS` разбирается не полностью (только заголовок).
- Не тестировано на реальном модеме в составе `smsgateway`.
  Отдельные части (SendSMS, ReadSMS, AT-транспорт) проверены
  концептуально — код основан на рабочей утилите `sim800ctl`.