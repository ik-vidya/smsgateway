# smsgateway

Легковесный и надежный SMS-шлюз для AT-совместимых GSM-модемов (протестирован на SIM800L). 
Принимает и отправляет SMS, сохраняет историю в SQLite, предоставляет HTTP/JSON REST API и Web-интерфейс. 
Поддерживает интеграцию с внешними системами через файловый бэкенд Gammu и синхронизацию (например, Syncthing).

## Возможности

- **Отправка и прием SMS**: Поддержка текстовых сообщений, кириллицы (UCS-2) и длинных SMS (Multipart).
- **Web UI**: Удобный интерфейс для мониторинга, поиска, отправки сообщений и настройки модема.
- **Безопасность**: Поддержка HTTP Basic Auth для защиты Web UI.
- **REST API**: Полноценное API для интеграции с вашими приложениями.
- **Gammu File Backend**: Чтение из `outbox`, запись в `inbox`, автоматическая генерация отчетов о доставке (Delivery Reports).
- **Управление очередью**: Возможность отменить ошибочно отправленное сообщение или повторить отправку при сбое.

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

### Web UI и Безопасность
По умолчанию Web UI работает без пароля. Если вы выставляете его в сеть, включите Basic Auth:

~~~env
WEBUI_ENABLED=true
WEBUI_ADDR=127.0.0.1:8081
WEBUI_AUTH_MODE=basic
WEBUI_AUTH_USER=admin
WEBUI_AUTH_PASS=your_strong_password
~~~

### Модем
~~~env
SERIAL_PORT=/dev/serial/by-id/usb-1a86_USB2.0-Serial-if00-port0
MODEM_PROFILE=sim800
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

### Отправка (Outbox)
Внешняя система кладет файл `OUT*.txt` в папку `outbox`. Шлюз отправляет SMS и перемещает файл в `sent`.

### Получение (Inbox)
Принятые SMS сохраняются в базу и дублируются в папку `inbox` (в формате plain text).

### Отчеты о доставке (Delivery Reports)
Если внешней системе нужно знать статус доставки, шлюз генерирует специальные файлы в папке `inbox`:
`IN<дата>_<sms_id>_<номер>_00.txt` (с текстом `DELIVERED` или `FAILED`). 
`sms_id` берется из имени исходящего файла (Serial).

## REST API

Базовый путь: `/api/v1`. Аутентификация через заголовок `X-API-Key` или `Authorization: Bearer <key>`.

- `GET /healthz` — проверка работоспособности (без авторизации).
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