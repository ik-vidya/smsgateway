# Связанные проекты

`smsgateway` использует пакеты из
[sim800ctl](https://github.com/ik-vidya/sim800ctl):

- `github.com/ik-vidya/sim800ctl/pkg/pdu` — PDU encode/decode
  (GSM-7, UCS-2, UDH, multipart). Используется в
  `internal/modem/sms.go` (отправка/приём) и
  `internal/gammu/parse.go` (декодирование PDU из Gammu-файла).
- `github.com/ik-vidya/sim800ctl/pkg/number` — нормализация
  номеров. Зарезервирован для будущего использования.

## Локальная разработка через go.work

Удобно держать оба репозитория рядом и связать их через
`go.work`:

```
go-smsd/
├── go.work              ← workspace
├── sim800ctl/
└── smsgateway/
```

Создание workspace:

```bash
cd go-smsd
go work init ./sim800ctl ./smsgateway
```

Файл `go.work`:

```go
go 1.26

use (
    ./sim800ctl
    ./smsgateway
)
```

После этого из `smsgateway` доступны импорты
`github.com/ik-vidya/sim800ctl/pkg/pdu` и т.д., и изменения
в `sim800ctl` сразу видны в `smsgateway`.

## go.mod smsgateway

```go
module github.com/ik-vidya/smsgateway

go 1.26

require (
    github.com/ik-vidya/sim800ctl v0.0.0-...
    ...
)
```

При локальной сборке Go берёт `sim800ctl` из workspace. Без
workspace — из сети по версии из `go.mod`.

## CI и `GOWORK=off`

CI на GitHub Actions **не использует workspace** (там только
`smsgateway`). Чтобы эмулировать локально:

```bash
GOWORK=off go build ./...
GOWORK=off go test ./...
GOWORK=off go mod tidy
```

Проверка, что `go.sum` содержит `sim800ctl`:

```bash
grep sim800ctl go.sum
```

Если пусто — `go mod tidy` не подтянул зависимость. Причина
обычно в том, что `import` в коде не соответствует `require`
в `go.mod`. Проверьте:

```bash
GOWORK=off go mod tidy -v
```

## Обновление версии sim800ctl

Если в `sim800ctl` вышел новый релиз:

```bash
cd smsgateway
go get github.com/ik-vidya/sim800ctl@latest
go mod tidy
```

При локальной разработке без релиза — `replace` в `go.mod`:

```go
replace github.com/ik-vidya/sim800ctl => ../sim800ctl
```

Но это только для локальной работы; в CI `replace` убирают
(иначе сборка в CI не найдёт `../sim800ctl`).
