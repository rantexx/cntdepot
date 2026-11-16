-- An empty depot_id means every depot of the company. Rows without a depot
-- (holds, damages, flags, approvals, tokens) take it from their visit or move.

-- name: InsertOrder :exec
INSERT INTO release_orders (company_id, id, depot_id, kind, reference, line_id, customer_id, valid_until, note, created_by)
VALUES (@company_id, @id, @depot_id, @kind, @reference, @line_id, @customer_id, @valid_until, @note, @created_by);

-- name: InsertOrderItem :exec
INSERT INTO order_items (company_id, id, order_id, number, type_code, full_empty, quantity, used)
VALUES (@company_id, @id, @order_id, @number, @type_code, @full_empty, @quantity, @used);

-- name: GetOrder :one
SELECT * FROM release_orders
WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id) AND id = @id;

-- name: ListOrderItems :many
SELECT * FROM order_items WHERE company_id = @company_id AND order_id = @order_id ORDER BY id;

-- name: ListOrders :many
SELECT * FROM release_orders
WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id)
  AND (@kind::text = '' OR kind = @kind) AND (@status::text = '' OR status = @status)
ORDER BY id DESC;

-- name: SetOrderStatus :execrows
UPDATE release_orders SET status = @status
WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id) AND id = @id;

-- name: UseItem :execrows
UPDATE order_items i SET used = i.used + @n::int
WHERE i.company_id = @company_id AND i.id = @id
  AND EXISTS (SELECT 1 FROM release_orders o WHERE o.company_id = i.company_id AND o.id = i.order_id
              AND (@depot_id::text = '' OR o.depot_id = @depot_id));

-- name: InsertVisit :exec
INSERT INTO visits (company_id, id, depot_id, number, type_code, line_id, customer_id, status, full_empty,
    grade, block, row_no, tier, in_move_id, out_move_id, in_at, out_at, imported, close_reason)
VALUES (@company_id, @id, @depot_id, @number, @type_code, @line_id, @customer_id, @status, @full_empty,
    @grade, @block, @row_no, @tier, @in_move_id, @out_move_id, @in_at, @out_at, @imported, @close_reason);

-- name: GetVisit :one
SELECT * FROM visits
WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id) AND id = @id;

-- name: InYardVisit :one
SELECT * FROM visits
WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id)
  AND number = @number AND status = 'in_yard';

-- name: ListVisits :many
SELECT * FROM visits
WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id)
  AND (@status::text = '' OR status = @status)
ORDER BY id DESC;

-- Only an open visit closes: a replay must not overwrite a closed one.
-- name: CloseVisit :execrows
UPDATE visits SET status = 'out', out_move_id = @out_move_id, out_at = @out_at, close_reason = @close_reason
WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id) AND id = @id AND status = 'in_yard';

-- name: SetVisitPosition :execrows
UPDATE visits SET block = @block, row_no = @row_no, tier = @tier
WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id) AND id = @id;

-- name: SetVisitGrade :execrows
UPDATE visits SET grade = @grade
WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id) AND id = @id;

-- name: InsertHold :execrows
INSERT INTO holds (company_id, id, visit_id, reason, placed_by, placed_at)
SELECT @company_id, @id, v.id, @reason, @placed_by, @placed_at FROM visits v
WHERE v.company_id = @company_id AND (@depot_id::text = '' OR v.depot_id = @depot_id) AND v.id = @visit_id;

-- name: ReleaseHold :execrows
UPDATE holds h SET released_by = @released_by, released_at = @released_at
WHERE h.company_id = @company_id AND h.id = @id AND h.released_at IS NULL
  AND EXISTS (SELECT 1 FROM visits v WHERE v.company_id = h.company_id AND v.id = h.visit_id
              AND (@depot_id::text = '' OR v.depot_id = @depot_id));

-- name: ListHolds :many
SELECT h.* FROM holds h JOIN visits v ON v.company_id = h.company_id AND v.id = h.visit_id
WHERE h.company_id = @company_id AND (@depot_id::text = '' OR v.depot_id = @depot_id)
  AND (@visit_id::text = '' OR h.visit_id = @visit_id)
  AND (NOT @open_only::boolean OR h.released_at IS NULL)
