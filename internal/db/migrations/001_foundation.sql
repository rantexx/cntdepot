-- 001 foundation: multi-company schema with row-level security.
-- Runs as the database owner; role depot_app already exists (db.Migrate).

-- Only the owner creates objects; depot_app must never own anything.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

-- Outside any company: no RLS.
CREATE TABLE companies (
    id         TEXT PRIMARY KEY CHECK (id <> ''),
    name       TEXT NOT NULL,
    country    TEXT NOT NULL DEFAULT '',
    modules    TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE users (
    id          TEXT PRIMARY KEY,
    email       TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL DEFAULT '',
    idp_subject TEXT UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE sessions (
    id_hash    TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    company_id TEXT REFERENCES companies (id) ON DELETE CASCADE,
    depot_id   TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_user ON sessions (user_id);
CREATE TABLE platform_admins (
    email TEXT PRIMARY KEY
);
CREATE TABLE ref_smdg_liners (
    code    TEXT PRIMARY KEY,
    name    TEXT NOT NULL,
    version TEXT NOT NULL
);
CREATE TABLE ref_locodes (
    code     TEXT PRIMARY KEY,
    country  TEXT NOT NULL,
    name     TEXT NOT NULL,
    function TEXT NOT NULL DEFAULT '',
    version  TEXT NOT NULL
);
CREATE TABLE ref_versions (
    name    TEXT PRIMARY KEY,
    version TEXT NOT NULL
);

-- Company data: RLS on (bottom of file).
CREATE TABLE depots (
    company_id TEXT NOT NULL REFERENCES companies (id),
    id         TEXT NOT NULL,
    name       TEXT NOT NULL,
    country    TEXT NOT NULL DEFAULT '',
    timezone   TEXT NOT NULL DEFAULT 'UTC',
    PRIMARY KEY (company_id, id)
);
CREATE TABLE memberships (
    company_id TEXT NOT NULL REFERENCES companies (id),
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('owner', 'manager', 'office', 'gate')),
    depot_ids  TEXT[], -- NULL = all depots
    PRIMARY KEY (company_id, user_id)
);
CREATE INDEX memberships_user ON memberships (user_id);
CREATE TABLE invitations (
    company_id TEXT NOT NULL REFERENCES companies (id),
    id         TEXT NOT NULL,
    email      TEXT NOT NULL,
    role       TEXT NOT NULL CHECK (role IN ('owner', 'manager', 'office', 'gate')),
    depot_ids  TEXT[], -- NULL = all depots
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    used_at    TIMESTAMPTZ,
    PRIMARY KEY (company_id, id)
);
CREATE INDEX invitations_open_email ON invitations (lower(email)) WHERE used_at IS NULL;
CREATE TABLE shipping_lines (
    company_id TEXT NOT NULL REFERENCES companies (id),
    id         TEXT NOT NULL,
    code       TEXT NOT NULL,
    name       TEXT NOT NULL,
    contact    TEXT NOT NULL DEFAULT '',
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (company_id, id),
    UNIQUE (company_id, code)
);
CREATE TABLE customers (LIKE shipping_lines INCLUDING DEFAULTS INCLUDING CONSTRAINTS);
ALTER TABLE customers ADD PRIMARY KEY (company_id, id), ADD UNIQUE (company_id, code),
    ADD FOREIGN KEY (company_id) REFERENCES companies (id);
CREATE TABLE hauliers (LIKE shipping_lines INCLUDING DEFAULTS INCLUDING CONSTRAINTS);
ALTER TABLE hauliers ADD PRIMARY KEY (company_id, id), ADD UNIQUE (company_id, code),
    ADD FOREIGN KEY (company_id) REFERENCES companies (id);

-- Gate data: the SQLite v1+v2 columns, tenant_id renamed to company_id.
CREATE TABLE gate_moves (
    company_id        TEXT NOT NULL REFERENCES companies (id),
    id                TEXT NOT NULL,
    depot_id          TEXT NOT NULL,
    lane              TEXT NOT NULL DEFAULT '',
    direction         TEXT NOT NULL DEFAULT 'in',
    status            TEXT NOT NULL,
    number            TEXT NOT NULL DEFAULT '',
    type_code         TEXT NOT NULL DEFAULT '',
    color             TEXT NOT NULL DEFAULT '',
    confidence        DOUBLE PRECISION NOT NULL DEFAULT 0,
    check_digit_valid BOOLEAN NOT NULL DEFAULT false,
    decision          TEXT NOT NULL DEFAULT '',
    reason            TEXT NOT NULL DEFAULT '',
    result_json       JSONB NOT NULL DEFAULT '{}',
    verified          BOOLEAN NOT NULL DEFAULT false,
    corrected_from    TEXT NOT NULL DEFAULT '',
    resolved_by       TEXT NOT NULL DEFAULT '',
    note              TEXT NOT NULL DEFAULT '',
    is_test           BOOLEAN NOT NULL DEFAULT false,
    source            TEXT NOT NULL DEFAULT 'phone',
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (company_id, id)
);
CREATE INDEX gate_moves_status ON gate_moves (company_id, status, id);
CREATE INDEX gate_moves_source ON gate_moves (company_id, source, id);
CREATE TABLE photos (
    company_id    TEXT NOT NULL REFERENCES companies (id),
    id            TEXT NOT NULL,
    move_id       TEXT NOT NULL,
    role          TEXT NOT NULL,
    sha256        TEXT NOT NULL,
    ai_sha256     TEXT NOT NULL,
    media_type    TEXT NOT NULL,
    ai_media_type TEXT NOT NULL,
    bytes         INTEGER NOT NULL,
    width         INTEGER NOT NULL DEFAULT 0,
    height        INTEGER NOT NULL DEFAULT 0,
    captured_at   TIMESTAMPTZ NOT NULL,
    deferred      BOOLEAN NOT NULL DEFAULT false,
    read_enabled  BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, move_id) REFERENCES gate_moves (company_id, id)
);
CREATE INDEX photos_move ON photos (company_id, move_id, id);
CREATE TABLE recognition_attempts (
    company_id     TEXT NOT NULL REFERENCES companies (id),
    id             TEXT NOT NULL,
    move_id        TEXT NOT NULL,
    photo_id       TEXT NOT NULL,
    engine         TEXT NOT NULL,
    model          TEXT NOT NULL,
    prompt_version TEXT NOT NULL,
    reading_json   JSONB NOT NULL,
    raw            TEXT NOT NULL,
    error          TEXT NOT NULL DEFAULT '',
    input_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens  INTEGER NOT NULL DEFAULT 0,
    latency_ms     INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, move_id) REFERENCES gate_moves (company_id, id),
    FOREIGN KEY (company_id, photo_id) REFERENCES photos (company_id, id)
);
CREATE INDEX attempts_move ON recognition_attempts (company_id, move_id, id);
CREATE INDEX attempts_photo ON recognition_attempts (company_id, photo_id, id);
CREATE TABLE events (
    seq         BIGSERIAL PRIMARY KEY,
    id          TEXT NOT NULL UNIQUE,
    company_id  TEXT NOT NULL REFERENCES companies (id),
    stream      TEXT NOT NULL,
    kind        TEXT NOT NULL,
    actor       TEXT NOT NULL,
    payload     JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX events_stream ON events (company_id, stream, seq);
CREATE TABLE settings (
    company_id TEXT NOT NULL REFERENCES companies (id),
    depot_id   TEXT NOT NULL DEFAULT '',
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (company_id, depot_id, key)
);
CREATE TABLE usage_counters (
    company_id TEXT NOT NULL REFERENCES companies (id),
    kind       TEXT NOT NULL,
    day        DATE NOT NULL,
    count      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (company_id, kind, day)
);

-- RLS: a row is visible only when app.company_id (set by db.InCompany) matches.
-- An unset setting is NULL or '' and matches no row. No FORCE: the owner-owned
-- SECURITY DEFINER functions below rely on the owner bypassing RLS.
DO $$
DECLARE t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['depots', 'memberships', 'invitations', 'shipping_lines', 'customers',
        'hauliers', 'gate_moves', 'photos', 'recognition_attempts', 'events', 'settings', 'usage_counters']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY company_isolation ON %I
            USING (company_id = current_setting(''app.company_id'', true))
            WITH CHECK (company_id = current_setting(''app.company_id'', true))', t);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO depot_app', t);
    END LOOP;
