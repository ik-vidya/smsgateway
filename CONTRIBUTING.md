# Как участвовать

## Проблемы

Нашли баг — открывайте issue. Приложите:

- Версию утилиты (`smsgateway version`).
- Модель и прошивку модема:

      sim800ctl -p PORT exec 'AT+CGMM'
      sim800ctl -p PORT exec 'AT+CGMR'

- Отчёт диагностики: `sim800ctl -p PORT diagnose --all-safe --report report.json`.

## Pull requests

1. Код покрыт тестами (`*_test.go`).
2. `go vet ./...`, `go test ./...` проходят.
3. Протестировано на реальном модеме — опишите в описании PR.
4. Комментарии в коде — на русском, идентификаторы — на английском.
5. Trailer `Assisted-by:` в коммите.

## Использование AI-ассистентов

См. аналогичный раздел в репозитории [sim800ctl](https://github.com/ik-vidya/sim800ctl/blob/main/CONTRIBUTING.md).

## Стиль

- Идиоматичный Go, `gofmt -s`, `go vet`.
- Никакого CGO.
- Graceful shutdown, context везде, где нужен.
- SQLite через `modernc.org/sqlite` (не mattn/go-sqlite3, не glebarez).
