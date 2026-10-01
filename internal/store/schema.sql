-- Schema v1 of the local gate hub: a subset of the spec data model
CREATE TABLE gate_moves (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL,
    depot_id          TEXT NOT NULL,
    lane              TEXT NOT NULL DEFAULT '',
    direction         TEXT NOT NULL DEFAULT 'in',
    status            TEXT NOT NULL,
    number            TEXT NOT NULL DEFAULT '',
    type_code         TEXT NOT NULL DEFAULT '',
    color             TEXT NOT NULL DEFAULT '',
    confidence        REAL NOT NULL DEFAULT 0,
    check_digit_valid INTEGER NOT NULL DEFAULT 0,
    decision          TEXT NOT NULL DEFAULT '',
    reason            TEXT NOT NULL DEFAULT '',
    result_json       TEXT NOT NULL DEFAULT '{}',
    verified          INTEGER NOT NULL DEFAULT 0,
    corrected_from    TEXT NOT NULL DEFAULT '',
    resolved_by       TEXT NOT NULL DEFAULT '',
    note              TEXT NOT NULL DEFAULT '',
    is_test           INTEGER NOT NULL DEFAULT 0,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);
CREATE INDEX gate_moves_status ON gate_moves (status, id);
CREATE TABLE photos (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    move_id       TEXT NOT NULL REFERENCES gate_moves (id),
    role          TEXT NOT NULL,
    sha256        TEXT NOT NULL,
    ai_sha256     TEXT NOT NULL,
    media_type    TEXT NOT NULL,
    ai_media_type TEXT NOT NULL,
    bytes         INTEGER NOT NULL,
    width         INTEGER NOT NULL DEFAULT 0,
    height        INTEGER NOT NULL DEFAULT 0,
    captured_at   TEXT NOT NULL
);
CREATE INDEX photos_move ON photos (move_id, id);
CREATE TABLE recognition_attempts (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL,
    move_id        TEXT NOT NULL REFERENCES gate_moves (id),
    photo_id       TEXT NOT NULL REFERENCES photos (id),
    engine         TEXT NOT NULL,
    model          TEXT NOT NULL,
    prompt_version TEXT NOT NULL,
    reading_json   TEXT NOT NULL,
    raw            TEXT NOT NULL,
    error          TEXT NOT NULL DEFAULT '',
    input_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens  INTEGER NOT NULL DEFAULT 0,
    latency_ms     INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL
);
CREATE INDEX attempts_move ON recognition_attempts (move_id, id);
CREATE INDEX attempts_photo ON recognition_attempts (photo_id, id);
CREATE TABLE events (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    id          TEXT NOT NULL UNIQUE,
    tenant_id   TEXT NOT NULL,
    stream      TEXT NOT NULL,
    kind        TEXT NOT NULL,
    actor       TEXT NOT NULL,
    payload     TEXT NOT NULL,
    occurred_at TEXT NOT NULL
);
CREATE INDEX events_stream ON events (stream, seq)
