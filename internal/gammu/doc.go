// Package gammu — Gammu file backend.
//
// Реализует совместимый с gammu-smsd файловый бэкенд:
//
//   - inbox  — сюда пишутся принятые модемом SMS;
//   - outbox — отсюда читаются файлы OUT*.txt и ставятся в очередь
//     отправки;
//   - sent   — сюда перемещаются успешно отправленные;
//   - error  — сюда перемещаются файлы с исчерпанными попытками
//     и те, что не удалось разобрать.
//
// Экспорт исходящих в OUT-файлы намеренно НЕ реализован: отправкой
// управляет наш сервисный слой через AT-команды.
//
// Формат имён (совместим с gammu-smsd Files backend):
//
//	IN<date>_<time>_<serial>_<sender>_<sequence>.<ext>
//	OUT<recipient>.<ext>
//	OUT<priority>_<recipient>_<serial>.<ext>
//	OUT<priority><date>_<time>_<serial>_<recipient>_<note>.<ext>
//
// Расширения: .txt, .txtd (DR), .txtf (flash), .txtdf (оба).
// WAP bookmark (.b) и SMS Backup XML (.smsbackup) не поддерживаются.
//
// Кодировки: UTF-8 (default и fallback), UTF-16LE/BE с BOM.
// Если GAMMU_INBOX_FORMAT задан, он влияет только на запись;
// при чтении кодировка определяется автоматически.
//
// См. docs/GAMMU-FORMAT.md и https://docs.gammu.org/smsd/file.html.
package gammu
