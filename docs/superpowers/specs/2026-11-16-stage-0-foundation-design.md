# Stage 0: Foundation — design

Date: 16 November 2026 · Status: draft for the owner's approval
Roadmap: `2026-11-15-depot-app-roadmap.md` (v2, committed 49fff96). Research: `C:\Users\OZGUN\reports\Faster global depot SaaS build.md`.

## 1. Intent

Turn today's single-depot program into the base of a global, multi-company cloud product, without adding depot features yet. After Stage 0, every later stage (gate and stock, signup, charges, repairs) is built on:
- PostgreSQL where the database itself keeps companies apart;
- personal logins;
- generated API code;
- automatic checks that catch mistakes before the owner sees them.

The gate OCR keeps working and becomes a **module** that can be switched on per company, ready for Stage 1 to plug into the new gate flow.

**Owner decisions behind this (16 Nov 2026):**
- The roadmap v2 is approved: cloud first, billing optional, global.
- The selling company is registered in Turkey for now.
- Start Stage 0.
- Keep the OCR module ready for later stages.

## 2. Goals, non-goals, anti-goals

**Goals**
1. **Automatic checks.**
   - One command runs every check: Go vet and tests, TypeScript typecheck, web build, generated code up to date.
   - A Claude Code hook refuses to finish a task while it fails.
   - A GitHub Actions file is ready for when the repo gets a remote.
2. **PostgreSQL replaces SQLite.**
   - Every table carries `company_id`.
   - Row-level security (RLS) means a query can only see the current company's rows.
   - The app connects as a role that is not the table owner, so RLS can't be bypassed by accident.
3. **API from one description.**
   - `api/openapi.yaml` describes every endpoint.
   - The Go server code is generated with oapi-codegen; the TypeScript client and React Query hooks with Orval.
   - Database access is generated from SQL with sqlc.
4. **People and companies.**
   - Our own tables: companies, depots, users, memberships with roles (owner, manager, office, gate).
   - Logins come from an identity provider (WorkOS AuthKit), plus a development login that works only in development.
   - The shared access code is removed.
5. **Reference lists:** shipping lines, customers and hauliers per company; plus shared lists seeded from free sources:
   - SMDG liner codes;
   - UN/LOCODE port codes;
   - the ISO 6346 size/type codes the `iso6346` package already knows.
6. **Admin screens** for those lists and for users, built with Refine on the generated client, in the existing Depot Gate design.
7. **OCR module.**
   - The `modules` list on each company switches it on.
   - With it off, nothing reads photos and no AI cost happens.
   - Automatic fallback to the next Gemini model when one is overloaded.
   - Every read counted per company, so it can be sold per read later.
