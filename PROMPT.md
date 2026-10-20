# PROMPT.md — точка входа для нового чата

> Прочитай этот файл целиком. Затем жди приложенный snapshot
> (`smsgateway-snapshot-*.md`) — там полные исходники. Не задавай
> уточняющих вопросов, если ответ есть в snapshot или здесь.
> Если чего-то действительно не хватает — уточни один раз, коротко.

---

## 1. Проект

**smsgateway** — SMS-шлюз для AT-совместимых GSM-модемов.
Долгоживущий сервис: принимает SMS через модем, сохраняет в SQLite,
отдаёт через HTTP/JSON REST API, имеет Web UI, поддерживает Gammu
file backend.

**Расположение:**

~~~
/mnt/Data-work/Projects/go-projects/go-smsd/
├── go.work              ← workspace, связывает оба модуля
├── sim800ctl/           ← CLI-утилита (публичный)
└── smsgateway/          ← основной проект (приватный, текущий)
~~~

**Go 1.26** (не 1.27, см. `go.mod`). `go.work` связывает `sim800ctl`
и `smsgateway`; `smsgateway` импортирует
`git.vidya.lv/vidya/sim800ctl/pkg/pdu` и `.../pkg/number`.

**CI зелёный**, HEAD на момент создания промпта — см. snapshot.

---

## 2. Жёсткие правила работы

**Язык:**

- С пользователем — **русский**.
- Комментарии в коде — **русский**.
- Идентификаторы, JSON, SQL, имена таблиц — **английский**.
- Логи, ошибки в коде — **английский** (кроме Web UI, там i18n).
- README, docs/ — **английский**.

**Формат выдачи кода — критично:**

- **Один файл = один блок кода = один heredoc.** Любой скрипт,
  документ или иной файл, подлежащий копированию или скачиванию,
  выдаётся **целиком внутри одного блока**. Никаких «часть 1,
  часть 2», «продолжение в следующем сообщении», разрывающих
  файл.
- **Внешний разделитель блока не должен встречаться в
  содержимом.** Если внутри файла уже есть `~~~` (bash-примеры,
  markdown-примеры) — внешний блок делается через тройные
  бэктики. Если внутри есть тройные бэктики — внешний через
  тильды. Вложенные разделители одного типа ломают копирование
  файла целиком.
- **Полный файл одним блоком.** Никогда «вставьте три строки».
- При правке существующих файлов — **один Python-скрипт** с полными
  содержимыми изменяемых файлов, идемпотентный, печатает что применено.
- Один файл = один блок кода = один heredoc.
- Bash-скрипты и markdown-примеры — через `~~~` (тильды), не через
  тройные бэктики.
- **Никаких unified diff** — ломается на gofmt.

**Python-скрипты (правила):**

- В начале скрипта — **диагностика**: `grep` по ключевым маркерам,
  чтобы видеть, что уже применено, а что нет.
- Патчить через **`replace_func`** (по имени функции с балансировкой
  скобок) или через `append_if_missing` (дописать в конец), а не
  через `src.replace(old_anchor, new)` — anchors ломаются на gofmt.
- Если anchor не совпал — **не падать**, а `print("! ...")` и продолжить.
- В конце — печатать, что проверять (`gofmt`, `build`, `test`).

**Git:**

- Коммит: тема, пустая строка, «Изменения», «Тесты», «Проверено».
- Trailer `Assisted-by: DeepSeek chat` — добавляется hook'ом
  `prepare-commit-msg`; в шаблоне обёртки (см. ниже) он указан
  явно на случай, если hook не настроен. В текущем репозитории
  hook настроен — двойного trailer'а не будет.
- После коммита — `git push`.

**Перед коммитом (обязательно):**

~~~bash
gofmt -s -w .
GOWORK=off go mod tidy
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test ./... -timeout 60s
~~~

`GOWORK=off` эмулирует CI.

---

**Обёртка для Python-скрипта — обязательный порядок:**

При выдаче Python-скрипта всегда прикладывать bash-обёртку
строго по этому шаблону. Порядок не менять — иначе `version`
в бинарнике будет `-dirty` или со старым SHA.