ORDER BY h.id;

-- name: UpsertBlock :one
INSERT INTO block_list (company_id, id, kind, value, reason, active, created_by, created_at)
VALUES (@company_id, @id, @kind, @value, @reason, true, @created_by, @created_at)
ON CONFLICT (company_id, kind, value) DO UPDATE SET reason = EXCLUDED.reason, active = true
RETURNING id;

-- name: SetBlockActive :execrows
UPDATE block_list SET active = @active WHERE company_id = @company_id AND id = @id;

-- name: ListBlocks :many
SELECT * FROM block_list
WHERE company_id = @company_id AND (NOT @active_only::boolean OR active)
ORDER BY kind, value;

-- name: InsertDamage :execrows
INSERT INTO damages (company_id, id, move_id, location, description, photo_ids)
SELECT @company_id, @id, m.id, @location, @description, @photo_ids::text[] FROM gate_moves m
WHERE m.company_id = @company_id AND (@depot_id::text = '' OR m.depot_id = @depot_id) AND m.id = @move_id;

-- name: ListDamages :many
SELECT d.* FROM damages d JOIN gate_moves m ON m.company_id = d.company_id AND m.id = d.move_id
WHERE d.company_id = @company_id AND (@depot_id::text = '' OR m.depot_id = @depot_id) AND d.move_id = @move_id
ORDER BY d.id;

-- name: InsertFlag :execrows
INSERT INTO gate_flags (company_id, id, move_id, code, detail)
SELECT @company_id, @id, m.id, @code, @detail FROM gate_moves m
WHERE m.company_id = @company_id AND (@depot_id::text = '' OR m.depot_id = @depot_id) AND m.id = @move_id;

-- name: ListFlags :many
SELECT f.* FROM gate_flags f JOIN gate_moves m ON m.company_id = f.company_id AND m.id = f.move_id
WHERE f.company_id = @company_id AND (@depot_id::text = '' OR m.depot_id = @depot_id)
  AND (@move_id::text = '' OR f.move_id = @move_id)
  AND (NOT @open_only::boolean OR f.resolved_at IS NULL)
ORDER BY f.id;

-- name: ResolveFlag :execrows
UPDATE gate_flags f SET resolved_by = @resolved_by, resolved_at = @resolved_at
WHERE f.company_id = @company_id AND f.id = @id AND f.resolved_at IS NULL
  AND EXISTS (SELECT 1 FROM gate_moves m WHERE m.company_id = f.company_id AND m.id = f.move_id
              AND (@depot_id::text = '' OR m.depot_id = @depot_id));

-- name: InsertApproval :execrows
INSERT INTO approvals (company_id, id, move_id, reasons, requested_by, requested_at)
SELECT @company_id, @id, m.id, @reasons, @requested_by, @requested_at FROM gate_moves m
WHERE m.company_id = @company_id AND (@depot_id::text = '' OR m.depot_id = @depot_id) AND m.id = @move_id;

-- name: GetApproval :one
SELECT a.* FROM approvals a JOIN gate_moves m ON m.company_id = a.company_id AND m.id = a.move_id
WHERE a.company_id = @company_id AND (@depot_id::text = '' OR m.depot_id = @depot_id) AND a.id = @id;

-- name: ListApprovals :many
SELECT a.* FROM approvals a JOIN gate_moves m ON m.company_id = a.company_id AND m.id = a.move_id
WHERE a.company_id = @company_id AND (@depot_id::text = '' OR m.depot_id = @depot_id)
  AND (@status::text = '' OR a.status = @status)
ORDER BY a.id;

-- name: DecideApproval :execrows
UPDATE approvals a SET status = @status, decided_by = @decided_by, decided_at = @decided_at
WHERE a.company_id = @company_id AND a.id = @id AND a.status = 'pending'
  AND EXISTS (SELECT 1 FROM gate_moves m WHERE m.company_id = a.company_id AND m.id = a.move_id
              AND (@depot_id::text = '' OR m.depot_id = @depot_id));

