
**Go 1.26** (не 1.27, см. `go.mod`). `go.work` связывает `sim800ctl`
и `smsgateway`; `smsgateway` импортирует
`github.com/ik-vidya/sim800ctl/pkg/pdu` и `.../pkg/number`.

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

**Почему именно такой порядок:**

- `gofmt -s -w .` до commit → `make build`'s `gofmt` не найдёт
  изменений → version без `-dirty`.
- Пре-чек до commit → при FAIL нет мусорного коммита в истории
  (это уже случалось: `70ade33` ушёл в `main` некомпилируемым).
- `make build` после commit → `git describe --always` видит
  актуальный коммит (а не предыдущий), `--dirty` не выставляется.
- `make build` до push → если сборка упала (что маловероятно после
  `go build ./...`, но `-ldflags`/`-trimpath`/CGO могут дать сбой),
  коммит остаётся локальным, push не выполняется. Не публикуем
  нерабочее.
- Проверка `git status --short` после `make build` — защита от
  случая, когда `tidy` или `fmt` внутри `make build` неожиданно
  изменили файл (например, обновили `go.sum`). Auto-amend
  гарантирует, что `version` в артефакте совпадает с SHA коммита.

**Что НЕ делать:**

- `make build` до `git commit` — version будет `-dirty` и со
  старым SHA (уже случалось: `870e00c-dirty` при HEAD
  `b7145013`).
- `git commit` без пре-чека — некомпилируемый коммит в `main`
  (уже случалось: `70ade33` с `undefined: strings`).
- `git push` до `make build` — на удалённой машине окажется
  версия, которую вы не проверили.
- `git commit --amend` после `git push` — перезапись уже
  опубликованной истории (нужен force-push, ломает коллег).
- `gofmt -w internal/ cmd/` вместо `gofmt -s -w .` — `make fmt`
  использует `-s`, а `make build` вызывает `make fmt`.
  Несовпадение флагов → `-dirty`.
- `#!/bin/bash -euo pipefail` — ядро передаёт `-euo pipefail`
  как один аргумент, bash падает на «строке 0». Только
  `#!/bin/bash` + `set -euo pipefail` отдельной строкой.
- Выдавать файл «частями» или разрывать блоком кода посередине —
  копирование/скачивание ломается. См. «Формат выдачи кода».

---

## 3. Текущее состояние пакетов

Все пакеты **реализованы и покрыты тестами**:

| Пакет | Что делает | Тестов |
|---|---|---|
| `internal/config` | `.env`, валидация, `envwrite` (правка `.env`), `ParseDuration` | 20+ |
| `internal/logging` | `slog` + ring buffer (1000) + pub/sub для SSE + `Filter`/`SnapshotFiltered`/`Entry.Text` | 20+ |
| `internal/storage` | SQLite (`modernc.org/sqlite`, без CGO), миграции, CRUD, retention | 14+ |
| `internal/modem` | AT-транспорт (reader, URC-канал), `SendSMS` (PDU), `ReadSMS`, PIN-unlock, `discover` (порты/модемы); CREG/CGREG/CBC — ответы, не URC | 53+ |
| `internal/profiles` | `generic`, `sim800`, `quectel`, `telit`, `huawei`; `Get()` case-insensitive, `Auto()` | 5 |
| `internal/diagnostics` | `Collect()` — опрос AT, `Error` + `CachedStatus`/`CachedRSSI` при таймауте | 20+ |
| `internal/probe` | Опрос модема + генерация скелета профиля; `--creg-timeout` для CREG/CGREG | 14 |
| `internal/service` | URC-цикл, sendLoop (retry), pending refs, DR, networkLoop, `EnqueueOutgoing`, `Registered()` | 40+ |
| `internal/api` | REST API: `/healthz`, `/readyz`, `/api/v1/*` (включая `/messages/{id}/delivery-reports`), `/api/v1/status` (+version/build_time) | 25+ |
| `internal/notify` | Telegram (async, dedup, rate limit) + Noop | 13 |
| `internal/gammu` | Gammu file backend: watcher outbox, retry без переименования, inbox/inbox writer, конвертер `gammu-smsdrc → .env`, plain-text fallback | 55+ |
| `internal/webui` | Bootstrap 5.2 + htmx 4.0 (локально в `static/vendor/`), i18n ru/lv/en, dashboard/inbox/outbox/compose/logs(SSE)/setup/messages/{id} (со страницей DR), `/logs/download` (text/jsonl), `/setup/save` (SIGHUP hot-reload) | 10+ |
| `cmd/smsgateway` | cobra: `version`, `migrate`, `list-ports`, `detect-modem`, `probe`, `gammu-outbox`, `convert-gammu-config`, сервис | — |

