# Stage 1 Gate and Stock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A depot records every gate in and out on a phone (also offline), with orders, holds, the block list, damage photos, the trucker's signature and a shareable EIR, and always knows its stock.

**Architecture:**
- **New package `internal/yard`** holds orders, visits, holds, the block list, the gate rules (pure functions) and the gate-save transaction.
- **The existing OCR `gate` service is unchanged.** It still creates moves and stores photos. The clerk's form is saved on that same move row through `yard`.
- **Phones keep an IndexedDB outbox,** replayed with client-generated ids, so a resend is harmless.
- **The EIR** is a server-rendered page behind an expiring token.

**Tech Stack:** Go 1.27, PostgreSQL with RLS (Stage 0 rules), sqlc, OpenAPI with oapi-codegen and Orval, React 19 + TypeScript 7 + Vite 8, Refine core, `vite-plugin-pwa` (new dev dependency, MIT).

**Spec:** `docs/superpowers/specs/2026-11-17-stage-1-gate-and-stock-design.md` (approved 17 Nov 2026; section 3.8 holds the settled rules and the owner decisions).

## Global Constraints

- **Stage 0 rules hold** (see `CLAUDE.md`):
  - every company table has `company_id` and RLS;
  - company and depot come from the session;
  - generated code is never hand-edited;
  - `bash scripts/check.sh` prints `CHECK OK` before work is done.
- **The gate never stops.** A gate move is always stored. Rule conflicts flag the move for the office; they never drop it.
- **The block list can never be passed at the gate,** under any refusal setting.
- **Refusal mode** is a company setting with three values:
  - `clerk_override` (default): the clerk passes with a reason, and the move is flagged.
  - `manager_approval`: a manager approves first; online only.
  - `none`.
- **Quantity order items** match a container when the ISO type codes share the same size, height and type-group letter: characters 1, 2 and 3 of the code. For example 45G0 and 45G1 match, but 45R1 does not match 45G1.
- **Business rules are settings, never code branches per customer.** The owner is interviewing partner depots, and the rules may change.
- **Damage and signature photos** (roles `damage`, `signature`) are never sent to OCR.
- **Language:** English and Turkish strings for every new screen. Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **This PC:** Go is at `C:\Program Files\Go\bin`, not on PATH. Start servers from Git Bash (PowerShell has no internet). Write files with LF line endings.

## Review Focus

1. **A release cancelled while a phone is offline.** A gate-out saved offline is synced after its release order was cancelled. Expected: the move is stored, the visit is closed, and the move is flagged "order no longer open". Test: Task 3 `TestOfflineGateOutAfterOrderCancelledIsFlaggedNotLost`.
2. **The same container gated in at two depots.** A gate-in happens at depot d2 while the container is in the yard at d1 of the same company. Expected: d1's visit is closed as "missed gate-out", d2's opens, and the move is flagged. Test: Task 3 `TestGateInOfNumberInYardElsewhereClosesOldVisit`.
3. **A replayed outbox.** The move was created and its photos uploaded, but the form save timed out. The whole outbox is replayed. Expected: one move, no duplicate photos, the form applied once, and the order item used once. Test: Task 3 `TestSaveGateMoveIsIdempotent`; Task 7 Playwright `offline gate-in synced later`.
4. **An EIR link to a corrected or voided move.** Expected: the page shows the current details with a "corrected" or "voided" banner, and nothing from another company or another move. Test: Task 4 `TestEIRShowsCorrectionAndVoidBanner`.
5. **A Turkish Excel export as the import file.** It is CSV with `;` separators, a UTF-8 BOM and dates written `31.12.2026`. Expected: it imports correctly. Test: Task 6 `TestImportTurkishExcelCSV`.

---

### Task 1: Schema 002 and the yard store

**Files:**
- Create:
  - `internal/db/migrations/002_yard.sql`
  - `internal/store/queries/yard.sql`
  - `internal/yard/store.go`
  - `internal/yard/store_test.go` (with `TestMain` using `pgtest.Main`)
- Modify: `internal/db/db_test.go` (the definer-function count rises from 3 to 4)

