-- 002 yard: release/redelivery orders, visits (stock), holds, the block list,
-- damages, gate flags, approvals, EIR tokens and per-company yard settings.
-- All company data: composite foreign keys keep references inside one company.

CREATE TABLE release_orders (
    company_id  TEXT NOT NULL REFERENCES companies (id),
    id          TEXT NOT NULL,
    depot_id    TEXT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('release', 'redelivery')),
    reference   TEXT NOT NULL,
    line_id     TEXT,
    customer_id TEXT,
    valid_until DATE,
    note        TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done', 'cancelled')),
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, depot_id) REFERENCES depots (company_id, id),
    FOREIGN KEY (company_id, line_id) REFERENCES shipping_lines (company_id, id),
    FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id)
);
CREATE INDEX release_orders_status ON release_orders (company_id, depot_id, status, id);

-- An item is a named container (quantity 1) or a quantity of an ISO type.
CREATE TABLE order_items (
    company_id TEXT NOT NULL REFERENCES companies (id),
    id         TEXT NOT NULL,
    order_id   TEXT NOT NULL,
    number     TEXT,
    type_code  TEXT,
    full_empty TEXT NOT NULL DEFAULT 'E' CHECK (full_empty IN ('E', 'F')),
    quantity   INT NOT NULL DEFAULT 1 CHECK (quantity >= 1),
    used       INT NOT NULL DEFAULT 0 CHECK (used >= 0 AND used <= quantity),
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, order_id) REFERENCES release_orders (company_id, id),
    CHECK ((number IS NULL) <> (type_code IS NULL)),
    CHECK (number IS NULL OR quantity = 1)
);
CREATE INDEX order_items_order ON order_items (company_id, order_id, id);

-- Visit move ids are added below, once gate_moves can reference visits too.
CREATE TABLE visits (
    company_id   TEXT NOT NULL REFERENCES companies (id),
    id           TEXT NOT NULL,
    depot_id     TEXT NOT NULL,
    number       TEXT NOT NULL,
    type_code    TEXT NOT NULL DEFAULT '',
    line_id      TEXT,
    customer_id  TEXT,
    status       TEXT NOT NULL DEFAULT 'in_yard' CHECK (status IN ('in_yard', 'out')),
    full_empty   TEXT NOT NULL DEFAULT 'E' CHECK (full_empty IN ('E', 'F')),
    grade        TEXT NOT NULL DEFAULT '' CHECK (grade IN ('', 'A', 'B', 'C', 'FOOD')),
    block        TEXT NOT NULL DEFAULT '',
    row_no       TEXT NOT NULL DEFAULT '',
    tier         TEXT NOT NULL DEFAULT '',
    in_move_id   TEXT,
    out_move_id  TEXT,
    in_at        TIMESTAMPTZ NOT NULL,
    out_at       TIMESTAMPTZ,
    imported     BOOLEAN NOT NULL DEFAULT false,
    close_reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, depot_id) REFERENCES depots (company_id, id),
    FOREIGN KEY (company_id, line_id) REFERENCES shipping_lines (company_id, id),
    FOREIGN KEY (company_id, customer_id) REFERENCES customers (company_id, id),
    FOREIGN KEY (company_id, in_move_id) REFERENCES gate_moves (company_id, id),
    FOREIGN KEY (company_id, out_move_id) REFERENCES gate_moves (company_id, id)
);
-- A container is in at most one yard of a company at a time.
CREATE UNIQUE INDEX visits_one_in_yard ON visits (company_id, number) WHERE status = 'in_yard';
CREATE INDEX visits_status ON visits (company_id, depot_id, status, id);

CREATE TABLE holds (
    company_id  TEXT NOT NULL REFERENCES companies (id),
    id          TEXT NOT NULL,
    visit_id    TEXT NOT NULL,
    reason      TEXT NOT NULL,
    placed_by   TEXT NOT NULL,
    placed_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_by TEXT NOT NULL DEFAULT '',
    released_at TIMESTAMPTZ,
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, visit_id) REFERENCES visits (company_id, id)
);
CREATE INDEX holds_visit ON holds (company_id, visit_id, id);

CREATE TABLE block_list (
    company_id TEXT NOT NULL REFERENCES companies (id),
    id         TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('container', 'line')),
    value      TEXT NOT NULL,
    reason     TEXT NOT NULL,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (company_id, id),
    UNIQUE (company_id, kind, value)
);

