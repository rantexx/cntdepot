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

-- name: InsertMessage :exec
INSERT INTO edi_messages (company_id, id, direction, kind, status, line_id, depot_id, answer_of, function,
    control_ref, message_ref, content_hash, blob_digest, file_name, sender, problems, next_attempt_at)
VALUES (@company_id, @id, @direction, @kind, @status, @line_id, @depot_id, @answer_of, @function,
    @control_ref, @message_ref, @content_hash, @blob_digest, @file_name, @sender, @problems, @next_attempt_at);

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
SELECT * FROM line_edi WHERE company_id = @company_id ORDER BY line_id;

-- name: DepotLocations :many
SELECT id, name, edi_location FROM depots WHERE company_id = @company_id ORDER BY id;

-- An APERAK from a line answers our messages to it by message or interchange
-- reference, or by the CODECO document number (the move id).
-- name: MarkAnswered :many
UPDATE edi_messages SET status = @status, problems = @problems
WHERE company_id = @company_id AND direction = 'out' AND line_id = @line_id
  AND (message_ref = ANY(@refs::text[]) OR control_ref = ANY(@refs::text[]) OR move_id = ANY(@refs::text[]))
RETURNING id;