**Interfaces:**
- **Produces, tables** (all company data: `company_id`, RLS policy `company_isolation`, composite foreign keys):
  - `release_orders(company_id, id, depot_id, kind 'release'|'redelivery', reference, line_id NULL, customer_id NULL, valid_until DATE NULL, note, status 'open'|'done'|'cancelled', created_by, created_at)`
  - `order_items(company_id, id, order_id, number NULL, type_code NULL, full_empty 'E'|'F', quantity INT >=1, used INT >=0 CHECK used<=quantity)`. Exactly one of `number` and `type_code` is set; `quantity` is 1 when `number` is set.
  - `visits(company_id, id, depot_id, number, type_code, line_id NULL, customer_id NULL, status 'in_yard'|'out', full_empty, grade '' |'A'|'B'|'C'|'FOOD', block, row_no, tier, in_move_id NULL, out_move_id NULL, in_at, out_at NULL, imported BOOL, close_reason)`, plus a unique partial index on `(company_id, number) WHERE status='in_yard'`.
  - `holds(company_id, id, visit_id, reason, placed_by, placed_at, released_by, released_at NULL)`
  - `block_list(company_id, id, kind 'container'|'line', value, reason, active BOOL, created_by, created_at)`
  - `damages(company_id, id, move_id, location, description, photo_ids TEXT[] CHECK 1..3)`
  - `gate_flags(company_id, id, move_id, code, detail, resolved_by, resolved_at NULL)`
  - `approvals(company_id, id, move_id, reasons JSONB, requested_by, requested_at, status 'pending'|'approved'|'denied', decided_by, decided_at)`
  - `eir_tokens(company_id, id, move_id, token_hash UNIQUE, expires_at, created_by)`
  - `company_settings(company_id PK, refusal_mode DEFAULT 'clerk_override', walkins_allowed DEFAULT true, eir_ttl_days DEFAULT 30)`
  - `line_prefixes(company_id, prefix CHAR(4), line_id, PK(company_id, prefix))`
- **Produces, columns on `gate_moves`:** `visit_id`, `order_id`, `order_item_id`, `truck_plate`, `trailer_plate`, `driver_name`, `haulier_id`, `seal`, `full_empty`, `condition 'sound'|'damaged'|''`, `signature_photo_id`, `form_saved_at NULL`, `voided BOOL DEFAULT false`, `void_reason`.
- **Produces, function:** SECURITY DEFINER `eir_lookup(p_hash text) RETURNS TABLE(company_id text, move_id text)`. It returns a row only while `expires_at > now()`. It follows the Stage 0 definer pattern: `search_path public, pg_temp`; EXECUTE revoked from PUBLIC and granted to `depot_app`.
- **Produces, Go API:** `yard.NewStore(pool *pgxpool.Pool) *Store`, with typed CRUD for each table above. Every method takes its scope from ctx, as the Stage 0 store does. An empty depot means all depots for reads; writes that create depot-owned rows need a depot.
- **Grants:** `depot_app` gets the usual company-table rights on the new tables.

- [ ] **Step 1: Write failing tests.**
  - `TestYardTablesRespectCompany`: for each new table, rows of company A are invisible to B, and a write by id from B fails.
  - `TestOneOpenVisitPerNumber`: a second `in_yard` visit for the same number in a company violates the index.
  - `TestOrderItemShape`: an item with both `number` and `type_code`, or with neither, is rejected; `used > quantity` is rejected.
  - `TestEIRLookupOnlyLiveTokens`: an expired token gives no row; a live one gives exactly its company and move.

  Also extend the db package's `TestSecurityInvariants` count to 4.
