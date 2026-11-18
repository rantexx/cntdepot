-- 006 edi first try: when the sender first tried a message. A message still
-- failing a day after its first try is given up as "failed" (spec 5.4); a
-- resend starts a new day. created_at cannot serve: it orders the queue.
ALTER TABLE edi_messages ADD COLUMN first_try_at TIMESTAMPTZ;