END $$;
GRANT USAGE ON SEQUENCE events_seq_seq TO depot_app;

GRANT SELECT, INSERT ON companies TO depot_app;
-- The owner switches the company's modules (the app updates only the session's company).
GRANT UPDATE (modules) ON companies TO depot_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON users, sessions TO depot_app;
GRANT SELECT ON platform_admins, ref_smdg_liners, ref_locodes, ref_versions TO depot_app;

-- Cross-company access, only for packages auth and platform. pg_temp is listed
-- last so depot_app cannot shadow these tables with temporary ones.
CREATE FUNCTION memberships_for_user(p_user TEXT)
RETURNS TABLE (company_id TEXT, company_name TEXT, role TEXT, depot_ids TEXT[])
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT m.company_id, c.name, m.role, m.depot_ids
    FROM memberships m JOIN companies c ON c.id = m.company_id
    WHERE m.user_id = p_user
    ORDER BY c.name, m.company_id
$$;

-- Turns the unused invitations for an email into memberships (an existing
-- membership keeps its role), marks them used and returns how many.
CREATE FUNCTION accept_invitations(p_user TEXT, p_email TEXT) RETURNS INT
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE n INT;
BEGIN
    WITH used AS (
        UPDATE invitations SET used_at = now()
        WHERE lower(email) = lower(p_email) AND used_at IS NULL
        RETURNING company_id, role, depot_ids, created_at
    ), joined AS (
        INSERT INTO memberships (company_id, user_id, role, depot_ids)
        SELECT DISTINCT ON (company_id) company_id, p_user, role, depot_ids
        FROM used ORDER BY company_id, created_at DESC
        ON CONFLICT (company_id, user_id) DO NOTHING
    )
    SELECT count(*) INTO n FROM used;
    RETURN n;
END
$$;

REVOKE ALL ON FUNCTION memberships_for_user(TEXT), accept_invitations(TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION memberships_for_user(TEXT), accept_invitations(TEXT, TEXT) TO depot_app;