- [ ] **Step 2: Run them.** `go test ./internal/yard/ ./internal/db/ -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the migration, the queries (then `bash scripts/gen.sh`) and `store.go`.
- [ ] **Step 4: Run them.** Expected: PASS. Then run `bash scripts/check.sh`. Expected: `CHECK OK`.
- [ ] **Step 5: Commit.** `feat(yard): orders, visits, holds, block list, damages, flags, approvals, EIR tokens schema`

### Task 2: Gate rules (pure)

**Files:** Create `internal/yard/rules.go`, `internal/yard/rules_test.go`.

**Interfaces:**
- **Produces, types:**
  - `Facts{Direction string; Number, TypeCode, FullEmpty string; LineID string; Now time.Time; Orders []OrderFact; OpenVisit *VisitFact; Blocked *BlockFact; OpenHolds []HoldFact; WalkinsAllowed bool; ChosenOrderItemID string}`
  - `OrderFact{OrderID, Kind, LineID, Status string; ValidUntil *time.Time; Items []ItemFact}`
  - `ItemFact{ItemID, Number, TypeCode, FullEmpty string; Remaining int}`
  - `VisitFact{VisitID, DepotID string}`
  - `BlockFact{Kind, Value, Reason string}`
  - `HoldFact{HoldID, Reason string}`
- **Produces, function:** `func Decide(f Facts) Decision`, returning `Decision{Blocked bool; Refusals []Reason; Flags []Reason; Item *MatchedItem; Candidates []MatchedItem}`. `Reason` is `{Code, Detail string}`.
- **Codes:**
  - refusals: `blocked`, `hold_open`, `no_order`, `order_expired`
  - flags: `walkin`, `missed_gate_out`, `no_open_visit`, `order_not_open`, `item_used_up`
- **Rules** (spec 3.2 and 3.8):
  - A block-list hit sets `Blocked` and refuses.
  - Gate-out with an open hold refuses with `hold_open`.
  - Gate-out needs a release-order item. A named item matches by number; a quantity item matches by type. The type rule is the size, height and group rule from Global Constraints, and full/empty must match the item. The line must match when the order has one. An expired order (`valid_until < Now` by date) refuses with `order_expired`.
  - Several matching items and no `ChosenOrderItemID` give a `Candidates` list.
  - Gate-in may match a redelivery item. With no match it raises the `walkin` flag, or refuses with `no_order` when walk-ins are off.
  - Gate-in with `OpenVisit` flags `missed_gate_out`. Gate-out with no `OpenVisit` flags `no_open_visit`.
- **Produces, helper:** `func TypeMatches(orderCode, containerCode string) bool`, using characters 1–3 of the code.

- [ ] **Step 1: Write a table-driven test `TestDecide`.** One case per rule above, plus:
  - `45G0` order with `45G1` container → match;
  - `45R1` container with `45G1` order → no match;
  - `22G1` vs `45G1` → no match;
  - a named item beats a quantity item;
  - a used-up item (Remaining 0) never matches;
  - a block-list hit beats everything.

  Add `TestTypeMatches`.
- [ ] **Step 2: Run it.** `go test ./internal/yard/ -run 'Decide|TypeMatches' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** No database access.
- [ ] **Step 4: Run it.** Expected: PASS.
- [ ] **Step 5: Commit.** `feat(yard): gate rules`

### Task 3: Gate save, corrections, approvals

**Files:**
- Create: `internal/yard/service.go`, `internal/yard/service_test.go`
- Modify: `internal/gate/service.go` (roles `damage` and `signature` are stored but never queued for OCR; `CreateMove` accepts an optional client id)

