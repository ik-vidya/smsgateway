-- 0001_init.sql — начальная схема smsgateway.
--
-- Таблицы:
--   messages            — все SMS (входящие и исходящие)
--   delivery_reports    — история delivery reports (аудит + защита от гонок)
--   pending_references  — защита от коллизий TP-MR (8-битный reference)
--   pending_parts       — сборка multipart-сообщений
--   pending_part_bodies — тела частей multipart
--   imported_files      — дедупликация Gammu-импорта

-- ==================== messages ====================
CREATE TABLE messages (
    id              INTEGER PRIMARY KEY,
    direction       TEXT NOT NULL,       -- 'in' | 'out'
    status          TEXT NOT NULL,       -- queued|sending|sent|delivered|failed|sent_no_dr|received
    peer            TEXT NOT NULL,       -- номер другой стороны
    text            TEXT,
    raw_pdu         TEXT,                -- hex PDU

    -- Исходящие
    reference       INTEGER,             -- TP-MR из +CMGS: <ref>
    tp_mr           INTEGER,             -- TP-MR, который мы положили в PDU
    retry_count     INTEGER NOT NULL DEFAULT 0,
    error           TEXT,

    -- Жизненный цикл
    queued_at       TIMESTAMP,
    sent_at         TIMESTAMP,
    delivered_at    TIMESTAMP,

    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_messages_status      ON messages(status);
CREATE INDEX idx_messages_direction   ON messages(direction);
CREATE INDEX idx_messages_peer        ON messages(peer);
CREATE INDEX idx_messages_reference   ON messages(reference);
CREATE INDEX idx_messages_created_at  ON messages(created_at);
CREATE INDEX idx_messages_sent_at     ON messages(sent_at);

-- ==================== delivery_reports ====================
CREATE TABLE delivery_reports (
    id              INTEGER PRIMARY KEY,
    message_id      INTEGER REFERENCES messages(id) ON DELETE SET NULL,
    reference       INTEGER NOT NULL,
    tp_st           INTEGER NOT NULL,     -- сырой TP-ST код
    status_text     TEXT NOT NULL,        -- "доставлено" | "не доставлено (...)"
    peer            TEXT,
    raw_pdu         TEXT NOT NULL,
    received_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_dr_message_id ON delivery_reports(message_id);
CREATE INDEX idx_dr_reference  ON delivery_reports(reference);
CREATE INDEX idx_dr_received   ON delivery_reports(received_at);

-- ==================== pending_references ====================
-- Защита от коллизий 8-битного TP-MR: не даёт отправить новое
-- сообщение с reference, который уже занят неподтверждённым DR.
CREATE TABLE pending_references (
    reference       INTEGER PRIMARY KEY,
    message_id      INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at      TIMESTAMP NOT NULL
);

CREATE INDEX idx_pending_ref_expires ON pending_references(expires_at);

-- ==================== pending_parts ====================
-- Незавершённые multipart-сборки входящих SMS.
CREATE TABLE pending_parts (
    id              INTEGER PRIMARY KEY,
    reference       INTEGER NOT NULL,
    peer            TEXT NOT NULL,
    total           INTEGER NOT NULL,
    received        INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(reference, peer)
);

CREATE INDEX idx_pending_parts_created ON pending_parts(created_at);

CREATE TABLE pending_part_bodies (
    pending_id      INTEGER NOT NULL REFERENCES pending_parts(id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    text            TEXT NOT NULL,
    PRIMARY KEY (pending_id, seq)
);

-- ==================== imported_files ====================
-- Дедупликация импорта Gammu file backend.
CREATE TABLE imported_files (
    path            TEXT PRIMARY KEY,
    mtime           TIMESTAMP NOT NULL,
    imported_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
