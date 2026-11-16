-- name: InsertAttempt :exec
INSERT INTO recognition_attempts (company_id, id, move_id, photo_id, engine, model, prompt_version,
    reading_json, raw, error, input_tokens, output_tokens, latency_ms, created_at)
VALUES (@company_id, @id, @move_id, @photo_id, @engine, @model, @prompt_version,
    @reading_json, @raw, @error, @input_tokens, @output_tokens, @latency_ms, @created_at);

-- name: ListAttempts :many
SELECT * FROM recognition_attempts a WHERE a.company_id = @company_id AND a.move_id = @move_id
  AND EXISTS (SELECT 1 FROM gate_moves m WHERE m.company_id = a.company_id AND m.id = a.move_id
              AND (@depot_id::text = '' OR m.depot_id = @depot_id))
ORDER BY a.id;

-- name: InsertEvent :exec
INSERT INTO events (id, company_id, stream, kind, actor, payload, occurred_at)
VALUES (@id, @company_id, @stream, @kind, @actor, @payload, @occurred_at);

-- name: ListEvents :many
SELECT * FROM events WHERE company_id = @company_id AND stream = @stream ORDER BY seq;

-- name: ListSettings :many
SELECT key, value FROM settings WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id) ORDER BY depot_id, key;

-- name: UpsertSetting :exec
INSERT INTO settings (company_id, depot_id, key, value, updated_at)
VALUES (@company_id, @depot_id, @key, @value, @updated_at)
ON CONFLICT (company_id, depot_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at;

-- name: AddUsage :exec
INSERT INTO usage_counters (company_id, kind, day, count) VALUES (@company_id, @kind, @day, @n)
ON CONFLICT (company_id, kind, day) DO UPDATE SET count = usage_counters.count + EXCLUDED.count;

-- name: CompanyIDs :many
SELECT id FROM companies ORDER BY id;

-- name: CompanyModules :one
SELECT modules FROM companies WHERE id = @id;