**Interfaces:**
- **Consumes:** Task 1 store, Task 2 `Decide`, `gate.Service` (moves and photos).
- **Produces, the yard service:**
  - `yard.NewService(st *Store, moves *store.Store, notify func(companyID, kind, id string)) *Service`
  - **Inputs:**
    - `GateForm{MoveID, Direction, Number, TypeCode, FullEmpty, Seal, Condition, LineID, CustomerID, HaulierID, TruckPlate, TrailerPlate, DriverName, SignaturePhotoID string; Damages []DamageInput; ChosenOrderItemID string; Override *Override; ApprovalID string}`
    - `DamageInput{Location, Description string; PhotoIDs []string}`
    - `Override{Reason string}`
  - **Save:** `func (s *Service) SaveGateMove(ctx, f GateForm, actor string) (Result, error)`, returning `Result{Move store.Move; VisitID string; Flags []Reason; Refused *Refusal; Candidates []MatchedItem}`. It runs in one company transaction.
    1. It loads facts (orders, open visit in any depot of the company, holds, block list, settings).
    2. It calls `Decide`.
    3. It applies the refusal mode:
       - **Blocked:** refused, never overridable.
       - **Refusals with `clerk_override`:** if `f.Override` is set, the move proceeds and the refusals are recorded as flags (`override:<code>`); otherwise it returns `Refused`.
       - **`manager_approval`:** a pending `approvals` row is created (or the existing one reused) and `Refused{ApprovalID}` is returned. It proceeds when `f.ApprovalID` refers to an `approved` row for this move.
       - **`none`:** refused.
    4. When it proceeds, it updates the move's form columns and sets `form_saved_at`.
    5. Gate-in opens a visit; if another `in_yard` visit exists, that one is first closed with `close_reason='missed_gate_out'`.
    6. Gate-out closes the open visit, or creates a closed one when there is none.
    7. It adds 1 to the item's `used`.
    8. It inserts `damages` and `gate_flags`.
    9. It upserts `line_prefixes(number[0:4] → line)`.
    10. It notifies with `("move", moveID)`.
  - **Repeat calls:** calling again for a move whose `form_saved_at` is set returns the stored result without applying anything twice.
  - **Corrections:** `func (s *Service) CorrectGateMove(ctx, moveID string, f GateForm, reason, actor string) (Result, error)`. It reverses the move's visit and item effects and applies the corrected form, recording an event. `func (s *Service) VoidGateMove(ctx, moveID, reason, actor string) error` releases the item, reopens or deletes the visit as appropriate, and sets `voided`.
  - **Approvals:** `func (s *Service) DecideApproval(ctx, approvalID string, approve bool, actor string) error` notifies with `("approval", id)`.
  - **Records:** `func (s *Service) PlaceHold / ReleaseHold / SetPosition / SetGrade / UpsertBlock / SetBlockActive / CreateOrder / CancelOrder / Snapshot(ctx) (Snapshot, error)`. The snapshot holds open orders with items, the active block list, open holds, the in-yard numbers for the scope's depot, and the company's `line_prefixes` (for the line suggestion).
- **Produces, in `gate`:** `CreateMove` takes an optional `id string`. When a move with that id already exists in the company, it returns that move instead of creating one.

- [ ] **Step 1: Write failing tests (real Postgres):**
  - `TestGateInOpensVisitAndUsesRedeliveryItem`
  - `TestGateOutClosesVisitAndUsesReleaseItem`
  - `TestBlockedCannotBeOverridden`
  - `TestClerkOverrideFlagsMove`
  - `TestManagerApprovalFlow`: refused → pending approval → approved → save proceeds
  - `TestRefusalModeNone`
  - `TestSaveGateMoveIsIdempotent`: save twice → one visit, `used` incremented once
  - `TestOfflineGateOutAfterOrderCancelledIsFlaggedNotLost`: the clerk passes it with the reason "saved offline" under `clerk_override`; the move is stored with flag `order_not_open` and the visit is closed
  - `TestGateInOfNumberInYardElsewhereClosesOldVisit`
  - `TestCorrectionMovesVisitAndItem`
  - `TestVoidReleasesItem`
  - `TestDamageAndSignaturePhotosAreNeverRead` (gate package, fake recognizer, 0 calls)
  - `TestCreateMoveWithClientIDIsIdempotent` (gate package)
  - `TestSnapshotIsDepotScoped`
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them.** Expected: PASS. Then run `bash scripts/check.sh`.
- [ ] **Step 5: Commit.** `feat(yard): gate save with visits, orders, refusal modes, approvals, corrections`

### Task 4: EIR tokens and page

**Files:**
- Create:
  - `internal/yard/eir.go`
  - `internal/httpapi/eir.go`
  - `internal/httpapi/eir.html` (`html/template`, embedded, Depot Gate print styles, English and Turkish by `?lang=`)
  - `internal/httpapi/eir_test.go`

