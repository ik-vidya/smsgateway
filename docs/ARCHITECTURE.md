# Архитектура

## Статус

**Черновик.** Описывает целевую структуру. Реализация — по мере
разработки.

## Пакеты (план)

- `cmd/smsgateway` — точка входа, CLI-флаги.
- `internal/config` — загрузка `.env`, валидация, парсинг длительностей.
- `internal/logging` — `log/slog`.
- `internal/modem` — AT-транспорт (reader/writer/URC), exec, инициализация.
- `internal/profiles` — профили модемов: команды, обходы багов, URC.
- `internal/storage` — SQLite, миграции, репозитории, retention.
- `internal/gammu` — Gammu file backend (импорт/экспорт).
- `internal/api` — HTTP-сервер, middleware, handlers.
- `internal/notify` — Telegram-нотификатор.
- `internal/service` — оркестрация: URC-цикл, отправка, приём, multipart.
- `internal/diagnostics` — сборщик отчёта диагностики.
- `internal/webui` — административный веб-UI (htmx 4 + Bootstrap 5.2).

## AT-транспорт: URC-канал

Главное отличие от CLI-утилиты `sim800ctl`: в шлюзе нужен
асинхронный AT-транспорт с URC-каналом.

- **Writer-горутина** — сериализует запись команд в порт.
- **Reader-горутина** — читает строки и классифицирует их:
  - URC (по паттернам `+CMTI:`, `+CDS:`, `+CREG:`, `+CGREG:`, `+CUSD:`,
    `RING`, `NO CARRIER`) — в канал `urcCh`;
  - эхо команды (при `ATE1`) — отбрасывается;
  - всё остальное — в канал `respCh` текущей команды.
- **`Exec(ctx, cmd)`** — синхронный вызов: пишет команду, читает
  `respCh` до `OK`/`ERROR`/`+CME ERROR:`/`+CMS ERROR:` или таймаута.
  URC в это время идут мимо и не мешают.
- **Service-горутина** — читает `urcCh` и реагирует: на `+CMTI` читает
  SMS и сохраняет в БД, на `+CDS` обновляет статус доставки,
  на `+CREG`/`+CGREG` обновляет состояние сети.

Такая архитектура обязательна: на SIM800L (и большинстве модемов)
URC могут приходить в середине ответа на другую AT-команду.

## Пакеты, переиспользуемые из sim800ctl

- `github.com/ik-vidya/sim800ctl/pkg/pdu` — PDU encode/decode
  (GSM-7, UCS-2, UDH, multipart).
- `github.com/ik-vidya/sim800ctl/pkg/number` — нормализация номеров.

Пока не импортируются — подключим, когда дойдём до модема и сервиса.