~~~bash
#!/bin/bash
set -euo pipefail
cd /mnt/Data-work/Projects/go-projects/go-smsd/smsgateway

# 0. Дерево должно быть чистым (иначе git add -A в шаге 4
#    подхватит посторонние M/??, как это уже было в 0b18fe4).
if [ -n "$(git status --short)" ]; then
    echo "WARNING: дерево не чистое:"
    git status --short
    echo "Сделайте git stash или отдельный коммит, потом повторите."
    exit 1
fi

# 1. Применить изменения.
python3 apply-XXX.py

# 2. Форматирование ВСЕГО дерева до commit
#    (совместимо с `make fmt`: gofmt -s -w ., иначе
#    `make build` найдёт не-упрощённые файлы и сделает -dirty).
gofmt -s -w .

# 3. Пре-чек: set -e остановит на первой ошибке.
#    При FAIL — коммита НЕ будет.
GOWORK=off go mod tidy
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test ./... -timeout 60s

# 4. Commit — только если пре-чек прошёл.
git add -A
git commit -m "$(cat <<'EOF'
<тема>

<тело: Изменения / Тесты / Проверено>

Assisted-by: DeepSeek chat
EOF
)"

# 5. make build — ПОСЛЕ commit, ДО push.
#    Тогда version: без -dirty, с актуальным SHA.
make build
./bin/smsgateway version

# 6. Если make build (tidy/fmt) изменил файлы — amend и пересборка,
#    иначе version снова станет -dirty.
if [ -n "$(git status --short)" ]; then
    echo "WARNING: make build изменил файлы — amend"
    git add -A
    git commit --amend --no-edit
    make build
    ./bin/smsgateway version
fi

# 7. push — только если всё выше прошло.
git push
~~~

**Shebang.** `#!/bin/bash -euo pipefail` — ошибка. Ядро при
`execve` передаёт `-euo pipefail` как **один** аргумент bash,
и bash пытается разобрать его как имя опции: `invalid parameter
name` на «строке 0». Правильно — `#!/bin/bash` и `set -euo
pipefail` **отдельной строкой**.

**Шаг 0 (чистота дерева).** Не косметика: `git add -A` в шаге 4
захватывает всё дерево. Уже случалось (`0b18fe4`): вместе с
probe-фиксом в коммит уехали `PROMPT.md`, `.gitignore`,
`setup_test.go` под чужим сообщением. Шаг 0 — защита.

---

## 3. Текущее состояние пакетов (v1.1.0)

Все пакеты **реализованы и покрыты тестами**:

| Пакет | Что делает | Тестов |
|---|---|---|
| `internal/config` | `.env`, валидация, `envwrite` (правка `.env`), `ParseDuration`, Basic Auth | 20+ |
| `internal/logging` | `slog` + ring buffer (1000) + pub/sub для SSE | 20+ |
| `internal/storage` | SQLite, миграции, CRUD, retention, pending_parts, gammu_serial | 15+ |
| `internal/modem` | AT-транспорт, `SendSMS` (PDU + пауза 1s), `ReadSMS`, `OpenSerialWithRetry` | 55+ |
| `internal/profiles` | `generic`, `sim800`, `quectel`, `telit`, `huawei` | 5 |
| `internal/diagnostics` | `Collect()` — опрос AT, CREG/COPS/CSQ контекст | 20+ |
| `internal/probe` | Опрос модема + генерация скелета профиля | 14 |
| `internal/service` | URC-цикл, sendLoop, multipart-сборка, DR, networkLoop, cancel/retry | 45+ |
| `internal/api` | REST API: `/healthz`, `/readyz`, `/api/v1/*` | 25+ |
| `internal/notify` | Telegram (async, dedup, rate limit) + Noop | 13 |
| `internal/gammu` | Gammu backend: watcher, DR-файлы, конвертер `gammu-smsdrc → .env` | 55+ |
| `internal/webui` | Basic Auth, Поиск по тексту, Отмена/Повтор, DR-блок, SSE | 10+ |
| `cmd/smsgateway` | cobra: все команды | — |