**Interfaces:**
- **Produces, tokens:** `func (s *Service) CreateEIRToken(ctx, moveID, actor string) (token string, expires time.Time, err error)`. The token is 32 random bytes in base64url, stored as a SHA-256 hash. Its life is `eir_ttl_days` from `company_settings`.
- **Produces, the page:** `GET /eir/{token}` is public, with no session and no `/api` guard.
  1. It looks the token up with `eir_lookup`.
  2. It renders the move under `tenancy.Scope{CompanyID}` with an empty depot.
  3. The page shows the depot and company name, the move details, damages with photos, the signature, and the flags it must show (a "corrected" or "voided" banner).
  4. Photos on the page are served from `/eir/{token}/photo/{photoId}`, and only photos of that move are allowed.
  5. An unknown or expired token gives 404 with no detail.
  6. It sends `Cache-Control: no-store` and `X-Robots-Tag: noindex`.

- [ ] **Step 1: Write failing tests:**
  - `TestEIRTokenOpensOnlyItsMove`
  - `TestEIRExpiredIs404`
  - `TestEIRPhotoOfOtherMoveIs404`
  - `TestEIRShowsCorrectionAndVoidBanner`
  - `TestEIRHeadersNoStoreNoIndex`
- [ ] **Step 2: Run them.** Expected: FAIL. **Step 3: Implement.** **Step 4: Run them.** Expected: PASS.
- [ ] **Step 5: Commit.** `feat(eir): shareable EIR page behind expiring tokens`

### Task 5: API for gate and yard

**Files:**
- Modify: `api/openapi.yaml`, then run `bash scripts/gen.sh`
- Create: `internal/httpapi/yard.go`, `internal/httpapi/yard_test.go`
- Modify: `internal/httpapi/strict.go` (permission table), `cmd/depotd/main.go` (wire `yard.Service` into `httpapi.Options` as `Yard *yard.Service`; the broker relays `approval` notifications)

**Interfaces (operationIds):**
- **Gate:**
  - `saveGateForm` POST `/api/moves/{id}/gate`
  - `createEirLink` POST `/api/moves/{id}/eir`
  - `gateSnapshot` GET `/api/gate/snapshot`
- **Orders:**
  - `listOrders` GET `/api/orders?kind=&status=&q=`
  - `createOrder` POST
  - `getOrder` GET `/api/orders/{id}`
  - `cancelOrder` POST `/api/orders/{id}/cancel`
- **Visits:**
  - `listVisits` GET `/api/visits?status=&lineId=&customerId=&type=&grade=&hold=&minDays=&page=&perPage=`
  - `getVisit` GET
  - `setVisitPosition` PATCH `/api/visits/{id}/position`
  - `setVisitGrade` PATCH `/api/visits/{id}/grade`
- **Holds:**
  - `placeHold` POST `/api/visits/{id}/holds`
  - `releaseHold` POST `/api/holds/{id}/release`
- **Block list:**
  - `listBlocks` GET `/api/blocks`
  - `createBlock` POST
  - `setBlockActive` PATCH `/api/blocks/{id}`
- **Approvals:**
  - `listApprovals` GET `/api/approvals?status=pending`
  - `decideApproval` POST `/api/approvals/{id}`
- **Gate issues:**
  - `listGateFlags` GET `/api/gate-flags?open=true`
  - `resolveGateFlag` POST `/api/gate-flags/{id}/resolve`
- **Corrections:**
  - `correctMove` POST `/api/moves/{id}/correct`
  - `voidMove` POST `/api/moves/{id}/void`
- **Settings:**
  - `getYardSettings` GET `/api/settings/yard`
  - `setYardSettings` PUT (owner)
- **Permissions:**
  - **gate:** saveGateForm, createEirLink, gateSnapshot, getOrder, and `GET /api/visits/{id}`.
  - **office:** everything else except approvals and settings.
  - **manage:** `decideApproval` and `listApprovals`.
  - **owner:** `setYardSettings`.
- **Responses:**
  - A refused save answers 409 with code `E4098` and a body `{refusals, approvalId?, candidates?}`.
  - A blocked one answers 409 `E4099`.

- [ ] **Step 1: Write failing tests:**
  - `TestSaveGateFormOverHTTP`: an accepted save answers 200 with a visit; a refused one 409 E4098 with the reasons; a blocked one 409 E4099.
  - `TestGateRoleCannotCancelOrders`
  - `TestSnapshotForGateRole`
  - `TestApprovalNotifiesManagersStream`
  - `TestEveryOperationHasAPermission` (existing test) covers the new operations.
