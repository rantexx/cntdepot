-- name: InsertPhoto :exec
INSERT INTO photos (company_id, id, move_id, role, sha256, ai_sha256, media_type, ai_media_type,
    bytes, width, height, captured_at, deferred, read_enabled)
VALUES (@company_id, @id, @move_id, @role, @sha256, @ai_sha256, @media_type, @ai_media_type,
    @bytes, @width, @height, @captured_at, @deferred, @read_enabled);

-- An empty depot_id means every depot of the company.

-- name: ListPhotos :many
SELECT sqlc.embed(p), m.depot_id FROM photos p
JOIN gate_moves m ON m.company_id = p.company_id AND m.id = p.move_id
WHERE p.company_id = @company_id AND (@depot_id::text = '' OR m.depot_id = @depot_id) AND p.move_id = @move_id
ORDER BY p.id;

-- name: ListPhotosOfMoves :many
SELECT sqlc.embed(p), m.depot_id FROM photos p
JOIN gate_moves m ON m.company_id = p.company_id AND m.id = p.move_id
WHERE p.company_id = @company_id AND p.move_id = ANY(@move_ids::text[])
ORDER BY p.id;

-- name: GetPhoto :one
SELECT sqlc.embed(p), m.depot_id FROM photos p
JOIN gate_moves m ON m.company_id = p.company_id AND m.id = p.move_id
WHERE p.company_id = @company_id AND (@depot_id::text = '' OR m.depot_id = @depot_id) AND p.id = @id;

-- name: ChoosePhoto :execrows
UPDATE photos SET deferred = false WHERE company_id = @company_id AND id = @id AND deferred;

-- name: PhotosWithoutAttempts :many
SELECT sqlc.embed(p), m.depot_id FROM photos p
JOIN gate_moves m ON m.company_id = p.company_id AND m.id = p.move_id
WHERE p.company_id = @company_id AND (@depot_id::text = '' OR m.depot_id = @depot_id)
  AND p.read_enabled AND NOT p.deferred AND m.resolved_by = ''
  AND NOT EXISTS (SELECT 1 FROM recognition_attempts a WHERE a.company_id = p.company_id AND a.photo_id = p.id)
ORDER BY p.id;

-- name: PhotoByDigest :one
SELECT sqlc.embed(p), m.depot_id FROM photos p
JOIN gate_moves m ON m.company_id = p.company_id AND m.id = p.move_id
WHERE p.company_id = @company_id AND (@depot_id::text = '' OR m.depot_id = @depot_id)
  AND (p.sha256 = @digest OR p.ai_sha256 = @digest)
ORDER BY p.id LIMIT 1;
