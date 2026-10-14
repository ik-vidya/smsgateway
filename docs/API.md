# HTTP API

## Статус

**Черновик.** API ещё не реализован. Ниже — планируемая структура.

Base path: `/api/v1`. Аутентификация — `X-API-Key` или
`Authorization: Bearer <key>`. `/healthz` — без авторизации.

## Эндпоинты (план)

- `GET /healthz`
- `GET /api/v1/status` — состояние модема, БД, очереди.
- `POST /api/v1/messages` — отправить SMS.
- `GET /api/v1/messages` — список с фильтрами.
- `GET /api/v1/messages/{id}` — одна.
- `DELETE /api/v1/messages/{id}` — удалить.
- `POST /api/v1/test-sms` — проверочная SMS на SELF_NUMBER.
- `GET /api/v1/diagnostics` — JSON-отчёт диагностики.

## Формат ошибок

    {"error":{"code":"bad_request","message":"..."}}
