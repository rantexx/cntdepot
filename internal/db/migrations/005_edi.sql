-- 005 edi: shipping-line EDI (COPARN in, CODECO and APERAK out, daily reports).
-- Company data with composite foreign keys, like 002.

CREATE TABLE company_edi (
    company_id        TEXT PRIMARY KEY REFERENCES companies (id),
    inbound_token     TEXT NOT NULL UNIQUE,
    party_id          TEXT NOT NULL DEFAULT '',
    next_ref          BIGINT NOT NULL DEFAULT 1,
    events_scanned_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE line_edi (
    company_id TEXT NOT NULL REFERENCES companies (id),
    line_id    TEXT NOT NULL,
    mode       TEXT NOT NULL CHECK (mode IN ('edi', 'report', 'off')),
    profile    JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (company_id, line_id),
    FOREIGN KEY (company_id, line_id) REFERENCES shipping_lines (company_id, id)
);

ALTER TABLE depots ADD COLUMN edi_location TEXT NOT NULL DEFAULT '';

ALTER TABLE release_orders
    ADD COLUMN source         TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'edi')),
    ADD COLUMN edi_message_id TEXT;
CREATE UNIQUE INDEX release_orders_one_edi ON release_orders (company_id, line_id, kind, reference)
    WHERE source = 'edi';

CREATE TABLE edi_messages (
    company_id      TEXT NOT NULL REFERENCES companies (id),
    id              TEXT NOT NULL,
    direction       TEXT NOT NULL CHECK (direction IN ('in', 'out')),
    kind            TEXT NOT NULL CHECK (kind IN ('coparn', 'codeco', 'aperak', 'report', 'test', 'unknown')),
    status          TEXT NOT NULL CHECK (status IN ('received', 'pending', 'sent', 'accepted', 'rejected',
        'failed', 'unmatched', 'error', 'done', 'ignored')),
    line_id         TEXT,
    depot_id        TEXT,
    move_id         TEXT,
    order_id        TEXT,
    event_id        TEXT,
    answer_of       TEXT,
    function        TEXT NOT NULL DEFAULT '',
    control_ref     TEXT NOT NULL DEFAULT '',
    message_ref     TEXT NOT NULL DEFAULT '',
    content_hash    TEXT NOT NULL DEFAULT '',
    blob_digest     TEXT NOT NULL DEFAULT '',
    file_name       TEXT NOT NULL DEFAULT '',
    sender          TEXT NOT NULL DEFAULT '',
    via             TEXT NOT NULL DEFAULT 'upload' CHECK (via IN ('mail', 'upload')),
    trusted         BOOLEAN NOT NULL DEFAULT false, -- mail only: the sender was authenticated (spec 5.8 rule 5)
    problems        JSONB NOT NULL DEFAULT '[]',
    tries           INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    report_day      DATE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at         TIMESTAMPTZ,
    PRIMARY KEY (company_id, id),
    FOREIGN KEY (company_id, line_id) REFERENCES shipping_lines (company_id, id),
    FOREIGN KEY (company_id, depot_id) REFERENCES depots (company_id, id),
    FOREIGN KEY (company_id, move_id) REFERENCES gate_moves (company_id, id),
    FOREIGN KEY (company_id, order_id) REFERENCES release_orders (company_id, id),
    FOREIGN KEY (company_id, answer_of) REFERENCES edi_messages (company_id, id)
);
CREATE UNIQUE INDEX edi_messages_one_per_event ON edi_messages (company_id, event_id, line_id)
    WHERE event_id IS NOT NULL;
CREATE UNIQUE INDEX edi_messages_one_report ON edi_messages (company_id, line_id, depot_id, report_day)
    WHERE kind = 'report';
CREATE INDEX edi_messages_queue ON edi_messages (company_id, status, next_attempt_at);

DO $$
DECLARE t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['company_edi', 'line_edi', 'edi_messages']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY company_isolation ON %I
            USING (company_id = current_setting(''app.company_id'', true))
            WITH CHECK (company_id = current_setting(''app.company_id'', true))', t);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO depot_app', t);
    END LOOP;
END $$;

-- The inbound mail hook has a token but no session: this finds its company.
CREATE FUNCTION edi_inbound_company(p_token TEXT) RETURNS TEXT
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT company_id FROM company_edi WHERE inbound_token = p_token
$$;
REVOKE ALL ON FUNCTION edi_inbound_company(TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION edi_inbound_company(TEXT) TO depot_app;

-- The background workers run without a company: this lists the ones with EDI.
CREATE FUNCTION edi_companies() RETURNS SETOF TEXT
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT company_id FROM company_edi ORDER BY company_id
$$;
REVOKE ALL ON FUNCTION edi_companies() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION edi_companies() TO depot_app;