**CI:** `.github/workflows/ci.yml` — `gofmt -l`, `go vet`, `go test`,
`-race` для параллельных, vendor-check, cross-compile (linux/amd64,
linux/arm64, linux/armv7, windows/amd64).

---

## 4. Что НЕ реализовано

1. **OIDC для Web UI (B4)** — отложено. `WEBUI_AUTH_MODE=none` —
   единственный режим. Ответственность за доступ — firewall/reverse
   proxy.
2. **Профили `quectel`/`telit`** — только заглушки `Generic`. Ждут
   реального железа.
3. **Команда `probe` для автогенерации профиля** — реализована
   и проверена на двух живых модемах SIM800L (2026-10-18):
   профиль `sim800` определяется автоматически, все четыре
   обхода (`ForceUCS2ForAllSMS`, `UseCSMPForSRR`,
   `AlignReadStorage`, `PinAttemptsAT`) подтверждены. На других
   модемах (Quectel, Telit, Huawei) автогенерация не проверялась.
4. **`AT+CREG=1` вместо `=2`** — обсуждалось, но не сделано (URC
   `+CREG` приходят часто, залогировано только изменение `stat`).
5. **`make deploy`** — target есть (в вашем локальном `Makefile`), в
   CI не проверяется.
6. **Тестирование модема из Web UI** — обсуждалось. Расширить
   `/setup` блоками «Диагностика» (из `diagnostics.Collect`) и
   «Входящие» (`ReadSMS(unread)`) — полезно. Сырые AT-команды —
   **не делать** (риск сломать модем). Пока не реализовано.

---

## 5. Известные подводные камни

- **SIM800L в 2G.** Два живых модема на 2026-10-18: один в
  роуминге (`CREG stat=5`), другой местный (`stat=1`). Оба
  работают. После фикса роутинга `+CREG`/`+CGREG` (см. §8)
  `AT+CREG?`/`AT+CGREG?` стабильно отвечают, `networkLoop`
  обновляет `networkStat` через оба пути (URC + опрос).
- **DR приходит через 60–90 секунд** после `+CMGS`. `TEST_SMS_TIMEOUT`
  по умолчанию **120s**. `api-test.sh` считает `sent` успехом
  (DR может прийти позже).
- **`GAMMU_WATCH_INTERVAL=5s`** — при длинной очереди скан может
  идти минутами (последовательная обработка). Второй экземпляр
  `smsgateway` на том же outbox — **опасен** (сценарий дубля на
  retry). Один процесс на outbox обязателен.
- **`make build` внутри обёртки** — после commit, до push.
  Иначе version `-dirty`.
- **`/logs/download` — без авторизации** (как `/logs` и
  `/logs/stream`). Web UI слушает `0.0.0.0:8086` — доступ в сети.
- **`imei` может быть невалидным**, если UART SIM800L глючит при
  таймауте (`sanitizeValue` стрипает `\r`, но не восстанавливает
  цифры). `Raw["AT+CGSN.warning"]` сигнализирует.
- **`SMSC` приходит в UCS-2 hex** (`002B0033...`) — `parseSMSC`
  пробует ASCII-hex, затем UTF-16BE, затем возвращает исходное.
- **`git add -A` в обёртке** — захватывает все `M`/`??` в дереве.
  Уже случалось: `0b18fe4` уехал с `PROMPT.md`, `.gitignore`,
  `setup_test.go` под сообщением «probe: …». Защита — шаг 0 в
  обёртке (`git status --short` → стоп).
