# HTTP/JSON REST API

Base path: `/api/v1`. Аутентификация — `X-API-Key` или
`Authorization: Bearer <key>`. `/healthz` — без авторизации.

## Аутентификация

Ключ задаётся в `.env` (`API_KEY` или `API_KEYS=key1,key2,...`).
Сравнение — constant-time (защита от timing-атак). Если ключей
несколько, проходит любой из списка.

Заголовки:

```
X-API-Key: <key>
```
или
```
Authorization: Bearer <key>
```

При отсутствии или неверном ключе — `401 Unauthorized`.

## Формат ответа

- Успех: JSON-объект, соответствующий эндпоинту.
- Ошибка: `{"error":{"code":"...","message":"..."}}`.
- `Content-Type: application/json; charset=utf-8`.

Коды ошибок:

| Код | HTTP | Когда |
|---|---|---|
| `bad_request` | 400 | Некорректный JSON, отсутствуют обязательные поля, неверный формат |
| `unauthorized` | 401 | Нет или неверный API-ключ |
| `not_found` | 404 | Запись не найдена |
| `conflict` | 409 | Конфликт состояния |
| `internal_error` | 500 | Внутренняя ошибка (БД, шаблон и т.п.) |
| `unavailable` | 503 | Сервис не сконфигурирован (например, нет модема для test-sms) |

## Эндпоинты

### `GET /healthz`

Без авторизации. Простой healthcheck: «процесс жив».
Возвращает 200 всегда, если процесс отвечает на HTTP.
Не проверяет БД или модем.

```json
{"status": "ok"}
```

### `GET /readyz`

Без авторизации. Расширенный healthcheck: «сервис готов
обслуживать запросы». Для healthcheck-проб docker, k8s,
reverse-proxy.

Проверяет:
- **БД** — критично. Недоступна → `503 not_ready`;
- **модем** — информационно. Если сконфигурирован, но не
  зарегистрирован в сети, это `warnings` (не `503`): SMS
  встают в очередь и уйдут, когда появится сеть.

**Ответ `200 OK`:**

```json
{
  "status": "ready",
  "checks": {
    "database": {"ok": true}
  },
  "warnings": []
}
```

Или с предупреждениями (модем не сконфигурирован):

```json
{
  "status": "ready",
  "checks": {
    "database": {"ok": true}
  },
  "warnings": ["modem not configured"]
}
```

Или модем сконфигурирован, но не в сети:

```json
{
  "status": "ready",
  "checks": {
    "database": {"ok": true}
  },
  "warnings": ["modem configured but not registered in network"]
}
```

**Ответ `503 Service Unavailable`:**

```json
{
  "status": "not_ready",
  "checks": {
    "database": {"ok": false, "error": "sql: database is closed"}
  }
}
```

Таймаут проверки БД — 2 секунды.

### `GET /api/v1/status`

Состояние сервиса, БД, модема, статистика по статусам.

```json
{
  "status": "ok",
  "uptime": "1h23m45s",
  "messages": {
    "queued": 0,
    "sending": 0,
    "sent": 3,
    "delivered": 12,
    "failed": 1,
    "sent_no_dr": 0,
    "received": 42
  },
  "database": {
    "path": "./data/smsgateway.db",
    "healthy": true
  },
  "modem": {
    "configured": true,
    "port": "/dev/ttyUSB0",
    "profile": "sim800",
    "manufacturer": "SIMCOM_Ltd",
    "model": "SIMCOM_SIM800L",
    "revision": "Revision:1418B04SIM800L24",
    "imei": "865674030283119",
    "signal": 17,
    "network": 1,
    "network_text": "зарегистрирован (домашняя сеть)"
  }
}
```

Поля `modem` присутствуют, если `service` сконфигурирован.
Если модем не сконфигурирован (`SERIAL_PORT=none`), `modem`
содержит `configured: false` и пустые поля.

### `POST /api/v1/messages`

Отправить SMS. Ставит сообщение в очередь; отправка —
асинхронно через модем (цикл `sendLoop` в сервисе).

**Тело:**

```json
{
  "to": "+37121234567",
  "text": "Hello",
  "request_id": "optional-client-id"
}
```

Поля:
- `to` — обязателен, формат `+<код><номер>`, 5–20 символов;
- `text` — обязателен, до 4000 символов;
- `request_id` — опционален, для трассировки (логируется, в БД
  не сохраняется).

**Ответ:** `202 Accepted`

```json
{
  "id": 42,
  "status": "queued"
}
```

**Ошибки:** `400 bad_request` — неверный формат полей.

Жизненный цикл сообщения:
`queued → sending → sent → delivered` (или `failed`/`sent_no_dr`).
Финальные статусы — `delivered`, `failed`, `sent_no_dr`.

### `GET /api/v1/messages`

Список сообщений с фильтрами и пагинацией.

Query-параметры (все опциональны):