ALTER TABLE gate_moves
    ADD COLUMN visit_id           TEXT,
    ADD COLUMN order_id           TEXT,
    ADD COLUMN order_item_id      TEXT,
    ADD COLUMN truck_plate        TEXT NOT NULL DEFAULT '',
    ADD COLUMN trailer_plate      TEXT NOT NULL DEFAULT '',
    ADD COLUMN driver_name        TEXT NOT NULL DEFAULT '',
    ADD COLUMN haulier_id         TEXT,
    ADD COLUMN seal               TEXT NOT NULL DEFAULT '',
    ADD COLUMN full_empty         TEXT NOT NULL DEFAULT '' CHECK (full_empty IN ('', 'E', 'F')),
    ADD COLUMN condition          TEXT NOT NULL DEFAULT '' CHECK (condition IN ('', 'sound', 'damaged')),
    ADD COLUMN signature_photo_id TEXT,
    ADD COLUMN form_saved_at      TIMESTAMPTZ,
    ADD COLUMN voided             BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN void_reason        TEXT NOT NULL DEFAULT '',
    ADD FOREIGN KEY (company_id, visit_id) REFERENCES visits (company_id, id),
    ADD FOREIGN KEY (company_id, order_id) REFERENCES release_orders (company_id, id),
    ADD FOREIGN KEY (company_id, order_item_id) REFERENCES order_items (company_id, id),
    ADD FOREIGN KEY (company_id, haulier_id) REFERENCES hauliers (company_id, id),
    ADD FOREIGN KEY (company_id, signature_photo_id) REFERENCES photos (company_id, id);

CREATE TABLE damages (
    company_id  TEXT NOT NULL REFERENCES companies (id),
    id          TEXT NOT NULL,
    move_id     TEXT NOT NULL,
    location    TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    photo_ids   TEXT[] NOT NULL CHECK (cardinality(photo_ids) BETWEEN 1 AND 3),
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, move_id) REFERENCES gate_moves (company_id, id)
);
CREATE INDEX damages_move ON damages (company_id, move_id, id);

CREATE TABLE gate_flags (
    company_id  TEXT NOT NULL REFERENCES companies (id),
    id          TEXT NOT NULL,
    move_id     TEXT NOT NULL,
    code        TEXT NOT NULL,
    detail      TEXT NOT NULL DEFAULT '',
    resolved_by TEXT NOT NULL DEFAULT '',
    resolved_at TIMESTAMPTZ,
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, move_id) REFERENCES gate_moves (company_id, id)
);
CREATE INDEX gate_flags_move ON gate_flags (company_id, move_id, id);

CREATE TABLE approvals (
    company_id   TEXT NOT NULL REFERENCES companies (id),
    id           TEXT NOT NULL,
    move_id      TEXT NOT NULL,
    reasons      JSONB NOT NULL DEFAULT '[]',
    requested_by TEXT NOT NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied')),
    decided_by   TEXT NOT NULL DEFAULT '',
    decided_at   TIMESTAMPTZ,
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, move_id) REFERENCES gate_moves (company_id, id)
);
CREATE INDEX approvals_move ON approvals (company_id, move_id, id);

-- The token itself is never stored, only its SHA-256 (like sessions).
CREATE TABLE eir_tokens (
    company_id TEXT NOT NULL REFERENCES companies (id),
    id         TEXT NOT NULL,
    move_id    TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_by TEXT NOT NULL,
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, move_id) REFERENCES gate_moves (company_id, id)
);

CREATE TABLE company_settings (
    company_id      TEXT PRIMARY KEY REFERENCES companies (id),
    refusal_mode    TEXT NOT NULL DEFAULT 'clerk_override'
        CHECK (refusal_mode IN ('clerk_override', 'manager_approval', 'none')),
    walkins_allowed BOOLEAN NOT NULL DEFAULT true,
    eir_ttl_days    INT NOT NULL DEFAULT 30 CHECK (eir_ttl_days >= 1)
);

-- The line last used for an owner prefix (first four letters of a number).
CREATE TABLE line_prefixes (
    company_id TEXT NOT NULL REFERENCES companies (id),
    prefix     CHAR(4) NOT NULL,
    line_id    TEXT NOT NULL,
    PRIMARY KEY (company_id, prefix),
    FOREIGN KEY (company_id, line_id) REFERENCES shipping_lines (company_id, id)
);

DO $$
DECLARE t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['release_orders', 'order_items', 'visits', 'holds', 'block_list', 'damages',
        'gate_flags', 'approvals', 'eir_tokens', 'company_settings', 'line_prefixes']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY company_isolation ON %I
            USING (company_id = current_setting(''app.company_id'', true))
            WITH CHECK (company_id = current_setting(''app.company_id'', true))', t);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO depot_app', t);
    END LOOP;
END $$;

-- The public EIR page has a token but no session: this finds its company and
-- move, only while the token is live.
CREATE FUNCTION eir_lookup(p_hash TEXT) RETURNS TABLE (company_id TEXT, move_id TEXT)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT t.company_id, t.move_id FROM eir_tokens t
    WHERE t.token_hash = p_hash AND t.expires_at > now()
$$;
REVOKE ALL ON FUNCTION eir_lookup(TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION eir_lookup(TEXT) TO depot_app;
