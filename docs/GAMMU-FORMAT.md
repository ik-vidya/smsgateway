# Gammu file backend

Совместим с форматом файлов [gammu-smsd](https://docs.gammu.org/smsd/file.html).
Используется, когда задан `GAMMU_ROOT`.

## Директории

Все подкаталоги настраиваются отдельно (`GAMMU_INBOX`, `GAMMU_OUTBOX`,
`GAMMU_SENT`, `GAMMU_ERROR`), по умолчанию — `inbox`, `outbox`, `sent`,
`error`. Относительные пути склеиваются с `GAMMU_ROOT`, абсолютные
используются как есть.

| Директория | Роль |
|---|---|
| `inbox`  | Принятые модемом SMS записываются сюда. |
| `outbox` | Отсюда читаются файлы `OUT*.txt` и ставятся в очередь отправки. |
| `sent`   | Успешно отправленные файлы перемещаются сюда. |
| `error`  | Файлы с исчерпанными попытками и те, что не удалось разобрать. |

Все каталоги создаются при старте, если их нет.

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
  - `txtf` — flash SMS (пока **не реализовано** на уровне PDU,
    см. «Ограничения»);
  - `txtdf` / `txtfd` — оба флага.

WAP bookmark (`.b`) и SMS Backup XML (`.smsbackup`) — не поддерживаются.
Файл с таким расширением будет перемещён в `error` с записью в логе.

## Формат содержимого

Построчный `Key = Value`. Значения в кавычках или без. Пустые строки,
комментарии (`#...`) и секции (`[...]`) игнорируются.

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

При определении текста приоритет такой:

1. `TextDecoded`, если задан.
2. `Text` как hex-PDU, если строка валидна как PDU.
3. `Text` как plain-текст.

Пример входящего:

    SMSC = "+3725509911"
    Number = "+37121234567"
    TextDecoded = "Привет, мир!"
    Coding = "Default_No_Compression"
    Received = "2026-10-15 12:00:34"

Пример исходящего:

    Number = "+37121234567"
    TextDecoded = "Hello"
    Coding = "Default_No_Compression"

## Кодировки

- **Чтение.** Автоопределение: BOM UTF-8 / UTF-16LE / UTF-16BE →
  соответствующая кодировка. BOM нет — если содержимое валидный UTF-8
  без NUL-байт, то UTF-8; иначе UTF-16LE. Это покрывает все файлы,
  которые генерирует gammu-smsd на разных платформах.
- **Запись.** Задаётся в `GAMMU_INBOX_FORMAT`:
  - `utf8` / `standard` — UTF-8 без BOM (по умолчанию);
  - `unicode` — UTF-16LE с BOM;
  - `detail` — не поддерживается (SMS Backup XML), при попытке
    записать будет ошибка при старте сервиса.

## Логика отправки

1. Watcher сканирует `outbox` каждые `GAMMU_WATCH_INTERVAL` (по
   умолчанию 5s). Файлы сортируются лексикографически.
2. Файл, изменённый меньше `grace period` (2s) назад, пропускается —
   он может ещё дописываться.
3. На каждое отправленное сообщение создаётся запись в `messages`
   (`source='gammu-outbox'`) и в `gammu_outbox` (состояние ретраев).
4. Успех → файл в `sent`, `messages.status='sent'`.
5. Ошибка → файл переименовывается с понижением приоритета
   (`A→B→…→Z`) и возвращается в `outbox`. Счётчик попыток в БД.
6. `retries >= GAMMU_OUTBOX_MAX_ATTEMPTS` **или** приоритет уже `Z` →
   файл в `error`, `messages.status='failed'`.

## Восстановление после сбоя

- Если сервис упал между записью в БД и созданием `entry` — при
  следующем старте `OrphanOutboxMessages` находит такие сообщения
  и переводит в `failed`.
- Если файл из `outbox` удалён или переименован вне сервиса, при
  следующем сканировании запись в БД без файла игнорируется
  (проверяется через `GetOutboxEntry` по актуальному пути).

## Ограничения

- **Flash SMS (`.txtf`)** передаётся как флаг `flash` до
  `ModemSender.Send`, но `pdu.Build` в `sim800ctl` flash пока не
  поддерживает: SMS уходит как обычная, в логе — warning. Когда
  `pdu.Build` научится, достаточно будет прокинуть флаг.
- **SMS Backup XML (`.smsbackup` / `GAMMU_INBOX_FORMAT=detail`)** не
  поддерживается. Файлы с таким расширением перемещаются в `error`.
- **WAP bookmark (`.b`)** не поддерживается.

## CLI

    smsgateway gammu-outbox --config .env
    smsgateway gammu-outbox --config .env --dry-run

Первая команда — разовый прогон watcher-а (отправляет всё, что
лежит в `outbox`, и завершается). Вторая — только парсит файлы и
печатает план, не открывая модем и БД.
