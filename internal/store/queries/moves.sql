-- name: InsertMove :exec
INSERT INTO gate_moves (company_id, id, depot_id, lane, direction, source, status, is_test, created_at, updated_at)
VALUES (@company_id, @id, @depot_id, @lane, @direction, @source, @status, @is_test, @created_at, @created_at);

-- name: GetMove :one
SELECT * FROM gate_moves WHERE company_id = @company_id AND depot_id = @depot_id AND id = @id;

-- name: ListMoves :many
SELECT * FROM gate_moves
WHERE company_id = @company_id AND depot_id = @depot_id
  AND (@status::text = '' OR status = @status)
  AND (@source::text = '' OR source = @source)
  AND (@before::text = '' OR id < @before)
ORDER BY id DESC
LIMIT @row_limit;

-- name: VerifiedMoves :many
SELECT * FROM gate_moves
WHERE company_id = @company_id AND depot_id = @depot_id
  AND status = 'accepted' AND verified AND NOT is_test
ORDER BY id;

-- name: SetStatus :exec
UPDATE gate_moves SET status = @status, updated_at = @updated_at
WHERE company_id = @company_id AND depot_id = @depot_id AND id = @id AND resolved_by = '';

-- name: SetResult :execrows
UPDATE gate_moves SET status = @status, number = @number, type_code = @type_code, color = @color,
    confidence = @confidence, check_digit_valid = @check_digit_valid, decision = @decision,
    reason = @reason, result_json = @result_json, updated_at = @updated_at
WHERE company_id = @company_id AND depot_id = @depot_id AND id = @id AND resolved_by = '';

-- name: ResolveMove :execrows
UPDATE gate_moves SET status = @status, number = @number, verified = @verified,
    corrected_from = @corrected_from, resolved_by = @resolved_by, note = @note, updated_at = @updated_at
WHERE company_id = @company_id AND depot_id = @depot_id AND id = @id;