-- name: InsertEIRToken :execrows
INSERT INTO eir_tokens (company_id, id, move_id, token_hash, expires_at, created_by)
SELECT @company_id, @id, m.id, @token_hash, @expires_at, @created_by FROM gate_moves m
WHERE m.company_id = @company_id AND (@depot_id::text = '' OR m.depot_id = @depot_id) AND m.id = @move_id;

-- name: EIRLookup :one
SELECT company_id::text, move_id::text FROM eir_lookup(@token_hash);

-- name: GetSettings :one
SELECT * FROM company_settings WHERE company_id = @company_id;

-- name: UpsertSettings :exec
INSERT INTO company_settings (company_id, refusal_mode, walkins_allowed, eir_ttl_days)
VALUES (@company_id, @refusal_mode, @walkins_allowed, @eir_ttl_days)
ON CONFLICT (company_id) DO UPDATE SET refusal_mode = EXCLUDED.refusal_mode,
    walkins_allowed = EXCLUDED.walkins_allowed, eir_ttl_days = EXCLUDED.eir_ttl_days;

-- name: UpsertLinePrefix :exec
INSERT INTO line_prefixes (company_id, prefix, line_id) VALUES (@company_id, @prefix, @line_id)
ON CONFLICT (company_id, prefix) DO UPDATE SET line_id = EXCLUDED.line_id;

-- name: ListLinePrefixes :many
SELECT * FROM line_prefixes WHERE company_id = @company_id ORDER BY prefix;

-- name: SetMoveForm :execrows
UPDATE gate_moves SET visit_id = @visit_id, order_id = @order_id, order_item_id = @order_item_id,
    truck_plate = @truck_plate, trailer_plate = @trailer_plate, driver_name = @driver_name,
    haulier_id = @haulier_id, seal = @seal, full_empty = @full_empty, condition = @condition,
    signature_photo_id = @signature_photo_id, form_saved_at = @form_saved_at, updated_at = @form_saved_at
WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id) AND id = @id;

-- name: GetMoveForm :one
SELECT id, visit_id, order_id, order_item_id, truck_plate, trailer_plate, driver_name, haulier_id, seal,
    full_empty, condition, signature_photo_id, form_saved_at, voided, void_reason
FROM gate_moves WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id) AND id = @id;

-- name: VoidMove :execrows
UPDATE gate_moves SET voided = true, void_reason = @void_reason, updated_at = @updated_at
WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id) AND id = @id;


-- The gate save (yard.Service). Every query is confined to the company; the
-- move itself is read under the scope's depot.

-- name: LockMove :one
SELECT id, depot_id, direction, number, visit_id, order_id, order_item_id, form_saved_at, voided
FROM gate_moves WHERE company_id = @company_id AND (@depot_id::text = '' OR depot_id = @depot_id) AND id = @id
FOR UPDATE;

-- Serialises saves of one container number in a company (one open visit per number).
-- name: LockNumber :exec
SELECT pg_advisory_xact_lock(hashtextextended(@company_id::text || '/' || @number::text, 0));

-- name: DepotTimezone :one
SELECT timezone FROM depots WHERE company_id = @company_id AND id = @id;

-- name: MovePhotoIDs :many
SELECT id FROM photos WHERE company_id = @company_id AND move_id = @move_id;

-- The items of a depot's open orders of one kind that can match a container:
-- named for its number, or a quantity of the same size, height and group.
-- Locked, so two saves cannot both use the last unit.
-- name: MatchOpenOrderItems :many
SELECT o.kind, o.line_id, o.customer_id, o.status, o.valid_until, sqlc.embed(i)
FROM order_items i JOIN release_orders o ON o.company_id = i.company_id AND o.id = i.order_id
WHERE i.company_id = @company_id AND o.depot_id = @depot_id AND o.kind = @kind AND o.status = 'open'
  AND (i.number = @number::text OR left(i.type_code, 3) = left(@type_code::text, 3))
ORDER BY i.id
FOR UPDATE OF i;