- [ ] **Step 2: Run them.** Expected: FAIL. **Step 3: Implement.** **Step 4: Run them.** Expected: PASS. Run `bash scripts/check.sh`.
- [ ] **Step 5: Commit.** `feat(api): gate form, orders, visits, holds, block list, approvals, corrections`

### Task 6: Reports, CSV export and Excel/CSV import

**Files:**
- Create: `internal/yard/reports.go`, `internal/yard/importer.go` (+ tests), `internal/httpapi/export.go` (+ test)
- Modify: `api/openapi.yaml` (reports and imports)

**Interfaces:**
- **Reports** (operationIds):
  - `reportDashboard` GET `/api/reports/dashboard` → `{inToday, outToday, inYardByLine[], openHolds, openFlags}`
  - `reportAgeing` GET `/api/reports/ageing` → buckets `0-7`, `8-30`, `31-90`, `90+` by line
  - `gateLog` GET `/api/reports/gate-log?from=&to=`
- **Exports:** hand-written `GET /api/export/{name}.csv`, where `name` is one of `visits`, `gate-log`, `orders`, `ageing`. Each takes the same filters as its list. They are UTF-8 with a BOM and `,` separators, so Excel opens them directly. Office permission; company and depot scoped.
- **Imports** (operationIds):
  - `importStock` POST `/api/import/stock` (multipart `file`)
  - `importOrders` POST `/api/import/orders`
  - `importTemplate` GET `/api/import/templates/{name}.csv`
- **Importer rules:**
  - Auto-detect `;` or `,` separators; strip a BOM.
  - Dates are accepted as `YYYY-MM-DD` and `DD.MM.YYYY`.
  - Lines and customers are matched by code.
  - The whole file runs in one transaction. On any bad row, nothing is imported, and the answer is 422 `E4220` with `{rows:[{row, field, message}]}`.
- **Stock template columns:** `number, type_code, line_code, customer_code, full_empty, in_date, block, row, tier, grade`.
- **Orders template columns:** `kind, reference, line_code, customer_code, valid_until, number, type_code, full_empty, quantity`. One row is one item; rows with the same `kind` and `reference` form one order.

- [ ] **Step 1: Write failing tests:**
  - `TestImportStockCreatesImportedVisits`
  - `TestImportBadRowImportsNothing`: a bad check digit on row 3 gives 422 listing row 3, and 0 visits are created
  - `TestImportTurkishExcelCSV`: `;`, BOM, `31.12.2026`
  - `TestImportOrdersGroupsByReference`
  - `TestAgeingBuckets`
  - `TestExportVisitsCSVHasBOMAndScope`
- [ ] **Step 2–4: Fail, implement, pass.** Then run `bash scripts/check.sh`.
- [ ] **Step 5: Commit.** `feat(yard): reports, CSV export, stock and order import`

### Task 7: Gate screen on the phone, working offline

**Files:**
- Create:
  - `web/src/gate/Gate.tsx` (the flow)
  - `web/src/gate/outbox.ts` (IndexedDB queue)
  - `web/src/gate/Signature.tsx` (canvas pad to PNG)
  - `web/src/gate/snapshot.ts`
  - `web/e2e/gate.spec.ts` (Playwright, phone viewport)
- Modify:
  - `web/vite.config.ts` (`vite-plugin-pwa`, app-shell precache, API never cached)
  - `web/src/App.tsx` (route `#/gate`, the main item for the gate role)
  - `web/src/i18n.ts`
  - `web/package.json` (devDependencies `vite-plugin-pwa`, `@playwright/test`; script `"e2e"`)

**Interfaces:**
- **Consumes:** the Task 5 operations and the existing `createMove` (with a client id) and `addPhoto`.
- **Produces, the outbox:**
  - Entries are `{id (ULID), kind: 'move'|'photo'|'form', moveId, payload, tries}`.
  - It replays in order, while online and the session scope matches.
  - The `X-Depot-Scope` header comes from the entry's stored scope, so a replay never lands in another depot.
  - Any 2xx removes an entry. A 409 E4098/E4099 from a replayed form marks it "refused" and shows it to the clerk to choose override or hold. A network error retries with backoff.
