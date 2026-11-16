# Stage 0a Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the single-depot SQLite hub into a multi-company PostgreSQL app with row-level security, personal logins (development login now, WorkOS later), generated API code, admin lists and a switchable OCR module. Everything here runs locally, with no cloud accounts.

**Architecture:**
- **One Go program, `depotd`.** Modes: `api` (cloud web + API), `migrate`, `hub` (developer tool with embedded PostgreSQL), `bench`, `export-dataset`.
- **Data access** goes through a `store` on a `pgxpool`. Every query runs in a transaction that sets `app.company_id` from the request's `tenancy.Scope`. Postgres RLS makes rows of other companies invisible.
- **The two cross-company lookups** that login needs live in SECURITY DEFINER functions owned by the migration role.
- **API:** described in `api/openapi.yaml`. oapi-codegen generates the Go strict server; Orval generates the TypeScript fetch client. Refine core hooks drive the admin screens.

**Tech Stack:** Go 1.27, pgx/v5, fergusstrange/embedded-postgres (dev/test), sqlc (fallback: pgx `RowToStructByName`), oapi-codegen v2 (as a `go tool`), React 19 + TypeScript 7 + Vite 8, Orval, @refinedev/core.

**Spec:** `docs/superpowers/specs/2026-11-16-stage-0-foundation-design.md`. The roadmap is `docs/superpowers/specs/2026-11-15-depot-app-roadmap.md`. Part 0b (WorkOS, Fly.io, R2, Sentry) is a separate plan, written once the owner has the accounts.

## Global Constraints

- **Data rules:**
  - Every table of company data has `company_id TEXT NOT NULL` and an RLS policy `company_id = current_setting('app.company_id', true)`.
  - A missing setting matches no rows.
  - Tables outside any company: `companies`, `users`, `sessions`, `platform_admins`, `ref_*`, `schema_version`.
- **Database roles:**
  - The app connects as role `depot_app`: `NOSUPERUSER NOBYPASSRLS`, not owner of any table.
  - Migrations run as the database owner.
  - No BYPASSRLS role is created. Cross-company access is only through the SECURITY DEFINER functions in Task 3, called only from packages `auth` and `platform`.
- **The current company and depot** come from the session (`tenancy.Scope` in the context), never from a request body.
- **Roles:** `owner`, `manager`, `office`, `gate`. The permission table is in Task 7.
- **No hand edits to generated code:**
  - `internal/api/gen.go`, `web/src/gen/**`, `internal/store/db/**` (sqlc output) are generated.
  - `scripts/check.sh` fails when they drift.
- **Dev login:** refused at startup when `-env production`.
- **The gate never stops:**
  - with module `ocr` off, photos are stored and the clerk types the number;
  - nothing is ever read by AI unless `photos.read_enabled = true`;
  - switching `ocr` on never reads older photos.
- **Identifiers and times:** IDs are ULIDs (`github.com/oklog/ulid/v2`), as today. Timestamps are `timestamptz`.
- **Copy:** English and Turkish strings for every new screen go in `web/src/i18n.ts`, following the existing keys.
- **Commits:** end every message with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Go on this PC:** at `C:\Program Files\Go\bin`, not on PATH. In bash, run `export PATH="$PATH:/c/Program Files/Go/bin"`.

## Review Focus

1. **Two companies, one live stream.** An SSE client of company A must never receive company B's move events → Task 9 test `TestStreamOnlyCarriesOwnCompany`.
2. **Photo digests are shared across companies** (content-addressed blobs). `GET /api/blobs/{digest}` must return 404 to company A for a digest that only company B uploaded, even though the bytes exist → Task 9 test `TestBlobOfOtherCompanyIs404`.
3. **Invitation emails:**
   - `" Ali@Example.COM "` must match an invitation for `ali@example.com`;
   - an unverified email must match nothing

   → Task 7 test `TestInvitationMatchesNormalisedVerifiedEmailOnly`.
4. **OCR switched off with photos already queued.** No engine call happens after the switch-off, and the moves stay open for a person → Task 6 test `TestQueuedReadsStopWhenModuleTurnsOff`.
5. **Removed or demoted users with a live session.** A member removed from a company, or demoted to `gate`, loses access on their very next request (memberships are checked on every request, not cached in the session) → Task 7 test `TestRemovedMemberLosesAccessOnNextRequest`.

---

### Task 1: Git housekeeping and the checks

**Files:**
- Create: `scripts/check.sh`, `CLAUDE.md`, `.claude/settings.json`, `.github/workflows/check.yml`
- Modify: `.gitignore` (add `.tools/`, `web/node_modules/` if missing)

**Interfaces:**
- Produces: `bash scripts/check.sh`, exit 0 when everything passes. Later tasks extend it with code-generation drift checks (Tasks 3 and 8).

- [ ] **Step 1: Git housekeeping.** Run in `C:\Users\OZGUN\depot-platform`:
  ```bash
  git checkout main && git merge --ff-only feat/gate-camera && git tag depot-pc-v1 && git checkout -b stage-0-foundation
  ```
  Expected: the fast-forward succeeds (main is an ancestor), tag created, new branch checked out.

- [ ] **Step 2: Write `scripts/check.sh`** (bash, `set -euo pipefail`). In order:
  1. add Go to PATH if `go` is missing;
  2. `test -z "$(gofmt -l cmd internal)"`;
  3. `go vet ./...`;
  4. `go test ./...`;
  5. `npm --prefix web run build`.

  Print `CHECK OK` at the end.

- [ ] **Step 3: Run it.** Run: `bash scripts/check.sh`. Expected: `CHECK OK`. The existing SQLite tests still pass at this point.