| Параметр | Формат | По умолчанию |
|---|---|---|
| `direction` | `in` / `out` | все |
| `status` | `queued`/`sending`/`sent`/`delivered`/`failed`/`sent_no_dr`/`received` | все |
| `peer` | подстрока номера | все |
| `since` | RFC3339 (`2026-10-15T12:00:00Z`) | без ограничения |
| `until` | RFC3339 | без ограничения |
| `limit` | 1–500 | 50 |
| `offset` | ≥ 0 | 0 |

Сортировка — `created_at DESC, id DESC` (новые сверху).

**Ответ:**

```json
{
  "items": [
    {
      "id": 42,
      "direction": "out",
      "status": "delivered",
      "peer": "+37121234567",
      "text": "Hello",
      "reference": 179,
      "retry_count": 0,
      "sent_at": "2026-10-15T12:00:02Z",
      "delivered_at": "2026-10-15T12:00:34Z",
      "created_at": "2026-10-15T12:00:00Z",
      "updated_at": "2026-10-15T12:00:34Z"
    }
  ],
  "total": 1,
  "limit": 50,
  "offset": 0
}
```

Поля:
- `reference` — TP-MR из `+CMGS: <ref>`; null, пока модем не
  подтвердил отправку;
- `retry_count` — число попыток отправки (0 для входящих);
- `error` — текст ошибки, если `status=failed`;
- `raw_pdu` — hex PDU (может быть пустым).

### `GET /api/v1/messages/{id}`

Одно сообщение по ID.

**Ответ:** `200 OK` — объект `MessageDTO` (та же структура,
что элемент `items` из `GET /api/v1/messages`).

**Ошибки:** `404 not_found` — сообщение не найдено.

### `DELETE /api/v1/messages/{id}`

Удалить сообщение.

**Ответ:** `204 No Content` — успех (без тела).

**Ошибки:** `404 not_found` — сообщение не найдено.

### `POST /api/v1/test-sms`

Отправить проверочную SMS на `SELF_NUMBER` и **дождаться
терминального статуса** или таймаута (`TEST_SMS_TIMEOUT`,
по умолчанию 30s).

Синхронный вызов: блокирует соединение до ответа или таймаута.
`WriteTimeout` HTTP-сервера учитывает `TEST_SMS_TIMEOUT` + 30s.

**Тело (опционально):**

```json
{"text": "smsgateway selftest"}
```

Если `text` не задан — используется `"smsgateway selftest HH:MM:SS"`.

**Ответ:**

```json
{
  "id": 43,
  "status": "delivered",
  "reference": 180,
  "sent_at": "2026-10-15T12:01:02Z",
  "delivered_at": "2026-10-15T12:01:14Z",
  "timed_out": false
}
```

Поля:
- `timed_out: true` — истёк `TEST_SMS_TIMEOUT`, терминального
  статуса не дождались. Модем мог отправить, но DR не пришёл
  (или в модеме нет SIM/сети).
- `error` — текст ошибки, если `status=failed`.

**Ошибки:**
- `500 internal_error` — если `SELF_NUMBER` не задан или
  `SendTestSMS` вернул ошибку;
- `503 unavailable` — сервис не сконфигурирован.

### `GET /api/v1/diagnostics`

Собрать диагностический отчёт модема. Опрос ATI/CGMI/CGMM/CGMR/
CGSN/CIMI/CCID/CPIN?/CSQ/CREG?/CGREG?/COPS?/CSCA?/CPMS?
Ошибка отдельной команды не прерывает сбор — поле остаётся
пустым, ответ сохраняется в `raw`.

**Ответ:**

```json
{
  "manufacturer": "SIMCOM_Ltd",
  "model": "SIMCOM_SIM800L",
  "revision": "Revision:1418B04SIM800L24",
  "imei": "865674030283119",
  "imsi": "247021003701891",
  "iccid": "89372038002032721077",
  "sim_status": "READY",
  "signal": {
    "rssi": 17,
    "ber": 0,
    "dbm": -79,
    "quality": "хороший"
  },
  "registration": {
    "status": 1,
    "text": "зарегистрирован (домашняя сеть)",
    "lac": "00DF",
    "cid": "156B"
  },
  "operator": {
    "name": "TELE2"
  },
  "smsc": "+3725509911",
  "storage": {
    "read": "SM",
    "write": "SM",
    "used": 0,
    "total": 50
  }
}
```

Параметр `?raw=1` — добавить поле `raw` с сырыми ответами
AT-команд (полезно для отладки, но в ответе могут быть IMEI,
IMSI, номера — не отдавайте наружу без необходимости).

**Ошибки:** `503 unavailable` — сервис не сконфигурирован.

## Версионирование

Текущая версия — `v1` (path `/api/v1`). При breaking changes
будет `/api/v2`, `/api/v1` останется доступным хотя бы на
один релиз.

## CORS

По умолчанию CORS-заголовки **не выставляются**: API рассчитан
на серверные интеграции (curl, внутренние сервисы). Если
понадобится доступ из браузера с другого origin — добавим
`API_CORS_ORIGINS` в конфиг (сейчас не реализовано).

## Rate limiting

Не реализован. При необходимости — на уровне reverse proxy
(nginx, traefik).
