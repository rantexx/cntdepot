-- 006 edi first try: when the sender first tried a message. A message still
-- failing a day after its first try is given up as "failed" (spec 5.4); a
-- resend starts a new day. created_at cannot serve: it orders the queue.
ALTER TABLE edi_messages ADD COLUMN first_try_at TIMESTAMPTZ;

-- One event can send a line both a cancel and an original (a corrected
-- container number, spec 5.8.3): one row per event, line and function.
DROP INDEX edi_messages_one_per_event;
CREATE UNIQUE INDEX edi_messages_one_per_event ON edi_messages (company_id, event_id, line_id, function)
    WHERE event_id IS NOT NULL;
