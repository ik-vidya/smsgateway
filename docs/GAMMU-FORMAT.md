# Gammu file backend

Совместим с форматом файлов [gammu-smsd](https://docs.gammu.org/smsd/file.html),
а также с plain-text-форматом, который используется `rssmspool`.

## Директории

| Директория | Роль |
|---|---|
| `inbox`  | Принятые модемом SMS записываются сюда. Также сюда попадают DR-отчеты. |
| `outbox` | Отсюда читаются файлы `OUT*.txt` и ставятся в очередь отправки. |
| `sent`   | Успешно отправленные файлы перемещаются сюда. |
| `error`  | Файлы с исчерпанными попытками и те, что не удалось разобрать. |

## Формат имён

### Входящие (IN)
    IN<YYYYMMDD>_<HHMMSS>_<serial>_<sender>_<sequence>.<ext>

- `<sender>` — номер или alphanumeric-отправитель. Может содержать `_`.
- `<sequence>` — номер части multipart (00 для одиночной SMS).
- Содержимое — plain text (тело сообщения). Все метаданные в имени файла.

### Исходящие (OUT)
Три формы, как в gammu-smsd:

    OUT<recipient>.<ext>
    OUT<priority>_<recipient>_<serial>.<ext>
    OUT<priority><date>_<time>_<serial>_<recipient>_<note>.<ext>

- `<priority>` — одна буква `A`..`Z` (`A` — высший приоритет).
- `<serial>` — ID из внешней системы (сохраняется в БД как `gammu_serial`).
- `<date>_<time>` — время создания SMS в формате `YYYYMMDD_HHMMSS` (сохраняется в БД как `created_at`).
- `<ext>` — `txt`, `txtd` (DR), `txtf` (flash), `txtdf`.

## Интеграция с внешними системами (АСУ ПП)

### 1. Чтение из outbox
Когда программа забирает файл из `outbox`, она сохраняет в БД:
- `gammu_serial` — из поля `<serial>`.
- `created_at` — из `<date>_<time>` (если формат 3), иначе используется `time.Now()`.

### 2. Генерация Delivery Reports (DR)
Когда от оператора приходит подтверждение доставки (DR), программа
создает в папке `inbox` файл следующего формата:

    IN<YYYYMMDD>_<HHMMSS>_<serial>_<peer>_00.txt

- `<serial>` — это тот самый `gammu_serial` (ID из АСУ ПП).
- `<peer>` — номер получателя.
- Содержимое: `DELIVERED` или `FAILED`.

Этот файл синхронизируется обратно в АСУ ПП (например, через Syncthing).
Внешняя система парсит имя файла, находит `<serial>` и обновляет
статус записи в своей БД на «Доставлено» или «Ошибка».

## Приоритет текста

При определении текста приоритет такой:
1. `TextDecoded`, если задан.
2. `Text` как hex-PDU, если строка валидна как PDU.
3. `Text` как plain-текст.

## Кодировки

Все файлы проходят через `DecodeText` **до** парсинга: UTF-8 (default),
UTF-16LE/BE с BOM. Если формат `unicode`, при записи используется UTF-16LE с BOM.

Запись (в inbox и DR) — по `GAMMU_INBOX_FORMAT`:
- `utf8` / `standard` — UTF-8 без BOM (по умолчанию);
- `unicode` — UTF-16LE с BOM;
- `detail` — не поддерживается (SMS Backup XML).