-- Schema v2: fixed gate cameras (spec 2026-10-02-gate-camera-design.md)
ALTER TABLE gate_moves ADD COLUMN source TEXT NOT NULL DEFAULT 'phone';
CREATE INDEX gate_moves_source ON gate_moves (source, id);
ALTER TABLE photos ADD COLUMN deferred INTEGER NOT NULL DEFAULT 0;
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
