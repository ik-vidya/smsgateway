# Gammu file backend

## Статус

**Черновик.** Реализация — позже.

## Формат файлов

- Incoming: `IN<YYYYMMDD>_<HHMMSS>_<seq>_<sender>_<idx>.<ext>`
- Поля: `SMSC`, `Number`, `Text`, `TextDecoded`, `Coding`, `UDH`,
  `Class`, `Sent`, `Received`.
- Кодировки: UTF-8, UTF-16LE/BE (с BOM и без).

## Импорт

`-import-gammu` — разовый импорт из `GAMMU_IMPORT_DIR`.
Если `GAMMU_IMPORT_INTERVAL` задан — периодически.

## Экспорт

Если `FILES_BACKEND_DIR` задан — входящие пишутся туда в этом формате.

## Ссылки

- [Формат Gammu SMSD file backend](https://docs.gammu.org/smsd/file.html)