- **Produces, the gate flow** (spec 3.2):
  - **Number:** typed with live check-digit feedback, or read by OCR when the `ocr` module is on (it reuses the existing capture/reading components).
  - **Status panel:** orders, open visit, hold and block messages, worked out from the snapshot when offline and from the server when online.
  - **Form:** type, full/empty, seal, condition; damages, each with 1–3 photos and a location and note; truck, trailer, driver and haulier, with the last values remembered.
  - **Signature.**
  - **Save:** goes into the outbox.
  - **Result:** an EIR link with the phone share sheet (`navigator.share`, falling back to copy-link) and print.
  - **A refused save shows the actions the company's refusal mode allows:**
    - `clerk_override`: a reason field;
    - `manager_approval`: "waiting for manager", updated live; offline it shows hold/refuse only;
    - `none`: refuse.
  - **Freshness:** the snapshot's age is shown ("lists updated 14:02").

- [ ] **Step 1: Write Playwright tests** against `depotd hub -engine fake` on 127.0.0.1:18093:
  - `offline gate-in synced later`: go offline, save a gate-in, go online; the move appears once and the visit is in the yard.
  - `gate-out refused by hold`: shows the hold reason; with `clerk_override` a reason passes it.
  - `replayed outbox makes no duplicates`.
- [ ] **Step 2: Run them.** `npm --prefix web run e2e`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them.** Expected: PASS. Run `npm --prefix web run build` and `bash scripts/check.sh`.
- [ ] **Step 5: Commit.** `feat(web): offline gate flow with signature, damages, EIR sharing`

### Task 8: Office screens

**Files:** Create `web/src/yard/{Yard.tsx, VisitDetail.tsx, Orders.tsx, OrderDetail.tsx, Blocks.tsx, Reports.tsx, Approvals.tsx, GateIssues.tsx, Import.tsx, YardSettings.tsx}`. Modify `web/src/App.tsx` (nav by permission) and `web/src/i18n.ts`.

**Interfaces:** consumes the Task 5 and 6 operations. Refine core hooks for the lists, as in Stage 0.
- **Yard:** filters, ageing colour, and a "on hold" sign.
- **Visit detail:** moves, damages, photos, EIR links, holds (place/release), position, grade, correct/void.
- **Orders:** list, detail with item usage, cancel.
- **Import:** template download; upload with a per-row error table.
- **Blocks:** the block list.
- **Reports:** dashboard tiles, ageing and gate log, each with a CSV button.
- **Approvals (manager):** live queue with photos; approve or deny.
- **Gate issues (office):** open flags; resolve with a note.
- **Yard settings (owner):** refusal mode, walk-ins, EIR link days.
- Every screen in English and Turkish, in the Depot Gate design.

- [ ] **Step 1:** Implement the screens. Run `npm --prefix web run build`. Expected: PASS.
- [ ] **Step 2:** Browser walk-through on the hub (127.0.0.1:18093, from Git Bash):
  1. Import a sample stock CSV.
  2. Create a release order (2 × 45G1).
  3. Gate-out one container on a phone viewport.
  4. Place a hold and see the gate-out refused.
  5. Switch refusal mode to `manager_approval` and approve from the Approvals screen.
  6. Open the EIR link.
  7. Export the stock CSV.

  Save screenshots to `.impeccable/review/stage1-*.jpg`.
- [ ] **Step 3:** `bash scripts/check.sh` → `CHECK OK`. Commit: `feat(web): yard, orders, block list, reports, approvals, gate issues, import, settings`.

### Task 9: Final check and notes

- [ ] **Step 1:** Run `bash scripts/check.sh` and `npm --prefix web run e2e`. Both must pass.
- [ ] **Step 2:** Add a "Built" section to the Stage 1 design listing any deviations. Update `docs/runbooks/local-test-kit.md` with the gate flow and the import templates. Commit.
