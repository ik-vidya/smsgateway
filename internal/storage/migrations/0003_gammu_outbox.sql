-- 0003_gammu_outbox.sql
--
-- Состояние файлов в outbox-директории Gammu. Gammu-smsd хранит
-- счётчик попыток в памяти и теряет его при рестарте; здесь —
-- персистентно. Используется для:
--   * учёта попыток (retries);
--   * запоминания последней ошибки;
--   * связи файла с записью в messages (чтобы outbox-сообщения
--     были видны в API и UI).

CREATE TABLE gammu_outbox (
    path        TEXT PRIMARY KEY,     -- текущий абсолютный путь файла
    message_id  INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    retries     INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT,
    priority    TEXT NOT NULL,        -- текущая буква A-Z
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_gammu_outbox_message  ON gammu_outbox(message_id);
CREATE INDEX idx_gammu_outbox_priority ON gammu_outbox(priority);
