# Развёртывание

## Требования

- Linux (тестировалось на x86_64 и ARMv7 — Raspberry Pi).
- Доступ к последовательному порту модема (`/dev/ttyUSB0`,
  `/dev/serial/by-id/...`). Пользователь, под которым
  работает сервис, должен быть в группе `dialout` (или
  `uucp`, зависит от дистрибутива).
- 2G/4G-модем с поддержкой AT-команд.
- Для отправки/приёма SMS — SIM с положительным балансом.

## Сборка

```bash
git clone https://git.vidya.lv/vidya/smsgateway.git
cd smsgateway
make build
```

Бинарник — `bin/smsgateway`. Для Raspberry Pi (ARMv7):

```bash
make dist
# или прицельно:
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
    go build -o bin/smsgateway-armv7 ./cmd/smsgateway
```

## Конфигурация

Скопировать пример и заполнить:

```bash
cp .env.example .env
$EDITOR .env
```

Обязательные поля:
- `API_KEY` — длинная случайная строка (минимум 32 символа);
- `DB_PATH` — путь к SQLite-файлу (например,
  `/var/lib/smsgateway/sms.db`);
- `SERIAL_PORT` — путь к модему (или `none` для теста без
  модема).

Найти порт можно командой:

```bash
./bin/smsgateway list-ports
# или
./bin/smsgateway detect-modem
```

Для нескольких модемов на одной машине — используйте
`/dev/serial/by-id/...` вместо `/dev/ttyUSB*` (последний
переезжает между перезагрузками).

## Миграции БД

При первом запуске и после обновления:

```bash
./bin/smsgateway migrate --config /etc/smsgateway/.env
```

Каталог `DB_PATH` создаётся автоматически, если его нет.

## systemd

Готовый unit-файл — `scripts/systemd/smsgateway.service`.
Копировать и адаптировать под свой путь:

```ini
[Unit]
Description=SMS Gateway
After=network.target

[Service]
Type=simple
User=smsgateway
Group=dialout
WorkingDirectory=/opt/smsgateway
ExecStart=/opt/smsgateway/bin/smsgateway --config /etc/smsgateway/.env
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=5

# Логи — в journald, не в файл.
StandardOutput=journal
StandardError=journal

# Защита.
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/smsgateway /var/spool/sms

[Install]
WantedBy=multi-user.target
```

Установка:

```bash
sudo useradd -r -s /usr/sbin/nologin -G dialout smsgateway
sudo mkdir -p /opt/smsgateway /etc/smsgateway /var/lib/smsgateway
sudo cp -r bin /opt/smsgateway/
sudo cp .env /etc/smsgateway/.env
sudo chown -R smsgateway:dialout /var/lib/smsgateway
sudo cp scripts/systemd/smsgateway.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now smsgateway
sudo systemctl status smsgateway
```

Логи:

```bash
sudo journalctl -u smsgateway -f
```

**Hot-reload через SIGHUP** — можно менять `SERIAL_PORT` в
`.env` без остановки сервиса:

```bash
sudo systemctl reload smsgateway
# или
sudo kill -HUP $(pgrep -f 'smsgateway --config')
```

`ExecReload=/bin/kill -HUP $MAINPID` в unit-файле делает
`systemctl reload` эквивалентным.

## Docker

```dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/smsgateway ./cmd/smsgateway

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/smsgateway /app/smsgateway
ENTRYPOINT ["/app/smsgateway"]
CMD ["--config", "/etc/smsgateway/.env"]
```

Сборка и запуск:

```bash
docker build -t smsgateway .
docker run -d \
    --name smsgateway \
    --device /dev/serial/by-id/usb-Silicon_Labs_CP2102_...:/dev/smsmodem \
    -v $(pwd)/.env:/etc/smsgateway/.env:ro \
    -v smsgateway-data:/var/lib/smsgateway \
    -p 127.0.0.1:8080:8080 \
    -p 127.0.0.1:8081:8081 \
    smsgateway
```

В `.env` — `SERIAL_PORT=/dev/smsmodem`, `DB_PATH=/var/lib/smsgateway/sms.db`.

**Внимание:** контейнеру нужен доступ к `/dev/serial/by-id/*`
с правильными правами. Обычно `--device` работает, но в
некоторых системах нужно `--privileged` (не рекомендуется) или
`--group-add dialout`.

## Несколько модемов на одной машине

Один процесс = один модем. Для нескольких — несколько
systemd-инстансов с разными `.env`:

```ini
# /etc/systemd/system/smsgateway@.service
[Unit]
Description=SMS Gateway (%i)
After=network.target

[Service]
Type=simple
User=smsgateway
Group=dialout
EnvironmentFile=/etc/smsgateway/%i.env
ExecStart=/opt/smsgateway/bin/smsgateway --config /etc/smsgateway/%i.env
ExecReload=/bin/kill -HUP $MAINPID
Restart=always

[Install]
WantedBy=multi-user.target
```

Файлы `/etc/smsgateway/modem1.env`, `modem2.env`, … — с
разными `SERIAL_PORT` (обязательно by-id!), разными
`DB_PATH`, разными `HTTP_ADDR` и `WEBUI_ADDR`.

Запуск:

```bash
sudo systemctl enable --now smsgateway@modem1 smsgateway@modem2
```

## Миграция с gammu-smsd

Если у вас уже работает `gammu-smsd` с файловым бэкендом:

1. Сконвертировать конфиг:

   ```bash
   ./bin/smsgateway convert-gammu-config \
       --from /etc/gammu-smsdrc \
       --to /etc/smsgateway/from-gammu.env \
       --verbose
   ```

2. Проверить результат глазами. Конвертер маппит только
   совместимые опции; остальное (hooks, DeliveryReport, …)
   игнорируется. `Service` должен быть `FILES` — для `MYSQL` /
   `PGSQL` конвертер выдаст warning, миграция потребует ручной
   работы.

3. Дополнить `.env` обязательными полями (`API_KEY`,
   `DB_PATH`, `HTTP_ADDR` и т.д.) — конвертер их не создаёт.

4. **Остановить `gammu-smsd`** перед запуском `smsgateway`,
   иначе оба процесса будут бороться за `/dev/ttyUSB*` и
   файлы в `GAMMU_ROOT`:

   ```bash
   sudo systemctl stop gammu-smsd
   sudo systemctl disable gammu-smsd
   ```

5. Запустить `smsgateway migrate` и `smsgateway --config ...`.

Обратная совместимость файлового бэкенда: `smsgateway` читает
и пишет файлы в том же формате, что `gammu-smsd`. Если
включить `GAMMU_ROOT` в `.env`, оба могут работать
параллельно — но только с разными `GAMMU_ROOT` и разными
портами, что обычно не нужно.

## Мониторинг

- `/healthz` — простой healthcheck (без авторизации).
- `/api/v1/status` — расширенный статус (модем, БД, счётчики).
- `/api/v1/diagnostics` — полный отчёт по AT-командам.
- `/logs` в Web UI — live-поток логов.
- journalctl — если запущен через systemd.
- Telegram — критические ошибки (если настроен).

## Резервное копирование

Всё состояние — в SQLite-файле (`DB_PATH`). Резервная копия:

```bash
sqlite3 /var/lib/smsgateway/sms.db ".backup '/backup/sms-$(date +%F).db'"
```

Или остановить сервис и скопировать файл:

```bash
sudo systemctl stop smsgateway
sudo cp /var/lib/smsgateway/sms.db /backup/
sudo systemctl start smsgateway
```

Файлы Gammu (если включён `GAMMU_ROOT`) — отдельно, это
пользовательские данные.

## Обновление

```bash
cd smsgateway
git pull
make build
sudo systemctl stop smsgateway
sudo cp bin/smsgateway /opt/smsgateway/bin/
sudo systemctl start smsgateway
```

Или через `systemctl reload` после замены бинарника, если
менять только `.env` — `make build` не нужен. Миграции
применяются автоматически при старте, отдельный `migrate`
не обязателен (но рекомендуется для проверки перед
рестартом в продакшене).

## Режимы переподключения модема

В `.env` можно задать `MODEM_RECONNECT_MODE`:

| Режим | Поведение | Когда использовать |
|---|---|---|
| `systemd` (по умолчанию) | Программа делает 10 попыток открыть порт (5с между ними). Если не выходит — выходит с ошибкой. Systemd (с `Restart=always`) перезапускает её через `RestartSec`. | Запуск через systemd (с udev или без) |
| `standalone` | Программа делает 60 попыток (1с между ними). Если не выходит — вызывает `syscall.Exec` и перезапускает себя (PID сохраняется). | Запуск вручную (`./bin/smsgateway`) |
| `container` | 10 попыток (5с). Затем выход с ошибкой. Перезапуск делает podman/docker. | Запуск в Podman/Docker |

**Примечание про udev:** udev — это не отдельный режим программы. Это правило хоста, которое делает `systemctl stop` при выдергивании и `systemctl start` при подключении модема. Он отлично дополняет режим `systemd`, но программа о его существовании не знает.