**CI:** `.forgejo/workflows/ci.yml` — `gofmt -l`, `go vet`, `go test`,
`-race`, vendor-check, cross-compile.

---

## 4. Что НЕ реализовано

1. **OIDC для Web UI (B4)** — отложено. `WEBUI_AUTH_MODE=basic` закрывает
   базовые потребности.
2. **Профили `quectel`/`telit`** — только заглушки `Generic`.
3. **Flash SMS** — не поддерживается на уровне `pdu.Build`.
4. **Встроенное автообновление** — сознательно не делаем. Безопаснее
   использовать `make deploy` (с бэкапом и авто-откатом).

---

## 5. Известные подводные камни

- **SIM800L в 2G.** Два живых модема: один в роуминге (`stat=5`),
  другой местный (`stat=1`). CREG/CGREG стабильны.
- **Длинные SMS (Multipart).** Входящие собираются в `pending_parts`.
  Исходящие отправляются с паузой (1s) и лимитом 10 частей.
  Ошибка `Temporary failure` возникает при отсутствии роуминга,
  теперь логируется с контекстом `CREG/COPS/CSQ`.
- **Gammu интеграция (АСУ ПП).** DR-файлы (`DELIVERED`/`FAILED`)
  создаются в `inbox`. `gammu_serial` и `created_at` берутся из
  имени файла (`fn.Serial`, `fn.Time`).
- **`make deploy`** — безопасный (бэкап, is-active check, авто-откат).
- **Web UI Basic Auth** — при `WEBUI_AUTH_MODE=basic` требует
  `WEBUI_AUTH_USER` и `WEBUI_AUTH_PASS`.

---

## 6. Проверка на живом модеме

Модемы SIM800L на удалённой машине (`rsmodule-tallin1`).
Проверка отправки/приёма, DR, long SMS.

**Деплой на удалённую:**
~~~bash
make deploy
~~~

**Откат:**
~~~bash
make rollback
make rollback-to VERSION=20261020-193807
~~~

---

## 7. Порядок действий в новом чате

1. Прочитать `PROMPT.md` (этот файл).
2. Прочитать `smsgateway-snapshot-*.md` (приложен).
3. Спросить: **«С какой задачей продолжаем?»**
4. Получить подтверждение по деталям.
5. Выдать **полный файл** или **Python-скрипт** — **одним блоком**.
6. Пользователь применит → commit/push.
7. Деплой через `make deploy`.

---

## 8. Известные баги-грабли (не повторять)

- **Slice-append с указателем**: `r := &m.script[i]` + `append(...)`.
- **Порядок в Close**: `transport.Close()` -> `wg.Wait()` -> `close()`.
- **time.Duration в JSON** — int64 наносекунд.
- **mockModem.SendSMS без References** — service ждёт `res.References[0]`.
- **Anchors в Python-патчах** — ломаются на gofmt. `replace_func`.
- **statusKey vs statusLabelKey** — в funcMap нужен `statusLabelKey`.
- **Shebang** — `#!/bin/bash -euo pipefail` падает.
- **`git add -A` при грязном дереве** — посторонние файлы в коммите.
- **Makefile** — табы, а не пробелы (иначе `missing separator`).
- **Incomplete multipart** — не забывайте удалять `pending_parts` по TTL.
- **`drainRespCh` busy loop** — при закрытом канале нужен `if !ok { return }`.
- **SIGHUP reload** — нужен `godotenv.Overload` перед `exec`.
- **EvalSymlinks** — сравнение портов строкой ломается на симлинках.
- **`readerLoop` busy loop** — при `(0, nil)` нужен `time.After(10ms)`.

---

## 9. Окружение разработчика

- Go 1.26.
- Forgejo: `https://git.vidya.lv/vidya/smsgateway`.
- Два модема SIM800L на `rsmodule-tallin1`.
- Ветка `main`, семвер через `make bump`.

---

## 10. Git-версионирование

Текущий тег — `v1.1.0`. `git describe` в `Makefile`.
Деплой — только через `make deploy` (с защитой от сбоев).