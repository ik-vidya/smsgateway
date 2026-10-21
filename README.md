# smsgateway

Легковесный и надежный SMS-шлюз для AT-совместимых GSM-модемов (протестирован на SIM800L). 
Принимает и отправляет SMS, сохраняет историю в SQLite, предоставляет HTTP/JSON REST API и Web-интерфейс. 
Поддерживает интеграцию с внешними системами через файловый бэкенд Gammu и синхронизацию (например, Syncthing).

## Возможности

- **Отправка и прием SMS**: Поддержка текстовых сообщений, кириллицы (UCS-2) и длинных SMS (Multipart).
- **Web UI с ролями**: Удобный интерфейс с поддержкой ролей Администратор / Пользователь (только чтение).
- **Уведомления в Telegram**: 4 уровня важности (OK, Warning, Error, Critical), локализация (ru/lv/en) и имя модема в сообщениях.
- **REST API**: Полноценное API для интеграции с вашими приложениями.
- **Gammu File Backend**: Чтение из `outbox`, запись в `inbox`, автоматическая генерация отчетов о доставке (Delivery Reports).

## Быстрый старт

### 1. Сборка
Для сборки потребуется Go 1.26+.

~~~bash
git clone https://git.vidya.lv/vidya/smsgateway.git
cd smsgateway
make build
~~~

Бинарник появится в `bin/smsgateway`.

### 2. Настройка
Скопируйте пример конфигурации и отредактируйте его:

~~~bash
cp .env.example .env
nano .env
~~~

Обязательно укажите:
- `API_KEY` (или `API_KEYS`) — ключ для доступа к REST API.
- `SERIAL_PORT` — путь к модему (например, `/dev/ttyUSB0` или `/dev/serial/by-id/usb-...`).
- `DB_PATH` — путь к файлу базы данных SQLite.

### 3. Запуск

~~~bash
sudo ./bin/smsgateway --config .env
~~~

## Конфигурация (.env)

Все настройки задаются через переменные окружения в файле `.env`.

### Идентификация модема
~~~env
# Человекочитаемое имя модема (для Telegram и Web UI).
# Если пусто, генерируется автоматически как <hostname>-<port> 
# (например, rsmodule-tallin1-ttyUSB0).
MODEM_DESCRIPTION=

SERIAL_PORT=/dev/serial/by-id/usb-1a86_USB2.0-Serial-if00-port0
MODEM_PROFILE=sim800
~~~

### Web UI и Безопасность (RBAC)
Поддерживается два уровня доступа: Администратор (полный доступ) и Пользователь (только просмотр входящих и исходящих SMS).

~~~env
WEBUI_ENABLED=true
WEBUI_ADDR=127.0.0.1:8081
WEBUI_AUTH_MODE=basic

# Пользователь (только чтение: Inbox, Outbox)
WEBUI_AUTH_USER=operator
WEBUI_AUTH_PASS=user_password

# Администратор (отправка SMS, логи, настройка модема)
WEBUI_AUTH_ADMIN=admin
WEBUI_AUTH_ADMIN_PASS=admin_password

# Если WEBUI_AUTH_ADMIN не задан, права администратора 
# наследуются от обычного пользователя (обратная совместимость).
~~~

### Уведомления в Telegram
~~~env
TELEGRAM_BOT_TOKEN=your_bot_token
TELEGRAM_CHAT_ID=your_chat_id
# Язык уведомлений: ru | lv | en
TELEGRAM_LANG=ru
~~~

### Gammu File Backend (Опционально)
Если вы хотите обмениваться сообщениями с внешней системой (АСУ ПП, 1С) через файлы:

~~~env
GAMMU_ROOT=/var/spool/sms
GAMMU_INBOX=inbox
GAMMU_OUTBOX=outbox
GAMMU_SENT=sent
GAMMU_ERROR=error
~~~

## Интеграция с внешними системами (Gammu)

`smsgateway` может работать как замена `gammu-smsd`, используя файловый обмен.

- **Отправка (Outbox)**: Внешняя система кладет файл `OUT*.txt` в папку `outbox`. Шлюз отправляет SMS и перемещает файл в `sent`.
- **Получение (Inbox)**: Принятые SMS сохраняются в базу и дублируются в папку `inbox` (в формате plain text).
- **Отчеты о доставке (Delivery Reports)**: Если внешней системе нужно знать статус доставки, шлюз генерирует специальные файлы в папке `inbox`: `IN<дата>_<sms_id>_<номер>_00.txt` (с текстом `DELIVERED` или `FAILED`).

## REST API

Базовый путь: `/api/v1`. Аутентификация через заголовок `X-API-Key` или `Authorization: Bearer <key>`.

- `GET /healthz` — проверка работоспособности.
- `GET /api/v1/status` — состояние сервиса и модема.
- `POST /api/v1/messages` — поставить SMS в очередь на отправку.
- `GET /api/v1/messages` — список сообщений (с фильтрами).
- `GET /api/v1/diagnostics` — диагностика модема.

Подробнее в [docs/API.md](docs/API.md).

## Развертывание (systemd)

Для продакшена рекомендуется использовать systemd. 
Пример юнита `smsgateway@.service` и правила `udev` для создания стабильного симлинка `/dev/sms-gateway` находятся в папке `scripts/`.

## Разработка

~~~bash
make test      # запустить тесты
make test-race # запустить тесты с race detector
make ci        # полный прогон (gofmt, vet, test, build)
make deploy    # безопасный деплой на удаленный сервер (с бэкапом и откатом)
~~~

## Лицензия

MIT License. См. файл [LICENSE](LICENSE).