- **`#!/bin/bash -euo pipefail`** — не работает: ядро передаёт
  `-euo pipefail` как один аргумент → `invalid parameter name`
  на «строке 0». Только `#!/bin/bash` + `set -euo pipefail`
  отдельной строкой.
- **Вложенные разделители в блоке кода** — `~~~markdown` с
  `~~~bash` внутри ломает копирование файла целиком. Внешний
  разделитель не должен встречаться в содержимом.
- **`probe` не вызывает `Modem.Init`** — поэтому `CSCS: "IRA"`
  и `CPMS: "ME",…` в отчёте — это сырое состояние модема.
  `PreInitHook` профиля (CSCS=UCS2, CPMS="SM","SM","SM")
  сработает при старте сервиса. Не пугаться.

---

## 6. Проверка на живом модеме

Два модема SIM800L (`Revision:1418B04SIM800L24`):
- местный — `CREG stat=1`;
- в роуминге 2G Латвии — `CREG stat=5`.

Оба — на **другой машине** (`rsmodule-tallin1`,
192.168.199.65:8085 API / 8086 Web UI). В текущем окружении
модемов нет — все unit-тесты на mock-модемах.

**Отправка SMS проверена** на живом модеме (2026-10-18):
`sendLoop` → `AT+CMGS` → `+CMGS: <ref>` → `+CDS` (DR) — работает
end-to-end. `probe` проверен на двух модемах: `Auto()` вернул
`sim800`, все четыре обхода подтверждены, `--creg-timeout`
(10s) и фикс роутинга `+CREG`/`+CGREG` убирают потерю ответов.

Если задача требует проверки на железе — пользователь выполнит
сам. Готовый тестовый скрипт — `test-tallin1.sh` в корне (три
прогона `api-test.sh`).

**Деплой на удалённую:**

~~~bash
make build
scp bin/smsgateway rsdt@192.168.199.65:/tmp/smsgateway-new
ssh rsdt@192.168.199.65 '
    BIN=$(readlink -f /proc/$(pgrep -f "smsgateway --config")/exe)
    sudo cp /tmp/smsgateway-new "$BIN"
    sudo chmod +x "$BIN"
    sudo pkill -f "smsgateway --config"
    sleep 1
    cd /home/rsmodule-common/bin && ./smsgateway.sh &
'
~~~

Проверка:

~~~bash
curl -s -H "X-API-Key: $KEY" \
    http://192.168.199.65:8085/api/v1/status | jq '{version, build_time}'
~~~

Если `build_time` старее последнего коммита — деплой не сделан.

---

## 7. Порядок действий в новом чате

1. Прочитать `PROMPT.md` (этот файл).
2. Прочитать `smsgateway-snapshot-*.md` (приложен) — там полные
   исходники, git status, git log, дерево файлов, интерфейсы.
3. Спросить пользователя: **«С какой задачей продолжаем?»** —
   с учётом раздела 4.
4. Получить подтверждение по деталям (если задача крупная —
   согласовать объём MVP, например для Web UI).
5. Выдать **полный файл** (новый) или **Python-скрипт** (правка) —
   **одним блоком**, см. раздел 2 «Формат выдачи кода».
6. Пользователь применит → gofmt/build/vet/test/commit/push.
7. Перед деплоем на удалённую — **напомнить**: `make build`, `scp`,
   `pkill`, перезапуск.

---

## 8. Известные баги-грабли (не повторять)

- **Slice-append с указателем**: `r := &m.script[i]` +
  `append(m.script[:i], m.script[i+1:]...)` → указатель на сдвинутый
  элемент. Копировать значение: `r := m.script[i]`.
- **Порядок в Close**: сначала `transport.Close()` (разблокирует
  Read), затем `wg.Wait()`, затем `close(closedCh)`.
- **Mock через `io.Pipe`**: `.Write` блокируется. `sync.Cond` +
  `bytes.Buffer`.
- **Указатель на элемент slice после удаления** — то же, что
  slice-append.