- [ ] **Step 4: Write `CLAUDE.md`.** At most 40 lines:
  - what depotd is (one paragraph);
  - "Run `bash scripts/check.sh` before saying work is done";
  - the never-break rules from Global Constraints (RLS and `company_id`, scope from session, generated code not hand-edited, the gate never stops, dev login never in production);
  - where the spec and roadmap live.

- [ ] **Step 5: Write `.claude/settings.json`.** Add a `Stop` hook that runs `bash scripts/check.sh` and blocks finishing on a non-zero exit, so the failure output reaches the agent. Write `.github/workflows/check.yml`: ubuntu-latest, setup-go `1.27.x`, setup-node `24`, `npm --prefix web ci`, `bash scripts/check.sh`.

- [ ] **Step 6: Commit.**
  ```bash
  git add scripts/check.sh CLAUDE.md .claude/settings.json .github/workflows/check.yml .gitignore
  git commit -m "chore: one check command, project instructions, stop hook and CI workflow"
  ```

### Task 2: PostgreSQL for tests and development

**Files:**
- Create: `internal/pgtest/pgtest.go`, `internal/pgtest/pgtest_test.go`
- Modify: `go.mod` (add `github.com/jackc/pgx/v5`, `github.com/fergusstrange/embedded-postgres`)

**Interfaces:**
- Produces:
  - `pgtest.Main(m *testing.M) int`: starts one embedded PostgreSQL per test binary (free port, temp data dir, binaries cached in `os.UserCacheDir()/depot/embedded-postgres`), runs `m.Run()`, stops the server.
  - `pgtest.NewDB(t *testing.T) (ownerURL string)`: creates a fresh database `t_<ulid>` and returns its superuser URL. The database is dropped in `t.Cleanup`.
- Every package with database tests has `func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }`.

- [ ] **Step 1: Write the failing test** `TestNewDBGivesIsolatedDatabases`:
  - two `NewDB` calls return different URLs;
  - a table created in one is absent in the other (query `to_regclass('x') IS NULL`).

- [ ] **Step 2: Run it.** Run: `go test ./internal/pgtest/ -run TestNewDB -v`. Expected: FAIL (package missing).

- [ ] **Step 3: Implement `Main` and `NewDB`** with embedded-postgres. The first run downloads PostgreSQL binaries (about 30 MB) into the cache; later runs reuse them. Use PostgreSQL version 17 if the library offers it, else the newest it offers.

