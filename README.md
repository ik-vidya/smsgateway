# smsgateway

SMS-шлюз для AT-совместимых GSM-модемов. Работает как долгоживущий
сервис: принимает SMS через модем, сохраняет в SQLite, отдаёт через
HTTP/JSON REST API. Более полная альтернатива `gammu-smsd`, написанная
на идиоматичном Go.

## Текущее состояние

Ранняя версия. Реализованы: конфигурация, логирование, SQLite-хранилище,
HTTP API, сервисный слой, AT-транспорт с URC-каналом, работа с SMS
(отправка/приём/удаление), профили модемов (generic, sim800).

Ещё **не** реализованы: web-UI, Gammu file backend, Telegram-алерты,
multipart-отправка/склейка, автоматический разбор PDU delivery report,
эндпоинты `/diagnostics` и `/test-sms`. Подробности —
[docs/STATUS.md](docs/STATUS.md).

## Возможности (реализовано)

- Отправка SMS через AT+CMGS в PDU-режиме (GSM-7, UCS-2).
- Приём SMS через URC `+CMTI` и AT+CMGL, декодирование PDU.
- Сохранение сообщений в SQLite (`modernc.org/sqlite`, без CGO).
- Delivery reports: таблица `delivery_reports`, обновление статуса
  сообщения на `delivered`/`failed`.
- Защита от коллизий 8-битного TP-MR через `pending_references`.
- Retention: автоматическая очистка старых записей.
- HTTP/JSON REST API с API-ключом.
- Мульти-профильная абстракция модемов: `generic`, `sim800` (со всеми
  обходами багов прошивки SIM800L).
- `log/slog` с уровнями `debug|info|warn|error` и форматами `text|json`.
- Режим "без модема": если `SERIAL_PORT` пуст или `none`, сервис
  работает без железа (API принимает SMS в очередь).

## Требования

