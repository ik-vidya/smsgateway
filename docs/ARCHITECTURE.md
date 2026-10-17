# Архитектура

## Назначение

`smsgateway` — долгоживущий сервис для приёма/отправки SMS через
AT-совместимый GSM-модем. Сохраняет сообщения в SQLite, отдаёт
их через HTTP/JSON REST API, предоставляет административный
Web UI, поддерживает Gammu file backend для совместимости с
`gammu-smsd`.

## Карта пакетов

```
cmd/smsgateway/             — точка входа, CLI (cobra): version,
                              migrate, list-ports, detect-modem,
                              probe, gammu-outbox,
                              convert-gammu-config, сервис
internal/config/            — загрузка .env, валидация, правка
                              .env in-place (envwrite)
internal/logging/           — log/slog + кольцевой буфер +
                              pub/sub для SSE
internal/storage/           — SQLite через modernc.org/sqlite,
                              миграции (embed), репозитории,
                              retention, модели
internal/modem/             — AT-транспорт: reader-горутина,
                              URC-канал, Exec/ExecPrompt,
                              SendSMS, ReadSMS, DeleteSMS,
                              OpenSerial, диагностика портов
                              (discover), PIN-разблокировка
internal/profiles/          — профили модемов: generic, sim800,
                              quectel, telit, huawei. Обходы
                              багов прошивок через Workarounds
internal/diagnostics/       — сбор AT-команд в структурированный
                              Result для /api/v1/diagnostics
internal/probe/             — опрос модема для отчёта и генерации
                              скелета профиля
internal/service/           — оркестрация: URC-цикл, отправка с
                              retry, pending references, DR,
                              networkLoop, recovery сирот,
                              адаптеры gammu.Sender/OutboxStore
internal/api/               — HTTP-сервер REST, middleware
                              (withAuth, withLogging, withRecover)
internal/notify/            — Telegram-нотификатор (async, dedup,
                              rate limit) + Noop
internal/gammu/             — Gammu file backend: inbox/outbox/
                              sent/error, watcher, retry с
                              понижением приоритета, decode
                              UTF-8/UTF-16, parse PDU,
                              конвертер gammu-smsdrc → .env
internal/webui/             — административный UI: dashboard,
                              inbox, outbox, compose, logs (SSE),
                              setup (watcher портов + probe +
                              save + hot-reload через SIGHUP)
```

## Поток данных

```
Modem ←serial→ AT-транспорт
                    │
                    ├─ URC → Service.handleURC
                    │            ├─ +CMTI → ReadSMS → storage
                    │            │           └─ Gammu inbox
                    │            ├─ +CDS  → HandleDeliveryReport
                    │            └─ +CREG → networkStat
                    │
                    └─ Exec / ExecPrompt ← Service
                                              │
                                              ├─ sendLoop
                                              │    └─ SendSMS
                                              │        ├─ PDU build
                                              │        └─ AT+CMGS
                                              │
                                              └─ networkLoop
                                                   └─ AT+CREG? / AT+CSQ
```

## AT-транспорт

Главная особенность — **асинхронный URC-канал**, без которого
корректная работа с SIM800L невозможна.

- **Reader-горутина** — побайтово читает порт, собирает строки,
  распознаёт prompt `> ` (без `\n`), классифицирует:
  - URC (`+CMTI`, `+CDS`, `+CREG`, `+CGREG`, `+CUSD`, `+CBC`,
    `RING`, `NO CARRIER`, `RDY`) → в `urcCh`;
  - всё остальное → в `respCh` текущей команды;
  - для `+CDS` — читает следующую строку как hex-PDU, декодирует
    и обогащает URC.
- **Exec(ctx, cmd)** — синхронный вызов: пишет команду, читает
  `respCh` до `OK`/`ERROR`/`+CME ERROR:`/`+CMS ERROR:` или
  таймаута. URC в это время идут мимо.
- **ExecPrompt(ctx, cmd, payload)** — для `AT+CMGS`: ждёт prompt
  `> `, пишет PDU + Ctrl-Z, ждёт `+CMGS: <ref>` и `OK`.
- **Запись синхронная**, `Exec` сериализуется через mutex. URC
  могут приходить посреди ответа на команду — это норма, reader
  их отфильтровывает.