8. **Cloud deployment** (part 0b, needs the owner's accounts):
   - the app on a hosting platform in an EU region;
   - managed PostgreSQL;
   - photos in Cloudflare R2;
   - errors in Sentry.

**Non-goals (later stages):**
- release orders, gate-out, stock list, EIR (Stage 1);
- signup, payments, customer portal (Stage 2);
- tariffs and charges (Stage 3);
- the offline depot PC and fixed camera in the cloud world (Stage 5).

**Anti-goals:**
- keeping SQLite and PostgreSQL side by side;
- any table of company data without `company_id` and RLS;
- hand-written API types that drift from the server;
- a login path that can reach production without the identity provider;
- breaking the gate app that runs at the owner's depot today.

## 3. Constraints and facts

- **This PC has no PostgreSQL, no Docker and no GitHub remote.**
  - **Development and tests:** use `embedded-postgres` (Go library that downloads PostgreSQL binaries once, Apache-2.0).
  - **CI:** a local check script; the GitHub Actions file runs only once a remote exists.
- **Accounts only the owner can create** (Claude never enters passwords or payment details):
  - WorkOS;
  - the hosting platform;
  - Cloudflare;
  - Sentry;
  - later the payment service.
- **What runs at the owner's depot today is the SQLite build.** The repo branches are `main` (old), `feat/local-gate-test-kit` and `feat/gate-camera`, which is newest and has everything. None are merged to `main`.
- **Size of the change:** the current store is about 660 lines (5 tables plus settings) and the HTTP API has about 20 routes. Rewriting both is a small, bounded job.
- **Cloud vs on-site camera:** in the cloud, phone photos work with OCR from day one. A fixed camera on the depot's network needs an on-site box (Stage 5), because the cloud can't reach into the depot network. The camera code stays, but it is used only by the on-site `hub` mode.
- **Turkey:** with the selling company in Turkey, Stripe's merchant-of-record service is out. Paddle or Polar acceptance must be confirmed at Checkpoint A (Stage 2 concern, not Stage 0).

## 4. How it works

### 4.1 Git and the depot's running app
1. **Fast-forward `main` to `feat/gate-camera`**, tag it `depot-pc-v1`. That is the build running at the owner's depot, and it stays untouched.
2. **Stage 0 work** goes on branch `stage-0-foundation`, merged to `main` when done.
3. **The depot keeps running `depot-pc-v1`** until Stage 1 replaces it with the cloud version.

### 4.2 Checks
- **`scripts/check.sh`** (bash, runs in Git Bash) runs, in order:
  1. `gofmt -l` must be empty;
  2. `go vet ./...` and `go test ./...` against embedded PostgreSQL;
  3. code generation, then `git diff --exit-code` on generated files;
  4. `npm --prefix web run build`, which already typechecks.
- **`CLAUDE.md` at the repo root** (short): what the product is, the check command, the never-break rules (RLS, `company_id`, generated code is never hand-edited, the gate never stops).
- **`.claude/settings.json`:** a Stop hook runs the check script and blocks finishing when it fails.
- **`.github/workflows/check.yml`:** the same script.

### 4.3 Database
- **Driver and access:** `pgx/v5` as the driver; `sqlc` (pgx output) generates query code from `internal/store/queries/*.sql`.
- **Migrations:** plain numbered SQL files embedded in the binary, applied by a small runner like today's `schema_version` approach (the `migrate` mode). No migration framework.
- **Roles:** an owner role runs migrations; an `app` role, not owner and not superuser, serves requests.
- **Every table has `company_id`** and an RLS policy `company_id = current_setting('app.company_id')::text`.
- **Every request runs in a transaction** that first sets `set_config('app.company_id', $1, true)`.
  - A missing setting matches no rows.
  - Shared reference tables (SMDG, UN/LOCODE) have no `company_id` and are read-only to `app`.
- **Tables carried over from SQLite** (same columns, types moved to PostgreSQL), rename `tenant_id` to `company_id`:
  - `gate_moves`;
  - `photos`;
  - `recognition_attempts`;
  - `events`;
  - `settings` (per company and depot).
- **New tables:**
  - `companies` (id, name, country, `modules text[]`, created_at);
  - `depots` (company_id, id, name, country, timezone);
  - `users` (id, email, name, identity-provider subject);
  - `memberships` (company_id, user_id, role, depot_ids or all);
  - `shipping_lines`, `customers`, `hauliers` (company_id, code, name, contact, active);
  - `usage_counters` (company_id, kind e.g. `ocr_read`, day, count);
  - shared `ref_smdg_liners`, `ref_locodes`.
- **Which tables have RLS (my call):**
  - **Every company data table has RLS:** gate_moves, photos, attempts, events, settings, depots, memberships, lines, customers, hauliers, usage_counters, invitations.
  - **Tables outside any company have no RLS:**
    - `companies`, `users`, `sessions`, `platform_admins`, `ref_*`;
    - `app` may read them, but writes to them only go through the auth and platform code below.
- **Two ways into the database (my call):**
  1. **The `app` pool** (RLS enforced) for everything a person does in a company, and for background jobs. Every job carries its `company_id` and opens its transaction with it set.
     - Startup recovery (unread photos, open moves) loops over companies and recovers each one inside its own company setting.
     - The live-update broker tags each event with its company and only sends it to sessions of that company.
  2. **A `system` pool** (a role with BYPASSRLS) used only by two packages, and nowhere else:
     - `auth`: login callback, session lookup, membership lookup by user;
     - `platform`: the platform-admin list of companies, creating a company.

  A test fails the build if any other package imports the system pool.
- **IDs:** ULIDs as today.
- **No data migration from the depot's SQLite.** Test data there is not worth keeping. The depot's real stock arrives by Excel import in Stage 1.

### 4.4 API and web client
- **`api/openapi.yaml`** (OpenAPI 3.1) covers every current route plus the new ones (session, me, companies/depots for platform admins, users and memberships, lines, customers, hauliers, reference lookups).
- **Server code:** `oapi-codegen` generates a strict server interface on `net/http`. Handlers implement it.
- **The live update stream** (`/api/stream`) and the live-video WebSocket stay hand-written; they don't fit request/response generation.
- **Web client:** `orval` generates the TypeScript client and React Query hooks. `web/src/api.ts` is replaced by the generated client plus a small wrapper for auth errors and the stream.
- **One command regenerates everything:** `go generate ./...` plus `npm --prefix web run gen`.

### 4.5 Logins, companies and roles
- **The identity provider only proves who someone is.** WorkOS AuthKit (hosted login page, email/password and Google/Microsoft) sends the person back to us. We create a server-side session (an HTTP-only cookie holding a random id, stored in a `sessions` table) and look up their memberships.
- **Roles are ours:**
  - owner (everything, including users and modules);
  - manager (everything except users and modules);
  - office (lists, review queue, reports);
  - gate (gate screens only).
- **Platform admin:** a `platform_admins` list (the owner of this product) can see and create companies.
  - **Seeding:** the first platform admin is set by the `-platform-admin <email>` flag when `migrate` runs.
- **Adding people (my call):**
  - An owner invites by email; this creates an `invitations` row (company, email, role, depots).
  - On login, an invitation is matched only when WorkOS reports the email as verified. The person then becomes a member and the invitation is used up.
  - A platform admin creating a company names its first owner's email, which becomes an owner invitation.
  - Someone who logs in with no membership sees "Ask your company's owner to invite you" and nothing else. Self-signup that creates a company is Stage 2.
- **The current company and depot** come from the session, never from the request body.
  - A person in several companies picks one, and the choice is stored on the session.
  - **Depot (my call):** the session also holds the current depot, chosen from a switcher. A membership lists its depots or "all".
  - **Enforcement:** depot access is enforced in app code by one helper every handler uses, and covered by tests. Company separation is the hard boundary in the database. Depot limits are a softer one inside a company.
- **Development login:** `-dev-login` shows a "pick a user" page. It is refused at startup when the configured environment is `production`.
- **Kept or removed from today:**
  - the `X-Depot-Client` header check and host guard stay;
  - the login lockout logic moves to the session login;
  - the access code is removed.

### 4.6 Reference data
- **SMDG Liner Code List and UN/LOCODE:** CSV files committed under `refdata/` with their version and source URL, loaded by a migration.
- **ISO size/type codes:** stay in the `iso6346` package.
- **Licence check before committing each file:** SMDG lists are published free; UN/LOCODE is free UNECE data. Note the terms in `refdata/README.md`.
- **Per-company lists** (lines, customers, hauliers) start empty. The shipping-line picker suggests SMDG entries.

### 4.7 Admin screens (web)
- **Refine** (headless) with React Router and the Orval client, styled with the existing Depot Gate tokens and components. No second design system.
- **Screens:**
  - users and roles;
  - shipping lines;
  - customers;
  - hauliers;
  - company settings, including modules, which only the owner role can change;
  - platform-admin companies list.
- **The existing gate pages** (Live gate, Review queue, Phone capture, Test read, Camera) keep working on the new API. When the OCR module is off they hide, except Phone capture, which still records photos.

### 4.8 OCR module
- **Switch:** `companies.modules` contains `ocr` when on. The gate service asks a `ModuleChecker` before reading. With it off, photos are stored and no AI read runs.
- **OCR off (my call; Stage 1 redesigns the gate flow properly):**
  - Phone capture asks the clerk to type the number, checked with the check digit.
  - A valid number with photos is accepted with the clerk as the one who confirmed it. An invalid one goes to the Review queue, which stays visible for this.
- **No surprise AI cost:**
  - Only photos taken while `ocr` is on are ever read; each photo records whether reading was on when it was taken.
  - Switching the module on never reads older photos, and startup recovery re-reads only photos marked for reading.
- **Model fallback:**
  - `recognition` gets a list of models, default `gemini-3.8-flash`, then `gemini-3.5-flash`;
  - an HTTP 503 or 429 tries the next one;
  - the attempt records which model answered.
- **Counting:** each AI read adds to `usage_counters` (`ocr_read`), so Stage 2 can bill per read.
- **Kept as is:**
  - the review queue;
  - check-digit rules;
  - `bench`;
  - `export-dataset` (reads PostgreSQL now);
  - the fake engine for tests.
- **Camera in the cloud:** the camera packages stay. In `api` (cloud) mode the Camera page says a fixed camera needs the on-site box. In `hub` mode it works as today.

### 4.9 Program modes
`depotd` gets `api` (cloud web + API), `migrate`, and keeps `hub` for the on-site box. In Stage 0, `hub` mode runs the same code against a local embedded PostgreSQL, so there is still only one store. It is a developer tool only (dev login, one fixed company) and is not shipped to depots until Stage 5. The owner's depot keeps running `depot-pc-v1` meanwhile. `bench` and `export-dataset` take a database URL.

### 4.10 Part 0b: cloud deployment (after the owner creates accounts)
- **Hosting:** Fly.io app plus Fly Managed PostgreSQL in Frankfurt (my call; prices unverified).
- **Photos:** Cloudflare R2. The `blob` package gets an S3-compatible backend; the local filesystem stays for development.
- **Errors:** Sentry for Go and React.
- **Secrets** (Gemini key, WorkOS keys, R2 keys, database URL): only in the hosting platform's secret store, never in git or chat.
- **Deploy:** a `Dockerfile` (multi-stage, distroless) and `fly.toml`; `migrate` runs as a release command.
- **Done when:** the owner logs in through WorkOS on the public URL, creates a shipping line, and takes a phone photo that Gemini reads.

## 5. Testing
- **Store tests** run against embedded PostgreSQL. Key cases:
  - an RLS test that proves company B can't read or write company A's rows, through every repository function;
  - a missing company setting returns nothing.
- **API tests:** each role tested against an allowed and a forbidden endpoint.
- **Existing gate, recognition, camera and HTTP tests** are ported and keep passing.
- **Model fallback tested with a fake HTTP server:** 503 → next model; 429 → next model; all fail → the attempt records the error.
- **Web:** typecheck and build. One Playwright smoke test (dev login → create shipping line → see it listed), if Playwright installs cleanly; otherwise deferred to Stage 1.

## 6. Owner actions
1. **Before 0b:** create free accounts at WorkOS, Fly.io, Cloudflare (R2) and Sentry. Claude guides; the owner enters all passwords and payment details.
2. **For Stage 1** (start gathering now): a filled EIR or gate slip, a release order, the tariff sheet, the stock Excel, three invoices, the list of lines, customers and hauliers.

## 7. Left to the builder
- Exact SQL column types, indexes and file layout inside packages.
- Refine resource wiring details.
- The order of tasks inside the plan.
- Choosing between oapi-codegen strict and standard server if strict fights the photo upload (multipart).

## 8. Calls I made (owner can overrule)
- Fly.io + Fly Managed PostgreSQL for hosting.
- Embedded PostgreSQL for development and tests.
- No data migration from the depot's SQLite.
- Server-side sessions instead of the identity provider's tokens in the browser.
- Roles: owner, manager, office, gate.
- `hub` mode moves to PostgreSQL now.
- Tag `depot-pc-v1` and fast-forward `main`.
- Two database pools: RLS-enforced `app` for everything, BYPASSRLS `system` only for login and platform admin.
- Invitations by email, matched only to a verified email; no self-signup until Stage 2.
- Current depot on the session; depot limits enforced in app code.
- With OCR off, the clerk types the number; switching OCR on never reads older photos.
- `hub` mode is a developer tool only in Stage 0.

## 9. Built (Part 0a) - deviations from this design
- No BYPASSRLS `system` role. Cross-company access goes only through SECURITY DEFINER functions: `memberships_for_user`, `accept_invitations`, `set_company_modules`.
- WorkOS is deferred to Part 0b. Only the dev identity provider exists; `api -env production` refuses to start until a real provider is configured.
- sqlc is used (pinned Windows/Linux release via `scripts/tools.sh`); the fallback was not needed.
- Wrong-password lockout is removed; the identity provider handles passwords.
- The reference-data loader ships with no bundled SMDG or UN/LOCODE data. SMDG requires written permission for commercial use and UN/LOCODE redistribution terms are unclear. The owner must obtain permission before data files are added (`internal/refdata/README.md`).
- Store depot semantics: an empty depot in scope means all depots of the company for reads; creating depot-owned rows requires a depot.
- Hub mode is a developer tool: dev login, listens on 127.0.0.1 by default; camera stills are served from the hub-only `/api/camera/stills/{digest}`.
- The session lists each company's allowed depots. The plan's ManualEntry example digits were swapped (MSCU1234566 is valid).
