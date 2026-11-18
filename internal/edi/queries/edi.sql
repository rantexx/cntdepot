-- name: GetCompanyEDI :one
SELECT * FROM company_edi WHERE company_id = @company_id;

-- name: InsertCompanyEDI :execrows
INSERT INTO company_edi (company_id, inbound_token) VALUES (@company_id, @inbound_token)
ON CONFLICT (company_id) DO NOTHING;

-- name: TakeNextRef :one
UPDATE company_edi SET next_ref = next_ref + 1 WHERE company_id = @company_id
RETURNING (next_ref - 1)::bigint AS ref;

-- name: InboundCompany :one
SELECT COALESCE(edi_inbound_company(@token::text), '')::text AS company_id;

-- name: EDICompanies :many
SELECT edi_companies()::text AS company_id;

-- A CODECO already queued for its event and line inserts nothing.
-- name: InsertMessage :execrows
INSERT INTO edi_messages (company_id, id, direction, kind, status, line_id, depot_id, move_id, event_id, answer_of, function,
    control_ref, message_ref, content_hash, blob_digest, file_name, sender, via, trusted, problems, next_attempt_at)
VALUES (@company_id, @id, @direction, @kind, @status, @line_id, @depot_id, @move_id, @event_id, @answer_of, @function,
    @control_ref, @message_ref, @content_hash, @blob_digest, @file_name, @sender, @via, @trusted, @problems, @next_attempt_at)
ON CONFLICT DO NOTHING;

-- name: GetMessage :one
SELECT * FROM edi_messages WHERE company_id = @company_id AND id = @id;

-- name: SetInResult :execrows
UPDATE edi_messages SET kind = @kind, status = @status, line_id = @line_id, depot_id = @depot_id,
    order_id = @order_id, function = @function, control_ref = @control_ref, message_ref = @message_ref,
    problems = @problems
WHERE company_id = @company_id AND id = @id;

-- Earlier files of a line with the same control reference: with the same
-- content (a repeat) and with other content.
-- name: RepeatsOf :one
SELECT count(*) FILTER (WHERE content_hash = @content_hash) AS same,
       count(*) FILTER (WHERE content_hash <> @content_hash) AS other
FROM edi_messages
WHERE company_id = @company_id AND direction = 'in' AND line_id = @line_id AND control_ref = @control_ref
  AND id <> @id AND status <> 'ignored';

-- name: ListLineEDI :many
SELECT l.line_id, l.mode, l.profile, s.code
FROM line_edi l JOIN shipping_lines s ON s.company_id = l.company_id AND s.id = l.line_id
WHERE l.company_id = @company_id ORDER BY l.line_id;

-- name: DepotLocations :many
SELECT id, name, edi_location FROM depots WHERE company_id = @company_id ORDER BY id;

-- An APERAK from a line answers our messages to it by message or interchange
-- reference, or by the CODECO document number (the move id). Only messages
-- already sent: a later CODECO of the same move still queued is not answered.
-- name: MarkAnswered :many
UPDATE edi_messages SET status = @status, problems = @problems
WHERE company_id = @company_id AND direction = 'out' AND line_id = @line_id
  AND kind IN ('codeco', 'test') AND status IN ('sent', 'accepted', 'rejected')
  AND (message_ref = ANY(@refs::text[]) OR control_ref = ANY(@refs::text[]) OR move_id = ANY(@refs::text[]))
RETURNING id;

-- The gate events that queue CODECO (spec 5.4), in the order they happened.
-- name: GateEvents :many
SELECT id, stream, kind FROM events
WHERE company_id = @company_id AND kind IN ('move.gate_saved', 'move.corrected', 'move.voided') AND occurred_at >= @since
ORDER BY seq;

-- name: SetEventsScanned :exec
UPDATE company_edi SET events_scanned_at = @at WHERE company_id = @company_id;

-- name: EventQueued :one
SELECT EXISTS (SELECT 1 FROM edi_messages WHERE company_id = @company_id AND event_id = @event_id)::bool AS queued;

-- The move's latest CODECO. A row in error was never built, so it does not count.
-- name: LastCodeco :one
SELECT line_id, function, blob_digest FROM edi_messages
WHERE company_id = @company_id AND move_id = @move_id AND direction = 'out' AND kind = 'codeco' AND status <> 'error'
ORDER BY created_at DESC, id DESC LIMIT 1;