- [ ] **Step 4: Run it.** Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -m "test: embedded PostgreSQL for tests"`

### Task 3: Schema, roles, RLS and the migration runner

**Files:**
- Create:
  - `internal/db/db.go`, `internal/db/migrations/001_foundation.sql`, `internal/db/db_test.go`
  - `internal/tenancy/tenancy.go`, `internal/tenancy/tenancy_test.go`
- Delete (in Task 4, once nothing uses them): `internal/store/schema.sql`, `internal/store/schema_v2.sql`

**Interfaces:**
- Produces in `tenancy`:
  - `type Role string` with constants `Owner`, `Manager`, `Office`, `Gate` (values `owner`, `manager`, `office`, `gate`).
  - `type Scope struct{ CompanyID, DepotID, UserID string; Role Role }`.
  - `func With(ctx context.Context, s Scope) context.Context` and `func From(ctx context.Context) (Scope, bool)`.
  - `var ErrNoScope = errors.New("tenancy: no company in context")`.
- Produces in `db`:
  - `type MigrateOptions struct{ AppPassword, PlatformAdminEmail string }`.
  - `func Migrate(ctx context.Context, ownerURL string, o MigrateOptions) error`:
    - applies embedded `migrations/*.sql` in order, recording them in `schema_version`;
    - creates or updates role `depot_app` with `o.AppPassword`;
    - grants it `SELECT, INSERT, UPDATE, DELETE` on company tables, `SELECT` on `ref_*`, and access to `companies, users, sessions, platform_admins` as listed below;
    - upserts `o.PlatformAdminEmail` (lower-cased) into `platform_admins` when non-empty.

    It is idempotent.
  - `func AppURL(ownerURL, password string) string`: the same URL with user `depot_app`.
  - `func Open(ctx context.Context, appURL string) (*pgxpool.Pool, error)`.
  - `func InCompany(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error`:
    - takes the company from `tenancy.From(ctx)`, or returns `tenancy.ErrNoScope`;
    - begins a transaction;
    - runs `SELECT set_config('app.company_id', $1, true)`;
    - calls `fn`, then commits.
  - `func Global(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error`: a transaction with no company set, for tables outside any company.
- Tables in `001_foundation.sql`:
  - **Outside any company, no RLS:**
    - `companies(id, name, country, modules text[] not null default '{}', created_at)`;
    - `users(id, email unique, name, idp_subject unique, created_at)`;
    - `sessions(id_hash text pk, user_id, company_id null, depot_id null, created_at, expires_at)`;
    - `platform_admins(email pk)`;
    - `ref_smdg_liners(code pk, name, version)`;
    - `ref_locodes(code pk, country, name, function, version)`;
    - `ref_versions(name pk, version)`.
  - **Company data, RLS on:**
    - `depots(company_id, id, name, country, timezone, pk(company_id,id))`;
    - `memberships(company_id, user_id, role, depot_ids text[] null = all, pk(company_id,user_id))`;
    - `invitations(company_id, id, email, role, depot_ids, created_by, created_at, used_at null)`;
    - `shipping_lines`, `customers`, `hauliers`, each `(company_id, id, code, name, contact, active bool default true, created_at, unique(company_id, code))`;
    - `gate_moves`, `photos`, `recognition_attempts`, `events`, all with today's columns:
      - `tenant_id` renamed to `company_id`;
      - INTEGER booleans become `boolean`;
      - TEXT times become `timestamptz`;
      - `result_json`, `reading_json` and `payload` become `jsonb`;
      - `events.seq` becomes `bigserial`;
      - `photos` gains `read_enabled boolean not null default false`;
    - `settings(company_id, depot_id, key, value, updated_at, pk(company_id, depot_id, key))`;
    - `usage_counters(company_id, kind, day date, count bigint, pk(company_id, kind, day))`.
  - **SECURITY DEFINER functions** (owned by the migration role, `SET search_path = public`, `EXECUTE` granted to `depot_app`):
    - `memberships_for_user(p_user text) RETURNS TABLE(company_id text, company_name text, role text, depot_ids text[])`;
    - `accept_invitations(p_user text, p_email text) RETURNS int`: turns unused invitations for the lower-cased email into memberships, marks them used, returns the count.
    - `depot_app` gets `SELECT` on `companies` only. Creating a company happens in `platform` through `Global` + insert, so grant `INSERT` on `companies` too.
- `depot_app` gets `SELECT, INSERT, UPDATE, DELETE` on `users` and `sessions` (needed by login), and `SELECT` on `platform_admins` (for the platform-admin check).

- [ ] **Step 1: Write the failing tests** in `internal/db/db_test.go`:
  - `TestMigrateIsIdempotent`: Migrate twice → no error; `schema_version` has 1 row.
  - `TestAppRoleCannotBypassRLS`: as `depot_app`, `SELECT rolbypassrls, rolsuper FROM pg_roles WHERE rolname=current_user` → false, false.
  - `TestCompanyRowsInvisibleAcrossCompanies`:
    - insert a shipping line for company `A` via `InCompany` scope A;
    - under scope B, `SELECT count(*) FROM shipping_lines` → 0;
    - under scope B, `UPDATE shipping_lines SET name='x'` affects 0 rows;
    - under scope B, an insert with `company_id='A'` fails with RLS error code `42501`.
  - `TestMissingScopeSeesNothing`: `Global` → `SELECT count(*) FROM shipping_lines` → 0 even with rows present.
  - `TestAcceptInvitationsByEmail`: an invitation for `ali@example.com` in company A; `SELECT accept_invitations('u1','ali@example.com')` as `depot_app` → 1; `memberships_for_user('u1')` returns company A with the invited role.
  - In `tenancy_test.go`: `TestScopeRoundTrip`, and `From(context.Background())` returns `false`.

- [ ] **Step 2: Run them.** Run: `go test ./internal/db/ ./internal/tenancy/ -v`. Expected: FAIL.

- [ ] **Step 3: Implement** `tenancy`, `db` and `001_foundation.sql` to the interfaces above. Each RLS policy is `USING (company_id = current_setting('app.company_id', true)) WITH CHECK (same)`, with `ALTER TABLE … ENABLE ROW LEVEL SECURITY`.

- [ ] **Step 4: Run them.** Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -m "feat(db): PostgreSQL schema with row-level security and app role"`

### Task 4: Store on PostgreSQL (sqlc)

**Files:**
- Create: `sqlc.yaml`, `internal/store/queries/*.sql`, `internal/store/db/` (generated), `scripts/tools.sh`, `scripts/gen.sh`
- Modify: `internal/store/*.go` (rewrite onto pgx; same exported method names and types as today), `internal/store/*_test.go` (port to pgtest), `scripts/check.sh` (run `scripts/gen.sh` then `git diff --exit-code -- internal/store/db internal/api web/src/gen`)
- Delete: `internal/store/schema.sql`, `internal/store/schema_v2.sql`, the SQLite migration test `TestMigratesAVersion1Database`; `modernc.org/sqlite` from `go.mod` once unused

**Interfaces:**
- Consumes: `db.InCompany`, `db.Global`, `tenancy.Scope` (Task 3).
- Produces: `store.New(pool *pgxpool.Pool) *Store`. `Open(path, tenant, depot)` is removed. Every existing method keeps its name and signature:
  - `CreateMove`, `CreateMoveWithSource`, `GetMove`, `ListMoves`, `VerifiedMoves`, `SetStatus`, `SetResult`, `Resolve`;
  - `AddPhoto`, `ListPhotos`, `GetPhoto`, `MarkForReading`, `PhotosWithoutAttempts`;
  - `AddAttempt`, `ListAttempts`;
  - `Events`;
  - `Settings`, `SetSettings`.
- Behaviour changes:
  - Each method reads company and depot from `tenancy.From(ctx)`; without a scope it returns `tenancy.ErrNoScope`.
  - `PhotosWithoutAttempts` now returns only photos with `read_enabled = true`.
  - `Photo` gains `ReadEnabled bool`.
  - `Move` and `Photo` keep their JSON field names (the web app reads them).
- New methods:
  - `CompanyIDs(ctx) ([]string, error)` (via `Global`);
  - `CompanyModules(ctx) ([]string, error)`;
  - `PhotoByDigest(ctx, digest string) (Photo, error)`: matches `sha256` or `ai_sha256` within the company; `ErrNotFound` otherwise;
  - `AddUsage(ctx, kind string, n int64) error`: upsert on today's UTC date.
- `scripts/tools.sh`:
  - downloads a pinned sqlc release binary for the OS into `.tools/`;
  - `go run` cannot build sqlc's PostgreSQL parser without cgo, and this PC has no C compiler;
  - pin the newest release at implementation time and record its version and SHA-256 in the script.
- **Fallback, decided now:** if the sqlc Windows binary cannot parse the PostgreSQL schema, drop sqlc. Write the queries by hand with `pgx.CollectRows(rows, pgx.RowToStructByName[T])`, remove `sqlc.yaml`, and note it in `CLAUDE.md`. The store tests are the same either way.

- [ ] **Step 1: Port the existing store tests** to `pgtest.NewDB` + `db.Migrate` + `store.New`, each running under `tenancy.With(ctx, Scope{CompanyID: "A", DepotID: "d1"})` after inserting company A and depot d1. Add the new tests:
  - `TestEveryStoreMethodRespectsCompany`: create a move, photo, attempt and setting as company A. Under scope B, every read method returns empty or `ErrNotFound`, and every write by id returns `ErrNotFound`.
  - `TestNoScopeIsAnError`: `GetMove(context.Background(), id)` → `errors.Is(err, tenancy.ErrNoScope)`.
  - `TestOnlyReadEnabledPhotosArePending`: one photo with `ReadEnabled=false`, one `true` → `PhotosWithoutAttempts` returns only the second.
  - `TestPhotoByDigestStaysInCompany`: a digest added by B → `ErrNotFound` for A.
  - `TestUsageCountsPerDay`: `AddUsage("ocr_read",1)` twice → today's count is 2.

- [ ] **Step 2: Run them.** Run: `go test ./internal/store/ -v`. Expected: FAIL (still SQLite).

- [ ] **Step 3: Implement.** Write `scripts/tools.sh`, `scripts/gen.sh` (runs `.tools/sqlc generate`, `go generate ./...`, `npm --prefix web run gen` when present), `sqlc.yaml` (engine postgresql, schema `internal/db/migrations`, queries `internal/store/queries`, gen go package `db` with `sql_package: pgx/v5`) and the store rewrite.

- [ ] **Step 4: Run them.** Expected: PASS. Then run `bash scripts/check.sh`. It fails only in packages that still call `store.Open`; those are fixed in Tasks 5 and 6.

- [ ] **Step 5: Commit.** `git commit -m "feat(store): PostgreSQL store scoped by company"`

### Task 5: Gate service, recognition and tools on the new store

**Files:**
- Modify:
  - `internal/gate/service.go` and tests;
  - `internal/gatecam/*.go` and tests;
  - `internal/bench/*.go`, `internal/dataset/*.go`, `internal/testkit/*.go` and their tests;
  - `internal/httpapi/*_test.go` (only what is needed to compile until Task 9).

**Interfaces:**
- Consumes: `store.New`, `tenancy.With`, `store.CompanyIDs` (Task 4).
- Produces:
  - **The gate service's company context.** `gate.Service` has the same exported methods. Every job carries the `tenancy.Scope` it was created under, and `process` runs with that scope.
  - **Startup recovery.** `Service.Start(ctx)` loops `store.CompanyIDs` and recovers each company under its own scope (open moves settle, pending photos re-queue).
  - **Notifications carry the company.** `gate.New(st, bl, rec, cfg, notify func(companyID, moveID string))`. The broker change that uses it is in Task 9; until then `main.go` adapts it.
  - **The camera manager's scope.** `gatecam.NewManager(st *store.Store, w *Watcher, live LiveSink, build Builder, scope tenancy.Scope) *Manager`. The camera belongs to one company and depot, which in `hub` mode are fixed.
  - **Bench and dataset export.** `bench.Run(ctx, st, bl, rec, threshold, limit)` and `dataset.Export(...)` keep their signatures and expect a scoped ctx.

- [ ] **Step 1: Port the tests** of gate, gatecam, bench, dataset and testkit to pgtest with company A scope. Add:
  - `TestStartRecoversEachCompanyInItsOwnScope`: pending photos in A and B are each re-queued, and each resulting attempt has its own company's `company_id`.

- [ ] **Step 2: Run them.** Run: `go test ./internal/gate/ ./internal/gatecam/ ./internal/bench/ ./internal/dataset/ -v`. Expected: FAIL.

- [ ] **Step 3: Implement** the scope-carrying jobs and per-company recovery.

- [ ] **Step 4: Run them.** Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -m "refactor(gate): gate service, camera and tools run per company"`

### Task 6: OCR module switch, model fallback and usage counting

**Files:**
- Modify:
  - `internal/gate/service.go` (+ test);
  - `internal/recognition/gemini.go` (+ `gemini_test.go`);
  - `internal/store/` (if a query is missing).

**Interfaces:**
- Consumes: `store.CompanyModules`, `store.AddUsage`, `Photo.ReadEnabled` (Task 4).
- Produces:
  - `const gate.ModuleOCR = "ocr"`.
  - `storePhoto` sets `ReadEnabled` to whether `ocr` is in `CompanyModules` at that moment. Only read-enabled photos are queued.
  - `process` checks the module again before calling the engine. If it is off, it skips the read and leaves the move open.
  - Each engine call that returns adds 1 to usage `ocr_read`.
  - `func (s *Service) ManualEntry(ctx context.Context, moveID, number, by string) (store.Move, error)`:
    - normalises the number with `iso6346.Normalize`;
    - if it is valid, resolves as accept with `Verified=true`;
    - otherwise stores the number, sets status `needs_review` with reason `typed number fails its check digit`, and returns the move;
    - an empty number gives `ErrNumberRequired`.
  - `recognition.GeminiConfig` gains `Fallbacks []string`, default `["gemini-3.5-flash"]` when `Model` is the default.
    - On HTTP 503 or 429 from one model, after its own `Retries`, the engine tries the next model.
    - `Attempt.Model` records the model that answered.
    - When every model fails, it returns the last error, and the attempt records it as today.

- [ ] **Step 1: Write the failing tests.**
  - `gate`:
    - `TestOCROffStoresPhotosWithoutReading`: a fake recognizer counts calls → 0; the photo has `ReadEnabled=false`.
    - `TestTurningOCROnDoesNotReadOlderPhotos`: a photo is added with the module off; switch it on; `Start` → 0 calls.
    - `TestQueuedReadsStopWhenModuleTurnsOff`: a fake recognizer blocks on its first call; enqueue 3 photos; turn the module off; unblock → total calls 1; moves still open.
    - `TestManualEntryValidAcceptsInvalidGoesToReview`: `MSCU1234565` (valid) → `accepted`, `verified`; `MSCU1234566` → `needs_review` with that reason.
    - `TestEachReadCountsOnce`: 2 photos read → usage `ocr_read` = 2.
  - `recognition`:
    - `TestFallbackOn503`: a fake server answers 503 for model 1 and 200 for model 2 → attempt OK, `Model == "gemini-3.5-flash"`.
    - `TestFallbackOn429`.
    - `TestAllModelsFailRecordsError`: the attempt carries the error. A gate test asserts the move ends `needs_review`, not stuck in `reading`.

- [ ] **Step 2: Run them.** Run: `go test ./internal/gate/ ./internal/recognition/ -v`. Expected: FAIL.

- [ ] **Step 3: Implement** to the interfaces above.

- [ ] **Step 4: Run them.** Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -m "feat(ocr): switchable OCR module, model fallback and per-read counting"`

### Task 7: Sessions, development login, invitations and permissions

**Files:**
- Create: `internal/auth/{provider.go,dev.go,session.go,middleware.go,perm.go}` and `internal/auth/auth_test.go`
- Delete: `internal/httpapi/lockout.go` and its tests (the identity provider handles passwords); the access-code login in `handlers.go` (removed in Task 9)

**Interfaces:**
- Consumes: `db.Global`, `db.InCompany`, the SQL functions `memberships_for_user` and `accept_invitations` (Task 3).
- Produces:
  - **Identities:**
    - `type Identity struct{ Subject, Email, Name string; EmailVerified bool }`.
    - `type Provider interface{ LoginURL(state string) string; Exchange(ctx context.Context, code string) (Identity, error) }`.
    - `func NewDevProvider() Provider`: its login page posts an email; the identity is `Subject "dev|"+email`, `EmailVerified=true`.
  - **Sessions:** `type Sessions struct{…}` with `func NewSessions(pool *pgxpool.Pool, ttl time.Duration) *Sessions` (ttl default 30 days) and:
    - `Login(ctx, id Identity) (cookieValue string, err error)`:
      1. upserts the user by subject;
      2. normalises the email (trim + lower);
      3. calls `accept_invitations` only if `EmailVerified`;
      4. creates the session, storing only `sha256(cookie)`.
    - `Resolve(ctx, cookieValue) (Current, error)`.
    - `SetScope(ctx, cookieValue, companyID, depotID string) error`: rejects a company or depot the user isn't a member of.
    - `Logout(ctx, cookieValue) error`.
  - **The current user:**
    - `type Current struct{ User User; Memberships []Membership; Scope *tenancy.Scope; PlatformAdmin bool }`.
    - `Scope` is filled only when the session has a company the user is still a member of. The depot must be in `depot_ids`, or `depot_ids` is null.
    - The role comes from the membership at request time.
  - **Middleware:** `func (s *Sessions) Middleware(next http.Handler) http.Handler`.
    - It puts `Current` and, when present, the scope into the context.
    - Without a valid cookie it answers 401 `E4010` for `/api/*`, except `/api/health`.
  - **Permissions:**
    - `type Perm string` with constants `PermGate`, `PermOffice`, `PermManage`, `PermOwner`.
    - `func Can(r tenancy.Role, p Perm) bool`, matrix:
      - `owner` → all;
      - `manager` → Gate, Office, Manage;
      - `office` → Gate, Office;
      - `gate` → Gate.
    - `func Require(p Perm, next http.Handler) http.Handler` → 403 `E4030` when the role can't, 409 `E4090` "choose a company" when there is no scope.
  - **Startup guard:** `func CheckDevLogin(env string, devLogin bool) error` → an error when `env == "production" && devLogin`.

- [ ] **Step 1: Write the failing tests:**
  - `TestInvitationMatchesNormalisedVerifiedEmailOnly`:
    - an invitation for `ali@example.com`;
    - `Login` with `" Ali@Example.COM "` verified → a member;
    - another invitation, then `Login` with an unverified matching email → not a member.
  - `TestRemovedMemberLosesAccessOnNextRequest`:
    - a session with scope A, request OK;
    - delete the membership;
    - next request through `Middleware` + `Require(PermGate)` → 409, because the scope is now empty.
  - `TestDemotedMemberLosesOwnerRights`: change the role to `gate`; `Require(PermOwner)` → 403.
  - `TestSetScopeRejectsForeignCompanyAndDepot`.
  - `TestSessionCookieIsStoredHashed`: the `sessions` table never contains the cookie value.
  - `TestCanMatrix`: table-driven over all 4 roles × 4 perms.
  - `TestDevLoginRefusedInProduction`.

- [ ] **Step 2: Run them.** Run: `go test ./internal/auth/ -v`. Expected: FAIL.

- [ ] **Step 3: Implement.** The cookie is `depot_session`: `HttpOnly`, `SameSite=Lax`, `Secure` when the request is HTTPS. The value is 32 random bytes in base64url.

- [ ] **Step 4: Run them.** Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -m "feat(auth): sessions, development login, invitations and role permissions"`

### Task 8: OpenAPI description and generated server and client

**Files:**
- Create:
  - `api/openapi.yaml`;
  - `internal/api/generate.go` (`//go:generate go tool oapi-codegen -config cfg.yaml ../../api/openapi.yaml`);
  - `internal/api/cfg.yaml`, `internal/api/gen.go` (generated);
  - `web/orval.config.ts`, `web/src/gen/` (generated), `web/src/http.ts` (Orval mutator).
- Modify: `go.mod` (`go get -tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest`), `web/package.json` (dev dep `orval`; script `"gen": "orval --config orval.config.ts"`), `scripts/check.sh` (drift check as in Task 4)

**Interfaces:**
- Produces:
  - **The generated Go server:** `api.StrictServerInterface`, with `std-http-server: true` and `strict-server: true`, models in package `api`.
  - **The generated TypeScript client:** Orval output `client: 'fetch'`, one function per operation plus types, in `web/src/gen/api.ts`.
  - **`web/src/http.ts`:**
    - adds header `X-Depot-Client: web` on non-GET requests;
    - sends credentials;
    - throws `ApiError{status, code, message}` on non-2xx;
    - on 401, calls `onUnauthorized` (set by the app).
- Operations in `openapi.yaml` (operationIds in parentheses). Every error is the existing shape `{code, message}`.
  - **Today's routes, same paths and shapes:**
    - `GET /api/health` (getHealth)
    - `POST /api/moves` (createMove)
    - `GET /api/moves` (listMoves)
    - `GET /api/moves/{id}` (getMove)
    - `POST /api/moves/{id}/photos` (addPhoto, multipart)
    - `POST /api/moves/{id}/resolve` (resolveMove)
    - `POST /api/moves/{id}/reread` (rereadMove)
    - `GET /api/validate/{number}` (validateNumber)
    - `POST /api/test-read` (testRead, multipart)
  - **New, manual entry:** `POST /api/moves/{id}/number` (enterNumber, body `{number}`).
  - **Session:**
    - `GET /api/session` (getSession)
    - `POST /api/session/scope` (setScope `{companyId, depotId}`)
    - `POST /api/session/logout` (logout)
  - **Company:**
    - `GET /api/company` (getCompany: name, country, modules)
    - `PATCH /api/company/modules` (setModules `{modules: string[]}`, owner)
  - **Depots:**
    - `GET /api/depots` (listDepots)
    - `POST /api/depots` (createDepot, owner)
  - **Members and invitations:**
    - `GET /api/members` (listMembers)
    - `PATCH /api/members/{userId}` (updateMember `{role, depotIds}`, owner)
    - `DELETE /api/members/{userId}` (removeMember, owner)
    - `GET /api/invitations` (listInvitations)
    - `POST /api/invitations` (createInvitation `{email, role, depotIds}`, owner)
    - `DELETE /api/invitations/{id}` (deleteInvitation, owner)
  - **Lists**, for each of `lines`, `customers`, `hauliers`:
    - `GET /api/{res}?q=&page=&perPage=` → `{items, total}`
    - `POST /api/{res}`
    - `PATCH /api/{res}/{id}`
    - `DELETE /api/{res}/{id}`, which sets `active=false`
    - operationIds `listLines`, `createLine`, `updateLine`, `deactivateLine` (and the same for Customers and Hauliers).
  - **Reference lookups:**
    - `GET /api/ref/liners?q=` (searchLiners, at most 20 results)
    - `GET /api/ref/locodes?q=` (searchLocodes, at most 20 results)
  - **Platform admin:**
    - `GET /api/platform/companies` (listCompanies)
    - `POST /api/platform/companies` (createCompany `{name, country, ownerEmail, modules}`)
  - **Stay hand-written and not in the spec:** `GET /api/stream` (SSE), `GET /api/blobs/{digest}`, `/api/camera*`, `/api/live/ws` and `/auth/*`.

- [ ] **Step 1: Write `api/openapi.yaml`** with the operations above. Run `bash scripts/gen.sh`. Expected: `internal/api/gen.go` and `web/src/gen/api.ts` are created, and `go build ./internal/api/` succeeds.

- [ ] **Step 2: Write the failing test** `TestSpecMatchesGeneratedCode`: running `bash scripts/gen.sh` then `git diff --exit-code` exits 0. This is the drift check in `check.sh`, so it fails when `gen.go` is hand-edited. Verify by running `check.sh` after deliberately editing `gen.go`. Expected: FAIL. Revert the edit.

- [ ] **Step 3: Commit.** `git commit -m "feat(api): OpenAPI description with generated Go server and TypeScript client"`

### Task 9: HTTP server on the generated interface

**Files:**
- Create:
  - `internal/httpapi/strict.go` (implements `api.StrictServerInterface`);
  - `internal/httpapi/lists.go`, `internal/httpapi/members.go`, `internal/httpapi/platform.go`;
  - `internal/platform/platform.go` (+ test).
- Modify:
  - `internal/httpapi/server.go`: `Options` loses `AccessCode` and gains `Sessions *auth.Sessions`, `Provider auth.Provider`, `Pool *pgxpool.Pool`, `Hub bool`;
  - `handlers.go`, `broker.go`, `camera.go` and all tests.
- Delete: the access-code login handler and `requireAccess`.

**Interfaces:**
- Consumes: `auth` (Task 7), `api` (Task 8), `store` (Task 4), `gate.Service.ManualEntry` (Task 6).
- Produces:
  - **`platform`:** `func CreateCompany(ctx context.Context, pool *pgxpool.Pool, name, country, ownerEmail string, modules []string) (companyID string, err error)`.
    - Inserts the company via `db.Global`.
    - Under that company's scope, inserts a default depot `main` and an owner invitation for the lower-cased `ownerEmail`.
    - `func ListCompanies(ctx, pool) ([]Company, error)`.
    - Both are refused unless `Current.PlatformAdmin`.
  - **The broker:** `Broker.Publish(companyID, moveID string)`. A subscriber registers with its company, and `stream` only sends that company's events.
    - It is wired to `gate.New`'s `notify func(companyID, moveID string)` from Task 5.
  - **`/auth/login`** redirects to `Provider.LoginURL(state)`:
    - `state` is a random value stored in a short-lived cookie;
    - `/auth/callback?code=&state=` checks the state, calls `Exchange`, then `Sessions.Login`, sets the cookie and redirects to `/`.
  - **Dev mode:** `GET /auth/dev` shows a one-field email form and `POST /auth/dev` logs in. Both are mounted only when the provider is the dev provider.
  - **Permissions per route:**
    - **PermGate:** health (no auth needed), moves, photos, resolve, enterNumber, validate, session, `GET` lists and refs, stream, blobs.
    - **PermOffice:** reread, testRead, list writes.
    - **PermManage:** camera routes.
    - **PermOwner:** modules, depots POST, members, invitations.
    - **Platform admin only:** `/api/platform/*`.
  - **Blobs:** `GET /api/blobs/{digest}` serves the bytes only if `store.PhotoByDigest` finds the digest in the current company; otherwise 404.
  - **Camera routes** are mounted only when `Options.Hub` is true. Otherwise `GET /api/camera` answers `{"available": false, "reason": "needs_onsite_box"}`.

- [ ] **Step 1: Port the httpapi tests** to a helper that builds the server with pgtest, the dev provider and a logged-in session per role. Add:
  - `TestStreamOnlyCarriesOwnCompany`: two SSE clients in A and B; a move created in B → only B's client receives it within 2 s; A receives nothing.
  - `TestBlobOfOtherCompanyIs404`: B uploads a photo; A requests its digest → 404; B → 200.
  - `TestGateRoleCannotManageMembers`: role gate, `POST /api/invitations` → 403 `E4030`.
  - `TestListsCrudAndDeactivate`: as office, create a line `MSC`; list it; patch the name; delete → `active=false`; `?q=` finds it.
  - `TestDuplicateLineCodeIs409`.
  - `TestPlatformCreateCompanyInvitesOwner`: the platform admin creates a company with owner `boss@x.com`; dev login as `boss@x.com` → a member with role owner, `GET /api/depots` → `main`.
  - `TestNonAdminCannotListCompanies` → 403.
  - `TestEnterNumberOnMove`: `POST /api/moves/{id}/number` with a valid number → `accepted`.
  - `TestUnauthenticatedApiIs401`.
  - `TestCameraUnavailableOutsideHub`.

- [ ] **Step 2: Run them.** Run: `go test ./internal/httpapi/ ./internal/platform/ -v`. Expected: FAIL.

- [ ] **Step 3: Implement** to the interfaces above.

- [ ] **Step 4: Run them.** Expected: PASS.

- [ ] **Step 5: Commit.** `git commit -m "feat(httpapi): session-based API on the generated server, company-scoped stream and blobs"`

### Task 10: Reference data (SMDG liners, UN/LOCODE)

**Files:**
- Create:
  - `internal/refdata/refdata.go`, `internal/refdata/refdata_test.go`;
  - `internal/refdata/data/smdg_liners.csv`, `internal/refdata/data/unlocode.csv`;
  - `internal/refdata/README.md`.
- Modify: `internal/db/db.go` (`Migrate` calls `refdata.Load` after SQL migrations)

**Interfaces:**
- Produces:
  - `func Load(ctx context.Context, tx pgx.Tx) error`:
    - reads the embedded CSVs;
    - replaces `ref_smdg_liners` / `ref_locodes` in one transaction when `ref_versions` differs from the embedded version;
    - does nothing when they're equal.
  - The versions are constants `SMDGVersion` and `LocodeVersion`.

- [ ] **Step 1: Fetch the sources.**
  - The SMDG Liner Code List (CSV or Excel) from smdg.org (current release, e.g. 20260106). Convert it to `code,name`.
  - The UN/LOCODE CSV release from unece.org. Keep only `country,location,name,function`, with code = country+location.
  - Record source URLs, release names and the terms of use in `README.md`.
  - If either licence forbids bundling, stop and ask the owner before committing that file.

- [ ] **Step 2: Write the failing tests:**
  - `TestLoadIsIdempotent`: twice → same counts, `ref_versions` set.
  - `TestKnownCodesPresent`: liner `MSC` and LOCODE `TRMER` (Mersin) exist.
  - `TestSearchLiners`: `searchLiners?q=maer` returns Maersk first. This search goes through the API test helper from Task 9.

- [ ] **Step 3: Run them.** Run: `go test ./internal/refdata/ ./internal/httpapi/ -run 'Load|Known|SearchLiners' -v`. Expected: FAIL.

- [ ] **Step 4: Implement** `Load` (using `COPY FROM` via `pgx.CopyFrom`) and the two search handlers. Use `ILIKE` on code and name, codes first, limit 20.

- [ ] **Step 5: Run them.** Expected: PASS.

- [ ] **Step 6: Commit.** `git commit -m "feat(refdata): SMDG liner codes and UN/LOCODE seeded on migrate"`

### Task 11: Program modes

**Files:**
- Modify: `cmd/depotd/main.go` (+ `main_test.go`), `docs/runbooks/local-test-kit.md`

**Interfaces:**
- Consumes: everything above.
- Produces (commands):
  - **`depotd migrate`:**
    - `-db-owner` (env `DATABASE_OWNER_URL`)
    - `-app-password` (env `DATABASE_APP_PASSWORD`)
    - `-platform-admin` (email)
  - **`depotd api`:**
    - `-db` (env `DATABASE_URL`, the `depot_app` URL)
    - `-addr :8080`
    - `-env development|production` (default `development`)
    - `-dev-login` (default true when the env is development)
    - `-blobs dir` (default `depot-data/blobs`)
    - engine flags as today
    - `-fallback-models` (comma list)

    It refuses to start via `auth.CheckDevLogin`.
  - **`depotd hub`:**
    - `-data dir`; starts embedded PostgreSQL with data in `<data>/pg`;
    - runs `Migrate`;
    - ensures company `local` (modules `{ocr}`), depot `own-depot` and a membership for the dev email `-dev-email` (default `owner@local`) as owner;
    - then serves like `api` with dev login, camera support (`Options.Hub=true`), `-fake-camera` and `-fake-camera-photos` as today.
  - **`depotd bench` / `export-dataset`:** `-db` plus `-company` (required), scope set from them.
  - `version` as today.

- [ ] **Step 1: Write the failing tests** in `main_test.go`:
  - `TestApiRefusesDevLoginInProduction`: run `run([]string{"api","-env","production","-dev-login"})` → an error that mentions `dev login`.
  - Keep `TestLoadStills`.

- [ ] **Step 2: Run them.** Run: `go test ./cmd/depotd/ -v`. Expected: FAIL.

- [ ] **Step 3: Implement** the modes. Update the runbook: how to run `hub` locally, how to run `migrate` + `api` against a database, and that the depot keeps running `depot-pc-v1`.

- [ ] **Step 4: Run them.** Expected: PASS. Then start `depotd hub -data dev-data -addr 127.0.0.1:18093 -engine fake` from **bash** (not PowerShell, which cannot reach the internet from here). Check:
  - `curl -s http://127.0.0.1:18093/api/health` → `"ok":true`;
  - `curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:18093/api/moves` → `401`.

  Stop the hub.

- [ ] **Step 5: Commit.** `git commit -m "feat(depotd): api, migrate and hub modes on PostgreSQL"`

### Task 12: Web app on the generated client, with login and admin screens

**Files:**
- Create:
  - `web/src/admin/dataProvider.ts`
  - `web/src/admin/ListPage.tsx`: one generic list/edit page for `lines`, `customers` and `hauliers`
  - `web/src/admin/Members.tsx`, `web/src/admin/Company.tsx`, `web/src/admin/Companies.tsx`
  - `web/src/session.tsx`: session context and company/depot switcher
  - `web/src/hubApi.ts`: the camera calls that stay hand-written
- Modify:
  - `web/src/App.tsx`: routes `#/admin/lines`, `#/admin/customers`, `#/admin/hauliers`, `#/admin/members`, `#/admin/company`, `#/platform/companies`; nav by permission and module
  - `web/src/pages/*.tsx`: use `web/src/gen/api.ts` instead of `web/src/api.ts`
  - `web/src/pages/Login.tsx`: dev email form or a "Sign in" button to `/auth/login`; a "no membership" message
  - `web/src/pages/Capture.tsx`: when module `ocr` is off, a required number field with live check-digit feedback; after the photos it calls `enterNumber`
  - `web/src/i18n.ts`: EN and TR strings
  - `web/package.json`: add `@refinedev/core`; Refine brings `@tanstack/react-query`
- Delete: `web/src/api.ts`, once nothing imports it

**Interfaces:**
- Consumes: the generated client and `web/src/http.ts` (Task 8); the API (Task 9).
- Produces:
  - **`dataProvider`:** a Refine `DataProvider` for resources `lines`, `customers`, `hauliers`, `members`, `invitations`. `getList` maps to `GET /api/{res}?page&perPage&q` → `{data: items, total}`. `create`, `update` and `deleteOne` map to POST, PATCH and DELETE.
  - **The `Refine` component** wraps only the admin routes, with `dataProvider` and no router provider. The existing hash routing stays.
  - **Screens** use the Depot Gate tokens and components. Refine core hooks only (`useList`, `useCreate`, `useUpdate`, `useDelete`), no UI kit.

- [ ] **Step 1: Point the existing pages** at the generated client and delete `api.ts`. Run: `npm --prefix web run build`. Expected: PASS with no reference to `./api` left (`grep -r "from \"./api\"\|from \"../api\"" web/src` → nothing).

- [ ] **Step 2: Build the session, login, switcher and admin screens.** The rail shows:
  - Admin (office and up): Lines, Customers, Hauliers;
  - Members and Company (owner);
  - Companies (platform admin);
  - gate pages per permission;
  - Review queue, Live gate and Test read only when `ocr` is on.

  Phone capture is always shown.

- [ ] **Step 3: Verify in the browser** against `depotd hub` on 127.0.0.1:18093 (Task 11):
  1. Dev login as `owner@local`.
  2. Create line `MSC` (name autocompleted from SMDG), customer, haulier.
  3. Invite `clerk@local` as gate.
  4. Log in as clerk → only Phone capture and Live gate visible.
  5. As owner, switch `ocr` off → Phone capture asks for the number; `MSCU1234565` → accepted.
  6. Day and night themes; TR language.

  Save screenshots to `.impeccable/review/stage0-*.jpg`.

- [ ] **Step 4: Run the checks.** Run: `bash scripts/check.sh`. Expected: `CHECK OK`.

- [ ] **Step 5: Commit.** `git commit -m "feat(web): login, company switcher and admin lists on the generated client"`

### Task 13: Final check and hand-over

**Files:**
- Modify: `docs/superpowers/specs/2026-11-16-stage-0-foundation-design.md`. Add a short "Built" note listing deviations: no BYPASSRLS role (SECURITY DEFINER functions instead), WorkOS deferred to 0b, sqlc or its fallback, lockout removed.

- [ ] **Step 1:** Run `bash scripts/check.sh`. Expected: `CHECK OK`.
- [ ] **Step 2:** Run `grep -rn "modernc.org/sqlite\|store.Open(\|AccessCode" --include=*.go .`. Expected: no matches.
- [ ] **Step 3:** Commit the doc note. Leave the branch `stage-0-foundation` unmerged for the whole-branch review.