## Сервисный слой

`Service` — координатор. Принимает `Modem` (интерфейс, чтобы
mock в тестах не требовал железа) и `*storage.Store`.

Горутины в `Run`:

- **urcLoop** — читает `URCs()`, реагирует на URC.
- **sendLoop** — раз в секунду выбирает `queued` из БД, шлёт
  через модем с retry (3 попытки, backoff 1s/5s, только для
  транзиентных ошибок).
- **pendingLoop** — раз в 30 секунд ищет `pending_references`
  с истёкшим TTL, помечает связанные сообщения `sent_no_dr`.
- **networkLoop** — раз в 30 секунд опрашивает `AT+CREG?` и
  `AT+CSQ` (fallback, если URC `+CREG` не приходят — например,
  модем не поддерживает `AT+CREG=2`).

При старте — `recoverGammuOrphans`: сообщения со
`source='gammu-outbox'` без соответствующей записи в
`gammu_outbox` переводятся в `failed` (сервис упал между
записью в БД и перемещением файла).

## Хранилище

SQLite через `modernc.org/sqlite` (pure Go, без CGO).
Одно соединение (`SetMaxOpenConns(1)`) — SQLite не любит
конкурентные записи.

Миграции — `//go:embed migrations/*.sql`, применяются при старте,
идемпотентны (`schema_migrations`).

Схема:

- `messages` — все SMS (in/out, все статусы);
- `delivery_reports` — история DR (аудит + защита от гонок);
- `pending_references` — брони TP-MR (8-битный, защита от
  коллизий, TTL);
- `pending_parts`, `pending_part_bodies` — multipart-сборка
  входящих;
- `imported_files` — дедупликация Gammu-импорта (mtime);
- `gammu_outbox` — состояние файлов в outbox: retries,
  last_error, priority, message_id.

Retention — фоновая очистка по `SMS_RETENTION`. TTL для
`pending_parts` — 15 минут.

## Gammu file backend

Совместим с `gammu-smsd` Files backend. Каталоги задаются
относительно `GAMMU_ROOT`:

- `inbox` — сюда пишутся принятые SMS;
- `outbox` — отсюда читаются `OUT*.txt` и ставятся в очередь;
- `sent` — успешно отправленные;
- `error` — исчерпаны попытки / не удалось разобрать.

**Watcher** (в `gammu.Manager`) поллит outbox раз в
`GAMMU_WATCH_INTERVAL`, обрабатывает по одному файлу за проход
(не параллельно — чтобы не гнать SMS в модем одновременно).
При неудаче приоритет понижается `A→B→…→Z`, файл возвращается
в outbox. По достижении `GAMMU_OUTBOX_MAX_ATTEMPTS` или
приоритета `Z` — файл в error.

**Экспорт принятых SMS** — `Service.ingestSMS` после сохранения
в БД дополнительно пишет файл в inbox через `gammu.InboxWriter`.
Ошибка записи файла не блокирует приём — только лог.

**Конвертер** `gammu-smsdrc → .env` — парсит INI-файл,
маппит `[gammu]`/`[smsd]` на переменные `.env`, отчитывается
об игнорированном. `MaxRetries` в gammu-smsd — это число
**повторов после первой** попытки; в smsgateway
`GAMMU_OUTBOX_MAX_ATTEMPTS` — **всего попыток**. Конвертер
делает `N+1`.

## Web UI

Стек: `net/http` + `html/template` (embed) + Bootstrap 5.2 +
htmx 4.0 (локально в `static/vendor/`). Локализация — go-i18n/v2,
ru/lv/en. CSP — `default-src 'self'` (без CDN). CSRF —
SameSite=Lax + проверка Origin/Referer для не-GET.

Страницы: dashboard, inbox, outbox, compose, logs (SSE),
setup (watcher портов + probe + save + самопроверка).

### Hot-reload через SIGHUP

`POST /setup/save` пишет `SERIAL_PORT` в `.env` и в фоне шлёт
`SIGHUP` процессу. `main.go` ловит SIGHUP, делает graceful
shutdown (останавливает URC-цикл, отправку, gammu, HTTP API,
Web UI, закрывает БД и модем через `sync.Once`), затем
`syscall.Exec("/proc/self/exe", ...)` — заменяет образ
процесса. PID сохраняется, systemd не замечает рестарт.

