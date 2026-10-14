# smsgateway

SMS-шлюз для AT-совместимых GSM-модемов. Работает как долгоживущий
сервис: принимает SMS через модем, сохраняет в SQLite и отдаёт через
HTTP API. Является более полной альтернативой `gammu-smsd`, написанной
на идиоматичном Go.

## Возможности (план)

- Отправка и приём SMS (GSM-7, UCS-2, multipart).
- Delivery reports через URC `+CDS`.
- HTTP/JSON REST API с API-ключом.
- SQLite (modernc.org/sqlite, без CGO) с retention.
- Gammu file backend (импорт/экспорт).
- Мульти-модемная абстракция: профили `sim800`, `quectel`, `telit`,
  `generic` (последний — по умолчанию).
- Опциональный административный веб-UI (htmx 4 + Bootstrap 5.2,
  локализация ru/lv/en, OIDC).
- Критические ошибки — в Telegram.
- `log/slog` с уровнями `debug|info|warn|error`.

## Требования

- Go 1.22+ для сборки.
- AT-совместимый GSM-модем.
- Для разработки удобно держать `sim800ctl` рядом — см. раздел
  «Связанные проекты».

## Связанные проекты

- [sim800ctl](https://github.com/ik-vidya/sim800ctl) — CLI-утилита
  для работы с SIM800L. Используется как инструмент диагностики
  при разработке `smsgateway`. Пакеты `pkg/pdu` и `pkg/number`
  переиспользуются.

## Сборка

    # Клонировать оба проекта рядом:
    git clone https://github.com/ik-vidya/sim800ctl.git
    git clone https://github.com/ik-vidya/smsgateway.git

    # В родительском каталоге создать go.work (см. пример в smsgateway/docs):
    go work init ./sim800ctl ./smsgateway

    cd smsgateway
    make build

## CLI

    smsgateway -config /path/to/.env
    smsgateway -config /path/to/.env -migrate
    smsgateway -config /path/to/.env -diagnostics
    smsgateway version

## Документация

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — архитектура, пакеты.
- [docs/API.md](docs/API.md) — HTTP API.
- [docs/GAMMU-FORMAT.md](docs/GAMMU-FORMAT.md) — формат Gammu file backend.
- [docs/WEBUI.md](docs/WEBUI.md) — административный веб-UI.
- [docs/MODEMS.md](docs/MODEMS.md) — профили модемов.

## Статус

Ранняя разработка. Пока реализован только фундамент: загрузка
конфигурации, логирование, CLI-каркас. Следующие шаги — storage (SQLite),
HTTP API, AT-транспорт с URC-каналом, сервисный слой.

## Лицензия

MIT — см. [LICENSE](LICENSE).
