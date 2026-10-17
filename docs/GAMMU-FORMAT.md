# Gammu file backend

Совместим с форматом файлов [gammu-smsd](https://docs.gammu.org/smsd/file.html),
а также с plain-text-форматом, который используется
`rssmspool` (syncmaps) и очень старыми версиями `gammu-smsd`
с `TransmitFormat=text`.

## Директории

| Директория | Роль |
|---|---|
| `inbox`  | Принятые модемом SMS записываются сюда. |
| `outbox` | Отсюда читаются файлы `OUT*.txt` и ставятся в очередь отправки. |
| `sent`   | Успешно отправленные файлы перемещаются сюда. |
| `error`  | Файлы с исчерпанными попытками и те, что не удалось разобрать. |

Все подкаталоги настраиваются отдельно (`GAMMU_INBOX`,
`GAMMU_OUTBOX`, `GAMMU_SENT`, `GAMMU_ERROR`), по умолчанию —
`inbox`, `outbox`, `sent`, `error`. Относительные пути
склеиваются с `GAMMU_ROOT`, абсолютные используются как есть.

## Формат имён

### Входящие

    IN<YYYYMMDD>_<HHMMSS>_<serial>_<sender>_<sequence>.<ext>

- `<serial>` — порядок приёма в пределах секунды (00..99). Первый
  свободный выбирается через `O_CREATE|O_EXCL`.
- `<sender>` — номер или alphanumeric-отправитель. Может содержать `_`.
- `<sequence>` — номер части multipart (00 для одиночной SMS).
- `<ext>` — `txt`.

### Исходящие

Три формы, как в gammu-smsd:

    OUT<recipient>.<ext>
    OUT<priority>_<recipient>_<serial>.<ext>
    OUT<priority><date>_<time>_<serial>_<recipient>_<note>.<ext>

- `<priority>` — одна буква `A`..`Z`, `A` — высший приоритет.
  Сортировка файлов лексикографическая: `OUTA…` < `OUTB…` < … < `OUTZ…`.
- `<serial>` — произвольный номер, в отправке не участвует.
- `<note>` — игнорируется.
- `<ext>` — `txt`. Дополнительно поддерживаются флаги в extension:
  - `txtd` — запросить delivery report (TP-SRR=1);
  - `txtf` — flash SMS (**не реализовано** на уровне PDU);
  - `txtdf` / `txtfd` — оба флага.

WAP bookmark (`.b`) и SMS Backup XML (`.smsbackup`) не
поддерживаются. Файл с таким расширением перемещается в
`error`.

## Содержимое файла: два формата

### Формат 1: `Key = Value` (классический gammu)

Построчный `Key = Value`. Значения в кавычках или без. Пустые
строки, комментарии (`#...`) и секции (`[...]`) игнорируются.

Поддерживаемые ключи:

| Ключ | Значение |
|---|---|
| `SMSC`        | Номер SMSC (для исходящих, обычно пусто) |
| `Number`      | Вторая сторона: получатель (out) или отправитель (in) |
| `Text`        | Hex-PDU или plain-текст |
| `TextDecoded` | Готовый текст (приоритет выше `Text`) |
| `Coding`      | `Default_No_Compression` (GSM-7) или `Unicode_No_Compression` (UCS-2) |
| `UDH`         | Hex UDH для multipart |
| `Class`       | Класс SMS (0..3) |
| `Sent`        | `YYYY-MM-DD HH:MM:SS` — для исходящих |
| `Received`    | `YYYY-MM-DD HH:MM:SS` — для входящих |

Пример:

    SMSC = "+3725509911"
    Number = "+37121234567"
    TextDecoded = "Привет, мир!"
    Coding = "Default_No_Compression"
    Received = "2026-10-15 12:00:34"

### Формат 2: plain text (rssmspool, старый gammu-smsd)

Тело SMS как есть, без структуры. Пример:

    Тест с rsmodule-tallin1 (tallin1 +37255691999): 2026-10-17 23:41:25

Адресат берётся из имени файла (`<recipient>` в
`OUT<priority><date>_<time>_<serial>_<recipient>_<note>.<ext>`).

**Автодетект формата.** `ParseFile` смотрит, есть ли в файле
хотя бы один **известный gammu-ключ** (из таблицы выше). Если
есть — парсим как `Key = Value`; если нет — всё содержимое
считается телом SMS. Наличие «=» само по себе не признак
gammu-формата: plain-текст может содержать «=»
(например, `Body = some text` — `Body` не из whitelist,
fallback сработает).

## Приоритет текста

При определении текста приоритет такой:

1. `TextDecoded`, если задан.
2. `Text` как hex-PDU, если строка валидна как PDU.
3. `Text` как plain-текст.

## Кодировки

Все файлы проходят через `DecodeText` **до** парсинга. Поддерживаются:

- **UTF-8** без BOM — используется как есть, если валиден и
  не содержит NUL-байт;
- **UTF-8 с BOM** (`EF BB BF`) — BOM снимается;
- **UTF-16LE с BOM** (`FF FE`) — декодируется в UTF-8;
- **UTF-16BE с BOM** (`FE FF`) — декодируется в UTF-8;
- **UTF-16LE без BOM** — fallback (если содержимое не валидно
  как UTF-8 или содержит NUL).

Порядок определения BOM → UTF-8 validity → UTF-16LE fallback
одинаково работает и для `Key = Value`-файлов, и для plain-text.
Пример: файл от rssmspool на диске в UTF-16LE с BOM
(`ff fe 22 04 35 04 41 04 42 04 ...`) — `DecodeText` снимает
BOM и декодирует в UTF-8, `ParseFile` видит нормальную строку.

Запись (в inbox) — по `GAMMU_INBOX_FORMAT`:
- `utf8` / `standard` — UTF-8 без BOM (по умолчанию);
- `unicode` — UTF-16LE с BOM;
- `detail` — не поддерживается (SMS Backup XML).

## Логика отправки

1. Watcher сканирует `outbox` каждые `GAMMU_WATCH_INTERVAL`
   (по умолчанию 5s). Файлы сортируются лексикографически.
2. Файл, изменённый меньше `grace period` (2s) назад,
   пропускается — он может ещё дописываться.
3. На каждое отправленное сообщение создаётся запись в
   `messages` (`source='gammu-outbox'`) и в `gammu_outbox`
   (состояние ретраев).
4. Успех → файл в `sent`, `messages.status='sent'`.
5. Ошибка → **имя файла не меняется**. Счётчик попыток
   (`retries`), пониженный приоритет (`priority`) и
   последняя ошибка (`last_error`) хранятся в БД
   (`gammu_outbox`). Файл остаётся в `outbox` под исходным
   именем, watcher подхватит его на следующем проходе.
6. `retries >= GAMMU_OUTBOX_MAX_ATTEMPTS` **или** приоритет
   уже `Z` → файл в `error` (с исходным именем),
   `messages.status='failed'`.

**Почему имя не меняется.** При пересборке имени из полной
формы `OUTA20261018_000647_6467_+371..._Admin.txtd` в
короткую `OUTB_+371..._99.txtd` теряются timestamp,
serial (обрезается до 99) и note. Пользователь видит в
`sent`/`error` файл, не имеющий отношения к исходному.
Сохранение имени даёт: тот же файл в `sent`/`error`,
что и был в `outbox`; счётчик — в БД.

## Восстановление после сбоя

- Если сервис упал между записью в БД и созданием `entry` —
  при следующем старте `OrphanOutboxMessages` находит такие
  сообщения и переводит в `failed`.
- Если файл из `outbox` удалён или переименован вне сервиса,
  при следующем сканировании запись в БД без файла
  игнорируется.

## Ограничения

- **Flash SMS (`.txtf`)** передаётся как флаг `flash` до
  `ModemSender.Send`, но `pdu.Build` в `sim800ctl` flash
  пока не поддерживает: SMS уходит как обычное, в лог —
  warning.
- **SMS Backup XML (`.smsbackup`)** не поддерживается. Файлы
  с таким расширением перемещаются в `error`.
- **WAP bookmark (`.b`)** не поддерживается.

## Диагностика

При `LOG_LEVEL=debug` в лог попадают строки:

```
gammu: файл прочитан    path=... raw_len=... raw_first_bytes=...
                        decoded_len=... decoded_first_chars=...
gammu: файл разобран    name=... fields_count=... has_textdecoded=...
                        has_text=... has_number=...
                        effective_len=... effective_source=...
```

По ним видно:
- какая кодировка была на диске (`raw_first_bytes`);
- что получилось после `DecodeText` (`decoded_first_chars`);
- сколько полей нашёл парсер (`fields_count`);
- откуда взялся текст (`effective_source`: `text_decoded` /
  `pdu` / `plain`);
- не пустой ли текст (`effective_len`).

Если файл попадает в `error` с «пустой текст», по этим строкам
сразу видно, почему: файл пустой, не удалось декодировать,
или (для gammu-формата) все поля пустые.

## CLI

    smsgateway gammu-outbox --config .env
    smsgateway gammu-outbox --config .env --dry-run

Первая — разовый прогон watcher-а. Вторая — только парсит
файлы и печатает план, не открывая модем и БД.