### SSE

`GET /logs/stream` — живой поток логов. `logging.Buffer` —
кольцевой буфер на 1000 записей + pub/sub. `Subscribe` атомарно
возвращает снимок + канал — нет дыры между снимком и
подпиской. При переполнении канала шлём `event: warning` с
числом пропущенных записей. Keepalive `: keepalive` каждые 15s.

`GET /setup/ports/stream` — аналогично для списка портов.

## Пакеты из sim800ctl

Импортируются с самого начала:

- `github.com/ik-vidya/sim800ctl/pkg/pdu` — PDU encode/decode
  (GSM-7, UCS-2, UDH, multipart);
- `github.com/ik-vidya/sim800ctl/pkg/number` — нормализация
  номеров (зарезервирован для будущего использования).

Локальная разработка — через `go.work`:
```
go-smsd/
├── go.work
├── sim800ctl/
└── smsgateway/
```
```go
go 1.26

use (
    ./sim800ctl
    ./smsgateway
)
```

CI — `GOWORK=off` (эмуляция без workspace). `go.sum` содержит
`sim800ctl`, `go.mod` — `require ...@latest` (или `replace`
для локальной разработки).

## Профили модемов

`Profile` — интерфейс:
- `Name()`, `Match(Info)`, `Workarounds()`,
  `PreInitHook(ctx, ModemHook)`, `PreSendHook(ctx, ModemHook)`.

Реализованы: `generic`, `sim800`, `quectel`, `telit`, `huawei`.

`Workarounds`:
- `ForceUCS2ForAllSMS` — всегда UCS-2 (SIM800L искажает GSM-7);
- `UseCSMPForSRR` — `AT+CSMP=49,167,0,0` перед отправкой (иначе
  DR не приходят на SIM800L);
- `AlignReadStorage` — `AT+CPMS` перед чтением (read≠receive);
- `PinAttemptsAT` — `AT+SPIC` для проверки попыток PIN.

`Auto(Info)` выбирает по Manufacturer/Model. Порядок — SIM800
первым (самый специфичный), потом Quectel/Telit/Huawei, иначе
Generic.

## Ошибки и retry

`IsTransientError` — транзиентные ошибки: таймаут, SIM800-текст
(`Temporary failure`, `No network service`, `Network timeout`,
`Network busy`), числовые CMS (38, 41, 42, 47, 331, 332, 500).
Постоянные (например, `+CMS ERROR: 305 Invalid destination`) —
без retry, сразу `failed`.

Backoff: 1s / 5s между попытками; всего 3 попытки.

## Notifications

`Notifier` интерфейс: `Notify(ctx, level, err)`, `Close() error`.
Реализации: `Noop`, `TelegramNotifier` (async, bounded queue
64, dedup 5 минут, rate limit 1 сообщение/сек).

Используется в:
- `main.go` — критическая ошибка старта;
- `service.notifyCritical` / `notifyWarning` — падения Init,
  ошибки gammu, паника в API handler;
- `SendTestSMS` — результат самопроверки (Warning для timeout,
  Critical для failed).

## Логирование

`log/slog` с уровнями `debug|info|warn|error` и форматом
`text|json`. `NewWithBuffer(level, format, buf)` дублирует
записи в `Buffer` для страницы `/logs`.

`Buffer` — кольцевой на 1000 записей. При переполнении
вытесняются старые. Подписчики (SSE-клиенты) не блокируют
других: медленный подписчик теряет записи, но не тормозит
остальных.

## CLI

`cmd/smsgateway` на cobra. Подкоманды:

- `version` — показать версию;
- `migrate` — применить миграции БД и выйти;
- `list-ports` — список последовательных портов;
- `detect-modem` — обнаружить модемы, опционально записать
  порт в `.env` (`--write-config`);
- `probe` — опрос модема + генерация скелета профиля;
- `gammu-outbox` — разовый прогон outbox-директории;
- `convert-gammu-config` — конвертер `gammu-smsdrc → .env`.

Без подкоманды — запуск сервиса (требуется `--config`).
