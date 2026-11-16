-- name: InsertPhoto :exec
INSERT INTO photos (company_id, id, move_id, role, sha256, ai_sha256, media_type, ai_media_type,
    bytes, width, height, captured_at, deferred, read_enabled)
VALUES (@company_id, @id, @move_id, @role, @sha256, @ai_sha256, @media_type, @ai_media_type,
    @bytes, @width, @height, @captured_at, @deferred, @read_enabled);

-- name: ListPhotos :many
SELECT * FROM photos WHERE company_id = @company_id AND move_id = @move_id ORDER BY id;

-- name: ListPhotosOfMoves :many
SELECT * FROM photos WHERE company_id = @company_id AND move_id = ANY(@move_ids::text[]) ORDER BY id;

-- name: GetPhoto :one
SELECT * FROM photos WHERE company_id = @company_id AND id = @id;

-- name: ChoosePhoto :execrows
UPDATE photos SET deferred = false WHERE company_id = @company_id AND id = @id AND deferred;

-- name: PhotosWithoutAttempts :many
SELECT p.* FROM photos p
WHERE p.company_id = @company_id AND p.read_enabled AND NOT p.deferred
  AND NOT EXISTS (SELECT 1 FROM recognition_attempts a WHERE a.company_id = p.company_id AND a.photo_id = p.id)
  AND EXISTS (SELECT 1 FROM gate_moves m
              WHERE m.company_id = p.company_id AND m.id = p.move_id AND m.depot_id = @depot_id AND m.resolved_by = '')
ORDER BY p.id;

-- name: PhotoByDigest :one
SELECT * FROM photos WHERE company_id = @company_id AND (sha256 = @digest OR ai_sha256 = @digest)
ORDER BY id LIMIT 1;
