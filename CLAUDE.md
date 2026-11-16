# depotd

depotd is a container depot app: a Go service (cmd/depotd, internal/*) with a React web UI (web/).
It records container gate moves, reads container numbers from gate camera photos (OCR, ISO 6346
check digits), and serves a queue and move records to depot staff. It is moving from a single-site
SQLite store to a multi-company PostgreSQL platform (Stage 0).

## Before saying work is done

Run `bash scripts/check.sh`. It must print `CHECK OK`. A Stop hook runs it too
(skipped while `.superpowers/check-hold` exists).

## Never break

- Every tenant table has `company_id` and row-level security (RLS). No query bypasses it.
- Company and user scope come from the session, never from request bodies, URLs or headers the client controls.
- Generated code is never hand-edited. Change the source (SQL, schema, spec) and regenerate.
- Store queries are SQL in `internal/store/queries` (and `internal/yard/queries`); `scripts/gen.sh` runs sqlc (pinned release binary from `scripts/tools.sh`, no cgo needed) into `internal/store/db` (and `internal/yard/db`).
- The gate never stops: a failure in recognition, camera, database or network must not block a truck
  from entering or leaving. Degrade to manual entry and queue the work.
- Dev login never exists in production builds or production config.

## Where things are

- Stage 0 spec: `docs/superpowers/specs/2026-11-16-stage-0-foundation-design.md`
- Roadmap: `docs/superpowers/specs/2026-11-15-depot-app-roadmap.md`
- Stage 0a plan: `docs/superpowers/plans/2026-11-16-stage-0a-foundation.md`
- Product and design: `PRODUCT.md`, `DESIGN.md`
- Local test kit runbook: `docs/runbooks/local-test-kit.md`
