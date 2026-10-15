# Статус проекта

Краткий список: что работает, что в процессе, что запланировано.
Обновляется по мере развития.

**Дата последнего обновления:** 2026-10-15.

---

## Реализовано

### Конфигурация (`internal/config`)
Загрузка `.env`, валидация, `ParseDuration` (`h/m/s/ms/d`, `1d12h`).
Тесты: 1.

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
`Modem.Init/Info/Close`. `SendSMS` (PDU, UCS-2/GSM-7, AT+CMGS),
`ReadSMS` (PDU-декодер через `sim800ctl/pkg/pdu`), `DeleteSMS`,
`DeleteAllSMS`. `OpenSerial`, `ListSerialPorts`. Тесты: 30.

### Профили (`internal/profiles`)
`Profile` интерфейс с `Workarounds/PreInitHook/PreSendHook`.
`Generic`, `SIM800` (ForceUCS2, UseCSMPForSRR, AlignReadStorage,
PinAttemptsAT). `Get(name)`, `Auto(info)`.

### Сервис (`internal/service`)
Интерфейс `Modem` для подмены в тестах. URC-цикл: `+CMTI` →
чтение → сохранение в БД → удаление из модема (дедупликация
по peer+text). Цикл отправки: `queued → sending → sent/failed`.
Цикл pending references: `ExpirePendingReferences → sent_no_dr`.
`HandleDeliveryReport`. Тесты: 8.

### HTTP API (`internal/api`)
`net/http` + `ServeMux` (method+path), graceful shutdown.
Middleware: `withAuth` (constant-time), `withLogging`, `withRecover`.
Эндпоинты: `/healthz`, `/api/v1/status`, `/api/v1/messages`
(CRUD). Формат ошибок `{"error":{"code","message"}}`. Тесты: 19.

### CLI (`cmd/smsgateway`)
Команды: `version`, `migrate`, `list-ports`, запуск сервиса.
Режим без модема при `SERIAL_PORT=none` (nopModem).

---

## Критичные задачи (обязательны для production)

Программа должна уметь всё то же, что CLI-утилита `sim800ctl`,
плюс дополнительные требования. Всё нижеследующее — блокеры.

1. **Автоматический разбор `+CDS`.** URC `+CDS` приходит двумя
   строками (header + PDU). Сейчас распознаётся только header,
   PDU не связывается с URC, `HandleDeliveryReport` не вызывается
   автоматически. Без этого статус `delivered` не устанавливается
   сам — вся логика DR не работает end-to-end.

2. **AlignReadStorage (SIM800L).** Перед чтением SMS на реальном
   SIM800L нужно выставлять `AT+CPMS="SM","SM","SM"`. У нас
   в обходах профиля заявлен, но не применяется. Без этого
   входящие SMS могут «теряться» (модем хранит в SM, читаем
   из ME).

3. **Retry при транзиентных ошибках отправки.** `+CMS ERROR:
   Temporary failure`, `No network service` и подобные. 3 попытки
   с backoff 1s/5s/15s. Поле `retry_count` в схеме уже есть.

4. **Multipart-отправка.** SMS > 160 символов GSM-7 / 70 UCS-2.
   Автоматическое разбиение с UDH reference. Multipart-приём
   уже работает (`pdu.Decode` + `storage.SavePendingPart`),
   отправка — нет.

---

## Другие запланированные задачи

6. `POST /api/v1/test-sms` — проверочная SMS на `SELF_NUMBER`.
7. `GET /api/v1/diagnostics` — самодиагностика модема
   (пакет `internal/diagnostics` пустой).
8. Gammu file backend — импорт/экспорт (пакет `internal/gammu`).
9. Telegram-алерты для критических ошибок (пакет `internal/notify`).
10. Web UI (htmx 4 + Bootstrap 5.2 + i18n ru/lv/en + OIDC).
11. Профили `quectel`, `telit`.
12. Команда `probe` для автогенерации профилей.

---

## Известные ограничения

- Один процесс = один модем.
- Нет multipart-отправки и retry.
- URC `+CDS` разбирается не полностью.
- Не тестировано на живом SIM800L в составе `smsgateway`
  (логика основана на рабочей утилите `sim800ctl`).