-- The same from cancelled or done orders, only to tell the clerk the order
-- is no longer open: not locked, the 50 most recent.
-- name: MatchOtherOrderItems :many
SELECT o.kind, o.line_id, o.customer_id, o.status, o.valid_until, sqlc.embed(i)
FROM order_items i JOIN release_orders o ON o.company_id = i.company_id AND o.id = i.order_id
WHERE i.company_id = @company_id AND o.depot_id = @depot_id AND o.kind = @kind AND o.status <> 'open'
  AND (i.number = @number::text OR left(i.type_code, 3) = left(@type_code::text, 3))
ORDER BY o.created_at DESC, i.id
LIMIT 50;

-- Whether the form's optional references exist in the company (an empty id: none given).
-- name: FormRefs :one
SELECT (@line_id::text = '' OR EXISTS (SELECT 1 FROM shipping_lines l WHERE l.company_id = @company_id::text AND l.id = @line_id::text))::boolean AS line_ok,
    (@customer_id::text = '' OR EXISTS (SELECT 1 FROM customers cu WHERE cu.company_id = @company_id::text AND cu.id = @customer_id::text))::boolean AS customer_ok,
    (@haulier_id::text = '' OR EXISTS (SELECT 1 FROM hauliers h WHERE h.company_id = @company_id::text AND h.id = @haulier_id::text))::boolean AS haulier_ok;

-- name: MatchBlock :one
SELECT * FROM block_list b
WHERE b.company_id = @company_id AND b.active
  AND ((b.kind = 'container' AND b.value = @number)
    OR (b.kind = 'line' AND b.value = (SELECT upper(trim(l.code)) FROM shipping_lines l
                                      WHERE l.company_id = @company_id AND l.id = @line_id::text)))
ORDER BY b.kind, b.id
LIMIT 1;

-- name: ApprovalsOfMove :many
SELECT * FROM approvals WHERE company_id = @company_id AND move_id = @move_id ORDER BY id;

-- name: SetMoveIdentity :execrows
UPDATE gate_moves SET direction = @direction, number = @number, type_code = @type_code,
    corrected_from = CASE WHEN number <> '' AND number <> @number THEN number ELSE corrected_from END,
    status = 'accepted', verified = @verified,
    resolved_by = CASE WHEN resolved_by = '' THEN @actor ELSE resolved_by END
WHERE company_id = @company_id AND id = @id;

-- name: UnlinkMove :exec
UPDATE gate_moves SET visit_id = NULL, order_id = NULL, order_item_id = NULL
WHERE company_id = @company_id AND id = @id;

-- The visits a move opened or closed.
-- name: VisitsOfMove :many
SELECT * FROM visits WHERE company_id = @company_id AND (in_move_id = @move_id::text OR out_move_id = @move_id::text)
ORDER BY id FOR UPDATE;

-- name: ReopenVisit :exec
UPDATE visits SET status = 'in_yard', out_move_id = NULL, out_at = NULL, close_reason = ''
WHERE company_id = @company_id AND id = @id;

-- A corrected move rewrites its visit in place: position, grade and holds stay.
-- name: RewriteVisit :exec
UPDATE visits SET number = @number, type_code = @type_code, line_id = @line_id, customer_id = @customer_id,
    full_empty = @full_empty, status = @status, out_move_id = @out_move_id, out_at = @out_at, close_reason = @close_reason
WHERE company_id = @company_id AND id = @id;

-- name: SetVisitCloseReason :exec
UPDATE visits SET close_reason = @close_reason WHERE company_id = @company_id AND id = @id AND status = 'out';

-- name: DeleteDamages :exec
DELETE FROM damages WHERE company_id = @company_id AND move_id = @move_id;

-- name: InsertEvent :exec
INSERT INTO events (id, company_id, stream, kind, actor, payload, occurred_at)
VALUES (@id, @company_id, @stream, @kind, @actor, @payload, @occurred_at);

-- name: OpenOrderItems :many
SELECT i.* FROM order_items i JOIN release_orders o ON o.company_id = i.company_id AND o.id = i.order_id
WHERE i.company_id = @company_id AND o.depot_id = @depot_id AND o.status = 'open'
ORDER BY i.id;