-- A move as its CODECO reports it, with its line (the visit's), haulier,
-- order and depot. The gate time is the form's first save, as the EIR shows it;
-- an offline move carries the time the server received it.
-- name: CodecoMove :one
SELECT m.id, m.depot_id, m.direction, m.number, m.type_code, m.full_empty, m.seal, m.truck_plate, m.condition,
    COALESCE(m.form_saved_at, m.created_at)::timestamptz AS gate_at, m.is_test, v.line_id, COALESCE(h.code, '')::text AS haulier_code,
    COALESCE(o.reference, '')::text AS order_ref, d.timezone, d.edi_location
FROM gate_moves m
JOIN depots d ON d.company_id = m.company_id AND d.id = m.depot_id
LEFT JOIN visits v ON v.company_id = m.company_id AND v.id = m.visit_id
LEFT JOIN hauliers h ON h.company_id = m.company_id AND h.id = m.haulier_id
LEFT JOIN release_orders o ON o.company_id = m.company_id AND o.id = m.order_id
WHERE m.company_id = @company_id AND m.id = @id;

-- name: MoveDamages :many
SELECT location, description FROM damages WHERE company_id = @company_id AND move_id = @move_id ORDER BY id;

-- The oldest due message to send, never ahead of an earlier unsent message
-- of the same move (spec 5.4: the order per container is kept). A report is
-- sent like the rest, to the line's ReportTo. ref is the subject's container number or reference.
-- name: ClaimDue :one
SELECT m.id, m.kind, m.line_id, m.blob_digest, m.file_name, m.tries, m.first_try_at, m.report_day,
    COALESCE(d.name, '')::text AS depot_name,
    COALESCE(NULLIF(g.number, ''), o.reference, m.control_ref)::text AS ref
FROM edi_messages m
LEFT JOIN depots d ON d.company_id = m.company_id AND d.id = m.depot_id
LEFT JOIN gate_moves g ON g.company_id = m.company_id AND g.id = m.move_id
LEFT JOIN edi_messages a ON a.company_id = m.company_id AND a.id = m.answer_of
LEFT JOIN release_orders o ON o.company_id = m.company_id AND o.id = COALESCE(m.order_id, a.order_id)
WHERE m.company_id = @company_id AND m.direction = 'out' AND m.status = 'pending'
  AND m.next_attempt_at <= @now
  AND NOT EXISTS (SELECT 1 FROM edi_messages e WHERE e.company_id = m.company_id AND e.move_id = m.move_id
      AND e.direction = 'out' AND e.status IN ('pending', 'failed') AND (e.created_at, e.id) < (m.created_at, m.id))
ORDER BY m.created_at, m.id
LIMIT 1
FOR UPDATE OF m SKIP LOCKED;

-- name: SetSent :exec
UPDATE edi_messages SET status = 'sent', sent_at = @sent_at, next_attempt_at = NULL, problems = '[]'
WHERE company_id = @company_id AND id = @id;

-- name: SetTried :exec
UPDATE edi_messages SET status = @status, tries = tries + 1, first_try_at = @first_try_at,
    next_attempt_at = @next_attempt_at, problems = @problems
WHERE company_id = @company_id AND id = @id;

-- name: Resend :execrows
UPDATE edi_messages SET status = 'pending', tries = 0, first_try_at = NULL, next_attempt_at = @now
WHERE company_id = @company_id AND id = @id AND direction = 'out' AND blob_digest <> '';

-- Inbound files stored but never processed (the process failed or stopped).
-- name: StuckInbound :many
SELECT id FROM edi_messages
WHERE company_id = @company_id AND direction = 'in' AND status = 'received' AND created_at < @before
ORDER BY created_at, id;

-- name: FailStuck :exec
UPDATE edi_messages SET status = 'error', problems = @problems
WHERE company_id = @company_id AND id = @id AND direction = 'in' AND status = 'received';

-- name: DepotZones :many
SELECT id, name, timezone FROM depots WHERE company_id = @company_id ORDER BY id;

-- name: ReportDone :one
SELECT EXISTS (SELECT 1 FROM edi_messages WHERE company_id = @company_id AND kind = 'report'
    AND line_id = @line_id AND depot_id = @depot_id AND report_day = @report_day)::bool AS done;

-- A daily report row: a second instance or a re-run inserts nothing.
-- name: InsertReport :execrows
INSERT INTO edi_messages (company_id, id, direction, kind, status, line_id, depot_id, report_day, blob_digest, file_name, content_hash, problems, next_attempt_at)
VALUES (@company_id, @id, 'out', 'report', @status, @line_id, @depot_id, @report_day, @blob_digest, @file_name, @blob_digest, '[]', @next_attempt_at)
ON CONFLICT DO NOTHING;

