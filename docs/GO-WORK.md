# Связанные проекты

`smsgateway` использует пакеты из
[sim800ctl](https://github.com/ik-vidya/sim800ctl):

- `pkg/pdu` — PDU encode/decode.
- `pkg/number` — нормализация номеров.

Для локальной разработки удобно держать оба репозитория рядом и
связать их через `go.work`:

    go-smsd/
    ├── go.work              <- go work init ./sim800ctl ./smsgateway
    ├── sim800ctl/
    └── smsgateway/

Файл `go.work`:

    go 1.22

    use (
        ./sim800ctl
        ./smsgateway
    )

После этого из `smsgateway` доступны импорты
`github.com/ik-vidya/sim800ctl/pkg/pdu` и т.д.
