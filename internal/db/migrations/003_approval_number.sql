-- 003: an approval names the container the clerk asked to pass. A refused
-- move is not applied, so its own number may still be empty.

ALTER TABLE approvals ADD COLUMN number TEXT NOT NULL DEFAULT '';
