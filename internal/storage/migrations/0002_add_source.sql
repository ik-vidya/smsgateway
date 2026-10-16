-- 0002_add_source.sql
--
-- Добавляет в messages колонку source — откуда сообщение:
--   'api'          — создано через HTTP API (исходящее)
--   'modem'        — принято через URC +CMTI (входящее)
--   'gammu-outbox' — прочитано из файлового outbox Gammu (исходящее)
--
-- Существующие записи получают NULL; это допустимо и означает
-- "до введения source".

ALTER TABLE messages ADD COLUMN source TEXT;

CREATE INDEX IF NOT EXISTS idx_messages_source ON messages(source);