- Go 1.26+ для сборки.
- AT-совместимый GSM-модем (для реальной работы).
- Для разработки удобно держать рядом
  [sim800ctl](https://github.com/ik-vidya/sim800ctl) — см. раздел
  «Связанные проекты».

## Сборка

```bash
git clone https://github.com/ik-vidya/smsgateway.git
cd smsgateway
make build
```

Кросс-сборка:

```bash
make dist   # linux/amd64, linux/arm64, linux/armv7
```

## Быстрый старт

```bash
# 1. Конфигурация
cp .env.example .env
$EDITOR .env
# Обязательные поля: API_KEY, DB_PATH.
# Для реального модема: SERIAL_PORT=/dev/ttyUSB0, MODEM_PROFILE=sim800.
# Для теста без модема: SERIAL_PORT=none

# 2. Миграции БД
./bin/smsgateway migrate --config .env

# 3. Запуск
./bin/smsgateway --config .env
```

Сервис слушает `HTTP_ADDR` (по умолчанию `127.0.0.1:8080`).

## CLI

```
smsgateway --config /path/to/.env           запустить сервис
smsgateway -v                               показать версию
smsgateway version                          показать версию
smsgateway migrate --config /path/to/.env   применить миграции БД
smsgateway list-ports                       список последовательных портов
smsgateway gammu-outbox --config .env       разовый прогон outbox
smsgateway gammu-outbox --config .env --dry-run  план без отправки
smsgateway convert-gammu-config --from /etc/gammu-smsdrc
smsgateway convert-gammu-config --from /etc/gammu-smsdrc --to my.env
```

## HTTP API

Базовый путь: `/api/v1`. Авторизация — заголовок `X-API-Key` или
`Authorization: Bearer <key>`. `/healthz` — без авторизации.

### `GET /healthz`

```json
{"status": "ok"}
```

### `GET /api/v1/status`

Состояние сервиса, статистика БД, информация о модеме.

```json
{
  "status": "ok",
  "uptime": "5m12s",
  "messages": {
    "queued": 0,
    "sent": 3,
    "delivered": 12,
    "failed": 1
  },
  "database": { "path": "./data/smsgateway.db", "healthy": true },
  "modem": {
    "configured": true,
    "port": "/dev/ttyUSB0",
    "profile": "sim800",
    "manufacturer": "SIMCOM_Ltd",
    "model": "SIMCOM_SIM800L",
    "revision": "Revision:1418B04SIM800L24",
    "imei": "863584038586060",
    "signal": 17,
    "network": 5,
    "network_text": "зарегистрирован (роуминг)"
  }
}
```

### `POST /api/v1/messages`

Отправить SMS.

```bash
curl -X POST \
  -H "X-API-Key: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"to":"+37121234567","text":"Привет"}' \
  http://127.0.0.1:8080/api/v1/messages
```

Ответ `202 Accepted`:

```json
{"id": 42, "status": "queued"}
```

Сообщение попадает в очередь, сервисный слой отправляет его через
модем. Статус обновляется по мере продвижения:
`queued → sending → sent → delivered` (или `failed`/`sent_no_dr`).

### `GET /api/v1/messages`

Список с фильтрами. Query-параметры:

- `direction` — `in` или `out`;
- `status` — `queued|sending|sent|delivered|failed|sent_no_dr|received`;
- `peer` — подстрока номера;
- `since`, `until` — RFC3339;
- `limit` (по умолчанию 50, макс 500), `offset`.

```json
{
  "items": [
    {
      "id": 42,
      "direction": "out",
      "status": "delivered",
      "peer": "+37121234567",
      "text": "Привет",
      "reference": 179,
      "sent_at": "2026-10-15T12:00:02Z",
      "delivered_at": "2026-10-15T12:00:34Z",
      "created_at": "2026-10-15T12:00:00Z",
      "updated_at": "2026-10-15T12:00:34Z"
    }
  ],
  "total": 1,
  "limit": 10,
  "offset": 0
}
```

### `GET /api/v1/messages/{id}`

Одно сообщение.

### `DELETE /api/v1/messages/{id}`

Удалить сообщение. Ответ `204 No Content`.

### Формат ошибок

```json
{"error": {"code": "bad_request", "message": "..."}}
```

## Конфигурация

Файл `.env`. Полный пример — [.env.example](.env.example).

| Переменная | Описание |
|---|---|
| `HTTP_ADDR` | Адрес HTTP API (по умолчанию `127.0.0.1:8080`) |
| `API_KEY` / `API_KEYS` | API-ключ(и). Хотя бы один обязателен |
| `SERIAL_PORT` | Порт модема (`/dev/ttyUSB0`, `COM3` или `none`) |
| `SERIAL_BAUD` | Скорость порта (по умолчанию 115200) |
| `MODEM_CMD_TIMEOUT` | Таймаут AT-команды (по умолчанию 5s) |
| `MODEM_PROFILE` | `generic` (по умолчанию), `sim800` |
| `DB_PATH` | Путь к SQLite-файлу |
| `SMS_RETENTION` | Срок хранения: `7d`, `720h`, `1d12h`. Пусто — вечно |
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `text` или `json` |
| `GAMMU_ROOT` | Корень Gammu file backend. Пусто — выключен |
| `GAMMU_INBOX` / `_OUTBOX` / `_SENT` / `_ERROR` | Подкаталоги (по умолчанию inbox/outbox/sent/error) |
| `GAMMU_INBOX_FORMAT` | `utf8` (default) / `unicode` / `standard` / `detail` |
| `GAMMU_WATCH_INTERVAL` | Период сканирования outbox (по умолчанию 5s) |
| `GAMMU_OUTBOX_MAX_ATTEMPTS` | Всего попыток отправки (по умолчанию 5) |

Также в `.env` есть переменные для будущих функций (web-UI,
Telegram) — см. `.env.example`.

## Архитектура

```
cmd/smsgateway/       — точка входа, CLI
internal/config/      — .env, валидация
internal/logging/     — log/slog
internal/storage/     — SQLite, миграции, репозитории, retention
internal/modem/       — AT-транспорт, URC-канал, Send/Read/Delete
internal/profiles/    — профили модемов: generic, sim800
internal/service/     — URC-цикл, отправка, pending references, DR
internal/api/         — HTTP-сервер, middleware, handlers
```

Ключевая особенность AT-транспорта: **два независимых потока** —
синхронные ответы на команды (через `Exec`) и асинхронные URC
(через `URCs()`). URC могут прийти в середине ответа на AT-команду
(особенно на SIM800L), и без разделения каналов корректная работа
невозможна. Подробнее — [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Миграция с gammu-smsd

Если у вас уже работает `gammu-smsd`, его файловый бэкенд
совместим с smsgateway. Чтобы перейти:

```bash
smsgateway convert-gammu-config --from /etc/gammu-smsdrc --to smsgateway.env
```

Конвертер прочитает `[gammu]` и `[smsd]`, выпишет совместимые
опции и отчитается о том, что проигнорировано (флаг `--verbose`
для полного списка). `MaxRetries` преобразуется в
`GAMMU_OUTBOX_MAX_ATTEMPTS` по формуле N+1: в gammu-smsd это
число повторов после первой попытки, у нас — общее число попыток.

Поддерживается только `Connection = serial` или `at`. `irda` и
`bluetooth` не поддерживаются.

## Связанные проекты

- [sim800ctl](https://github.com/ik-vidya/sim800ctl) — CLI-утилита
  для работы с SIM800L. Пакеты `pkg/pdu` (PDU encode/decode) и
  `pkg/number` (нормализация номеров) переиспользуются в
  `smsgateway`.

Для локальной разработки оба проекта держатся рядом и связываются
через `go.work`:

```
go-smsd/
├── go.work              ← go work init ./sim800ctl ./smsgateway
├── sim800ctl/
└── smsgateway/
```

## Документация

- [docs/STATUS.md](docs/STATUS.md) — что реализовано, что нет.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — архитектура.
- [docs/API.md](docs/API.md) — детали HTTP API.
- [docs/MODEMS.md](docs/MODEMS.md) — профили модемов.
- [docs/WEBUI.md](docs/WEBUI.md) — план веб-UI.
- [docs/GAMMU-FORMAT.md](docs/GAMMU-FORMAT.md) — формат Gammu.

## Разработка

```bash
make test        # go test ./...
make test-race   # go test -race ./...
make vet         # go vet ./...
make lint        # golangci-lint (если установлен)
```

Проект создан с использованием AI-ассистента DeepSeek в режиме
диалога. Коммиты содержат trailer `Assisted-by: DeepSeek chat`.

## Лицензия

MIT — см. [LICENSE](LICENSE).