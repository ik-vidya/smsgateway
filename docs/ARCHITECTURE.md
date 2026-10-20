# Архитектура

## Назначение

`smsgateway` — долгоживущий сервис для приёма/отправки SMS через
AT-совместимый GSM-модем. Сохраняет сообщения в SQLite, отдаёт
их через HTTP/JSON REST API, предоставляет административный
Web UI, поддерживает Gammu file backend для совместимости с
`gammu-smsd`.

## Карта пакетов

~~~
cmd/smsgateway/             — точка входа, CLI (cobra).
internal/config/            — загрузка .env, валидация.
internal/logging/           — log/slog + кольцевой буфер + SSE.
internal/storage/           — SQLite, миграции, репозитории.
internal/modem/             — AT-транспорт, SendSMS, ReadSMS, PIN.
internal/profiles/          — профили модемов (sim800, generic и др.).
internal/diagnostics/       — сбор AT-команд для /diagnostics.
internal/probe/             — опрос модема для скелета профиля.
internal/service/           — оркестрация: URC, sendLoop, DR, networkLoop.
internal/api/               — HTTP-сервер REST API.
internal/notify/            — Telegram-нотификатор.
internal/gammu/             — Gammu file backend (watcher, DR-файлы).
internal/webui/             — Web UI (Basic Auth, SSE, поиск).
~~~

## Поток данных

~~~
Modem ←serial→ AT-транспорт
                    │
                    ├─ URC → Service.handleURC
                    │            ├─ +CMTI → ReadSMS → storage
                    │            │           └─ Gammu inbox
                    │            ├─ +CDS  → HandleDeliveryReport
                    │            │           └─ Gammu DR (inbox)
                    │            └─ +CREG → networkStat
                    │
                    └─ Exec / ExecPrompt ← Service
                                              │
                                              ├─ sendLoop
                                              │    └─ SendSMS (с паузой 1s)
                                              │
                                              └─ networkLoop
~~~

## AT-транспорт

Главная особенность — **асинхронный URC-канал**.
- **Reader-горутина** читает порт, распознаёт URC (`+CMTI`, `+CDS`, `+CREG` и др.) и ответы на команды.
- **Exec** — синхронный вызов, ждёт `OK`/`ERROR`.
- **ExecPrompt** — для `AT+CMGS` (отправка PDU).

## Сервисный слой

`Service` — координатор.
- **urcLoop** — читает URC. При отключении модема сохраняет fatalErr и отменяет контекст.
- **sendLoop** — раз в секунду выбирает `queued` из БД, отправляет с retry.
- **pendingLoop** — чистит истекшие `pending_references` (TP-MR).
- **networkLoop** — опрос `AT+CREG?` и `AT+CSQ` раз в 30 секунд.

## Длинные SMS (Multipart)

- **Входящие**: Части складываются в `pending_parts`. Когда приходят все — текст склеивается и сохраняется в `messages`. В Web UI есть предупреждение о незавершённых сборках.
- **Исходящие**: Отправляются с паузой 1 секунда между частями (защита от блокировки сети) и лимитом 10 частей.

## Gammu Delivery Reports

Для интеграции с внешними системами (АСУ ПП) при получении DR создаётся файл в папке `inbox`. В имени файла используется `gammu_serial` (ID из внешней системы, сохранённый из имени OUT-файла). Это замыкает цикл синхронизации без HTTP-API.

## Web UI

Стек: `net/http` + `html/template` + Bootstrap + htmx.
- **Basic Auth** (`WEBUI_AUTH_MODE=basic`).
- **Поиск по тексту** в Inbox/Outbox.
- **Кнопки «Отменить»** (queued) и **«Повторить»** (failed).
- **Hot-reload** через SIGHUP (без разрыва PID).

## Профили модемов

`Profile` — интерфейс: `Name()`, `Match()`, `Workarounds()`, `PreInitHook()`, `PreSendHook()`.
Реализованы: `generic`, `sim800`, `quectel`, `telit`, `huawei`.

## Безопасный деплой

`make deploy` — делает бэкап старого бинарника, rsync нового, перезапускает systemd, проверяет `is-active`. При провале — авто-откат.