- **`time.Duration` в JSON** — int64 наносекунд. Для человекочитаемого
  — отдельное поле `WaitMillis int64`.
- **`mockModem.SendSMS` без `References`** — service ждёт
  `res.References[0]`. Автозаполнять из `res.Reference`.
- **`go mod edit -require=@v0.0.0`** — требует сетевой версии.
  `@latest` или `replace=../sim800ctl`.
- **Anchors в Python-патчах** — ломаются на gofmt. Использовать
  `replace_func` (по имени функции с балансировкой скобок).
- **`:=` в Python** — это Go, не Python. Виноват дважды за сессию.
- **`for i < len(src):` в Python** — тоже Go. `while`.
- **Патч `exec` в `diagnostics.go`** — anchor не совпал из-за
  форматирования; использовать `replace_func`.
- **`statusKey` vs `statusLabelKey`** — в funcMap шаблона нужно
  `statusLabelKey` (в handlers.go).
- **`strings` в `logs.go`** — забыт импорт при добавлении
  `handleLogsDownload`.
- **`#!/bin/bash -euo pipefail`** — недопустим: ядро передаёт
  `-euo pipefail` как ОДИН аргумент bash → `invalid parameter
  name` (сообщение «строка 0»). Правильно: `#!/bin/bash` +
  `set -euo pipefail` отдельной строкой.
- **`git add -A` при грязном дереве** — посторонние правки уедут
  в коммит с чужим сообщением (уже случалось: `0b18fe4`).
  Защита — шаг 0 в обёртке.
- **Регекспы CREG/CGREG матчили ответ на AT+CREG?, а не URC.**
  По 3GPP TS 27.007 ответ на AT+CREG? — `+CREG: <n>,<stat>`
  (два числа перед кавычками), URC — `+CREG: <stat>` (одно
  число). Регексп `^\+CREG:\s*(\d+)\s*,\s*(\d+)` матчил только
  ответ, поэтому: (а) ответ на `Exec("AT+CREG?")` попадал в
  `urcCh` и терялся; (б) настоящий URC уходил в `respCh` и
  выбрасывался `drain'ом` перед следующей командой. Побочно:
  `+CBC` (в 3GPP никогда не URC) тоже терялся. Симптом:
  `probe` показывает `CREG: —`, `CBC: — (нет)`, при этом
  `COPS` знает оператора. Фикс — в `internal/modem/urc.go`:
  `reCREG`/`reCGREG` матчат URC-формат, `+CBC` убран из
  `tryParseURC` (тип `URCCBC` сохранён для будущего).
  Регрессионные тесты: `TestExecCREGResponse`,
  `TestExecCGREGResponse`, `TestExecCBCResponse`.
- **Вложенные разделители в блоке** — `~~~markdown` с `~~~bash`
  внутри ломает копирование. Внешний разделитель не должен
  встречаться в содержимом.
- **Разрыв файла на части** — «часть 1/2», «продолжение в следующем
  сообщении» недопустимы. Файл — целиком в одном блоке.

---

## 9. Окружение разработчика

- Go 1.26.
- Репозитории в `/mnt/Data-work/Projects/go-projects/go-smsd/`.
- `go.work` связывает `sim800ctl` и `smsgateway`.
- SSH-ключ к GitHub.
- Hook `prepare-commit-msg` добавляет `Assisted-by: DeepSeek chat`.
- CI на GitHub Actions: `go mod download`, `go vet`, `go test`,
  build без CGO, cross-compile (linux/amd64, linux/arm64,
  linux/armv7, windows/amd64).
- Два модема SIM800L на удалённой машине (не в текущем
  окружении): один местный (`stat=1`), один в роуминге
  (`stat=5`).

---

## 10. Git-версионирование

Semver через git-теги. `git describe --tags --always --dirty`
поддерживается `Makefile` (`VERSION`). Текущий тег — `v0.1.0`.

Порядок `make build` (после `commit`, до `push`), шаг 0 (чистота
дерева) и требования к shebang — см. раздел 2 «Обёртка для
Python-скрипта».