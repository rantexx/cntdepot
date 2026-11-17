-- 004: the office says what it did about a gate flag when it resolves it.

ALTER TABLE gate_flags ADD COLUMN resolve_note TEXT NOT NULL DEFAULT '';
