# Stage 1b Line EDI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A line's release orders arrive as EDI (e-mail or upload) and become orders. Every gate move of an EDI line's container sends a CODECO. Both sides acknowledge with APERAK. Lines without EDI get a daily CSV e-mail.

**Architecture:**
- **New pure package `internal/edifact`:** reads and writes EDIFACT interchanges and builds or reads COPARN, CODECO and APERAK. It has no database.
- **New package `internal/mail`:** the `Mailer` interface, with a file driver (the default) and a Postmark driver, plus the Postmark inbound payload.
- **New package `internal/edi`:** owns its tables, inbound processing and the background worker.
  - CODECO is driven by the `events` rows the yard already writes in the gate transaction (`move.gate_saved`, `move.corrected`, `move.voided`), so `internal/yard`'s gate code is not touched.
  - Orders go through new `yard` service functions.

**Tech Stack:** Go 1.27 (standard library only for EDIFACT, MIME and HTTP), PostgreSQL with RLS, sqlc, OpenAPI with oapi-codegen and Orval, React 19 + TypeScript 7.

**Spec:** `docs/superpowers/specs/2026-11-17-stage-1b-line-edi-design.md` (approved 18 Nov 2026). Section 5.8 holds the settled rules.

## Global Constraints

- **Stage 0 and 1 rules hold** (see `CLAUDE.md`):
  - every company table has `company_id` and RLS;
  - company and depot come from the session;
  - generated code is never hand-edited (`bash scripts/gen.sh`);
  - `bash scripts/check.sh` prints `CHECK OK`.
- **The gate never waits on EDI.** No gate request touches EDI tables or the mailer. EDI runs from events in the background.
- **Every inbound file is stored raw (blob plus `edi_messages` row) before parsing.**
- **Owner rule: no accounts are created.** The Postmark driver is tested only against an `httptest` server. The file mailer is the default everywhere.
- **SMDG guides:** follow SMDG D.00B version 2.1.3 for COPARN and CODECO. Do not bundle SMDG code lists or UN/LOCODE.
  - The guide PDFs go in `docs/refs/smdg/`, which is git-ignored.
  - They are downloaded only after the owner says yes in chat.
- **Outbound character set** is `UNOA`: text is upper-cased and transliterated to ASCII (Ş→S, Ğ→G, İ→I, Ü→U, Ö→O, Ç→C), and `? ' + :` are escaped with `?`.
- **Permission:** EDI screens and endpoints use the existing `auth.PermManage` (owners and managers). This replaces the spec's "new permission `edi.manage`": the existing ranked permission already grants exactly owners and managers.
- **Language:** English and Turkish strings for every new screen. Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **This PC:**
  - Go is at `C:\Program Files\Go\bin`, not on PATH.
  - Write files with LF line endings, and write patch scripts with the Write tool, not Bash heredocs.
  - Never touch port 8080, `bin\depotd.exe` or `depot-data`.

## Review Focus

1. **EDIFACT the way lines really send it:**
   - a UNA with other separators;
   - `?'` inside free text;
   - CRLF or a newline after each `'`;
   - a trailing newline.

   Expected: parsed the same. Test: Task 2, `TestParseVariants`.
2. **Turkish names in CODECO.** For example the driver "ŞÜKRÜ ÇİFTÇİ" and a seal "A'B". Expected: valid UNOA (`SUKRU CIFTCI`, `A?'B`). Test: Task 2, `TestCODECOTransliteratesAndEscapes`.
3. **The same COPARN e-mailed twice and also uploaded.** Expected: one order; the repeats are `ignored`. Test: Task 4, `TestSameFileTwiceMakesOneOrder`.
4. **The mail service down for hours.** Expected:
   - gate saves are unaffected;
   - messages retry at 1 min, 5 min, 30 min, then hourly;
   - a later message for the same move never overtakes an earlier one;
   - after 24 h the message is `failed`.

   Test: Task 5, `TestSenderBacksOffKeepsOrderAndFails`.
5. **A forged sender:** an allowed From address, but SPF and DKIM do not pass. Expected: stored as `unmatched` and never applied. Test: Task 4, `TestUntrustedSenderIsUnmatched`.

---

### Task 1: Schema 005 and the EDI store

**Files:**
- Create:
  - `internal/db/migrations/005_edi.sql`
  - `internal/edi/queries/edi.sql`
  - `internal/edi/store.go`
  - `internal/edi/store_test.go` (with `TestMain` using `pgtest.Main`)
- Modify:
  - `sqlc.yaml`: a third block, the same as the yard block, with `queries: internal/edi/queries` and `out: internal/edi/db`;
  - `internal/yard/queries/yard.sql`: add `FindEDIOrder`, `SetOrderSource` and `ReplaceOrderItems` helpers;
  - `internal/db/db_test.go`: the definer-function count rises from 4 to 6;
  - `.gitignore`: add `docs/refs/`.

**Interfaces:**
- **Produces, tables** (each with `company_id`, RLS policy `company_isolation`, a grant to `depot_app`, and composite foreign keys):
  - `company_edi(company_id PK, inbound_token TEXT UNIQUE NOT NULL, party_id TEXT NOT NULL DEFAULT '', next_ref BIGINT NOT NULL DEFAULT 1, events_scanned_at TIMESTAMPTZ NOT NULL DEFAULT now())`
  - `line_edi(company_id, line_id, mode TEXT CHECK (mode IN ('edi','report','off')), profile JSONB NOT NULL DEFAULT '{}', updated_at)`, primary key `(company_id, line_id)`, with a foreign key to `shipping_lines`.
  - `edi_messages(company_id, id, direction 'in'|'out', kind 'coparn'|'codeco'|'aperak'|'report'|'test'|'unknown', status, line_id NULL, depot_id NULL, move_id NULL, order_id NULL, event_id NULL, answer_of NULL, function TEXT '', control_ref TEXT '', message_ref TEXT '', content_hash TEXT '', blob_digest TEXT '', file_name TEXT '', sender TEXT '', problems JSONB '[]', tries INT 0, next_attempt_at TIMESTAMPTZ NULL, report_day DATE NULL, created_at, sent_at NULL)`
    - **Status values:** `received`, `pending`, `sent`, `accepted`, `rejected`, `failed`, `unmatched`, `error`, `done`, `ignored`.
    - **Indexes:** a unique index on `(company_id, event_id, line_id) WHERE event_id IS NOT NULL`; a unique index on `(company_id, line_id, depot_id, report_day) WHERE kind = 'report'`; and an index on `(company_id, status, next_attempt_at)`.
  - `depots`: add `edi_location TEXT NOT NULL DEFAULT ''`.
  - `release_orders`: add `source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','edi'))` and `edi_message_id TEXT NULL`. Add a unique index on `(company_id, line_id, kind, reference) WHERE source = 'edi'`.
- **Produces, definer functions** (owner-owned, `SET search_path`, `REVOKE ALL FROM PUBLIC`, `GRANT EXECUTE` to `depot_app`):
  - `edi_inbound_company(p_token TEXT) RETURNS TEXT`: the company of an inbound token.
  - `edi_companies() RETURNS SETOF TEXT`: the companies that have a `company_edi` row.
- **Produces, Go** in `internal/edi/store.go`:
  - `type Store`, `func NewStore(pool *pgxpool.Pool) *Store`.
  - `func (s *Store) run(ctx, fn func(q *edidb.Queries, sc tenancy.Scope) error) error`, the same as `yard.Store.run`.
  - `CompanyEDI(ctx) (CompanyEDI, error)`: creates the row with a random 20-character lower-case token on first use.
  - `NextRef(ctx, q) (string, error)`: returns `next_ref` zero-padded to 9 digits, then increments it.
  - `InboundCompany(ctx, token) (string, error)` and `Companies(ctx) ([]string, error)`, through the definer functions with `db.Global`.

- [ ] **Step 1: Write failing tests** in `internal/edi/store_test.go`:
  - `TestEDITablesAreCompanyIsolated`: company B sees none of company A's `edi_messages`, `line_edi` or `company_edi` rows.
  - `TestInboundTokenFindsOnlyItsCompany`: two companies, each token resolves to its own company, and an unknown token resolves to `""` with `ErrNotFound`.
  - `TestNextRefIsSequentialPerCompany`: A gives `000000001`, then `000000002`; B gives `000000001`.
  - `TestOneEDIOrderPerLineKindReference`: a second insert with `source='edi'` and the same line, kind and reference fails with a unique violation. The same pair with `source='manual'` succeeds.
- [ ] **Step 2:** Run `go test ./internal/edi/ ./internal/db/`. Expected: FAIL (no migration).
- [ ] **Step 3:** Write the migration, the queries and `store.go`, then run `bash scripts/gen.sh`.
- [ ] **Step 4:** Run `go test ./internal/edi/ ./internal/db/ ./internal/yard/`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(edi): schema 005 and EDI store`.

---

### Task 2: EDIFACT codec and the three messages (pure)

**Files:**
- Create:
  - `internal/edifact/edifact.go` (interchange read and write)
  - `internal/edifact/coparn.go`
  - `internal/edifact/codeco.go`
  - `internal/edifact/aperak.go`
  - `internal/edifact/codes.go`
  - `internal/edifact/edifact_test.go`
  - `internal/edifact/testdata/*.edi`

**Interfaces:**
- **Produces, the interchange:**
  - `type Segment struct{ Tag string; Elements [][]string }`, where an element is its components.
  - `type Message struct{ Ref, Type string; Segments []Segment }`. `Type` is the full UNH S009, for example `CODECO:D:00B:UN:SMDG21`. The association code is the one the 2.1.3 guide states.
  - `type Interchange struct{ Sender, SenderQual, Recipient, RecipientQual, ControlRef string; Prepared time.Time; Test bool; Messages []Message }`
  - `func Parse(data []byte) (Interchange, error)`: honours UNA, the release character, and CR/LF between segments.
  - `func (i Interchange) Encode() []byte`: writes UNA, then UNB, UNH, UNT and UNZ with correct counts, `UNOA:2`, and no newlines.
  - `func Text(s string) string`: transliterates and upper-cases for UNOA. Escaping happens in `Encode`.
- **Produces, messages:**
  - `type Item struct{ Number, TypeCode, FullEmpty string; Quantity int }`
  - `type Order struct{ Function, Kind, Reference, Location string; ValidUntil *time.Time; Items []Item }`
    - `Function` is `"9"` (original), `"5"` (replace) or `"1"` (cancel).
    - `Kind` is `"release"` or `"redelivery"`.
  - `type Problem struct{ Code, Text string }`
  - `func ReadCOPARN(m Message) (Order, []Problem)`
  - `type Damage struct{ Location, Description string }`
  - `type GateMove struct{ Function, Direction, DocNumber, Number, TypeCode, FullEmpty, Seal, TruckPlate, HaulierCode, Condition, OrderRef, LineCode, Location string; At time.Time; Damages []Damage }`
  - `func CODECO(ref string, g GateMove) Message`: the BGM document code is 34 for gate-in and 36 for gate-out, with the function from `g.Function`.
  - `type Answer struct{ Accepted bool; RefersTo []string; Problems []Problem }`
  - `func APERAK(ref string, a Answer) Message` and `func ReadAPERAK(m Message) Answer`. An answer is rejected when it has any ERC segment, with the FTX texts as problems.

- [ ] **Step 1: Fill in `codes.go` from the guides** (after the owner allows the download).
  - Map the COPARN BGM document-name code to a kind:
    - codes that announce equipment **leaving** the depot → `release`;
    - codes that announce equipment **arriving** → `redelivery`.
  - Write down the qualifiers used, with the guide section in a comment:
    - the RFF reference qualifiers, in order of preference;
    - the DTM qualifier for validity;
    - the LOC qualifier for the depot;
    - the EQD full or empty indicator (4 = empty, 5 = full).
  - Write `testdata/coparn_release.edi`, `coparn_redelivery_quantity.edi`, `coparn_cancel.edi`, `codeco_gate_in.edi` and `aperak_reject.edi` from the guides' examples.
- [ ] **Step 2: Write failing tests:**
  - `TestParseVariants`: the same COPARN in four spellings (default separators; a UNA with `*` for `+`; CRLF after every `'`; `?'` inside an FTX) parses to equal `Interchange` values.
  - `TestRoundTrip`: `Parse(i.Encode())` equals `i`, and UNT counts and UNZ counts are right.
  - `TestReadCOPARNRelease`:
    - `coparn_release.edi` → kind `release`, function `9`, reference as in the file;
    - a named item with quantity 1;
    - `ValidUntil` set.
  - `TestReadCOPARNQuantity`: an EQD with no number and `EQN+3` → `Item{TypeCode:"22G1", FullEmpty:"E", Quantity:3}`.
  - `TestReadCOPARNProblems`: a bad check digit and an EQD with neither number nor type both give `Problem`s naming the segment position.
  - `TestCODECOTransliteratesAndEscapes`: driver `ŞÜKRÜ ÇİFTÇİ` → `SUKRU CIFTCI`; seal `A'B` → `A?'B` in the encoded bytes.
  - `TestCODECOGolden`: a fixed `GateMove` encodes byte-for-byte to `testdata/codeco_gate_in.edi`.
  - `TestAPERAKRoundTrip`: a rejection carrying two problems reads back with `Accepted == false`, both texts, and `RefersTo`.
- [ ] **Step 3:** Run `go test ./internal/edifact/`. Expected: FAIL.
- [ ] **Step 4:** Implement. Then run `go test ./internal/edifact/`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(edifact): EDIFACT codec, COPARN, CODECO, APERAK`.

---

### Task 3: Mail drivers and the inbound payload

**Files:**
- Create:
  - `internal/mail/mail.go`
  - `internal/mail/file.go`
  - `internal/mail/postmark.go`
  - `internal/mail/mail_test.go`

**Interfaces:**
- **Produces, types:**
  - `type Attachment struct{ Name, ContentType string; Data []byte }`
  - `type Outgoing struct{ To []string; Subject, Text string; Attachments []Attachment }`
  - `type Mailer interface{ Send(ctx context.Context, m Outgoing) error; Name() string }`. `Name` returns `"file"` or `"postmark"`.
  - `type Inbound struct{ To []string; From, Subject, Text string; Trusted bool; Attachments []Attachment }`
- **Produces, constructors:**
  - `func NewFile(dir, from string) *File`: writes one RFC 5322 MIME multipart `.eml` per mail to `dir`, named `<UTC yyyymmddThhmmss>-<ulid>.eml`, using `mime/multipart` with base64 attachments.
  - `func NewPostmark(token, from, baseURL string) *Postmark`:
    - `POST {baseURL}/email` with header `X-Postmark-Server-Token` and JSON `{From, To (comma-joined), Subject, TextBody, MessageStream:"outbound", Attachments:[{Name, Content (base64), ContentType}]}`;
    - a response with `ErrorCode != 0` or a non-2xx status is an error carrying `Message`;
    - `baseURL` defaults to `https://api.postmarkapp.com`.
- **Produces, inbound parsing:**
  - `func ParsePostmarkInbound(body []byte) (Inbound, error)`: reads `FromFull.Email`, `ToFull[].Email`, `Subject`, `TextBody`, `Headers[]` and `Attachments[]` (base64 `Content`).
  - `Trusted` is true when the `Received-SPF` header value starts with `pass` (ignoring case), or an `Authentication-Results` header contains `dkim=pass`.
  - Check these field names against Postmark's public inbound webhook docs page. Reading is fine; no account.

- [ ] **Step 1: Write failing tests:**
  - `TestFileMailerWritesEML`: the `.eml` parses back with `net/mail` plus `mime/multipart`, with the same subject, `To`, text and attachment bytes.
  - `TestPostmarkSendsJSON`: an `httptest` server checks the method, path, token header and the JSON fields, including base64 attachment content.
  - `TestPostmarkErrorCode`: a 422 response with `{"ErrorCode":300,"Message":"Invalid email"}` → an error containing `Invalid email`.
  - `TestParseInboundTrust`: three payloads (SPF pass; DKIM pass only; neither) → `Trusted` true, true, false. Attachments are decoded.
- [ ] **Step 2:** Run `go test ./internal/mail/`. Expected: FAIL.
- [ ] **Step 3:** Implement. Run `go test ./internal/mail/`. Expected: PASS.
- [ ] **Step 4:** Commit `feat(mail): file and Postmark mailers, inbound payload`.

---

### Task 4: Inbound: files to orders, APERAK out

**Files:**
- Create:
  - `internal/edi/service.go`
  - `internal/edi/inbound.go`
  - `internal/edi/profile.go`
  - `internal/edi/inbound_test.go`
- Modify:
  - `internal/yard/store.go` and `internal/yard/service.go`: `Order` gains `Source` and `EDIMessageID`;
  - `internal/yard/service_test.go`.

**Interfaces:**
- **Consumes:** Task 1's `Store`, Task 2's `Parse`, `ReadCOPARN`, `APERAK` and `ReadAPERAK`, and Task 3's `Inbound` and `Mailer`.
- **Produces, yard:**
  - `func (s *Service) FindEDIOrder(ctx, lineID, kind, reference string) (Order, []OrderItem, error)` → `ErrNotFound` when there is none.
  - `func (s *Service) ReplaceOrder(ctx, id string, o Order, items []OrderItem, actor string) (kept []string, err error)` applies spec 5.8 rule 2:
    - named items match by number, quantity items by `type_code` plus `full_empty`;
    - an item that was used and is missing from the replacement is kept;
    - a quantity below `used` becomes `used`;
    - each case kept this way adds a line to `kept`, such as `"MSKU1234565 already gated"` or `"22G1 E: 2 used, kept 2"`;
    - unused items that are absent are deleted, and new items are added;
    - the order's `valid_until` is updated;
    - `status` becomes `done` when every item is used up, else `open`; a cancelled order returns `ErrOrderClosed`;
    - writes event `order.replaced` with `{kept}`.
  - `CreateOrder` stores `Source` and `EDIMessageID`.
- **Produces, edi:**
  - `type Profile struct{ SenderID, SenderQual, RecipientID, RecipientQual, LineCode string; SendTo, AllowedFrom []string; EmptiesOnly, SendAPERAK, Test bool; TypeMap [][2]string; ReportTo []string; ReportTime string }`
    - `TypeMap` holds pairs of `{ours, theirs}`.
    - `ReportTime` is `"HH:MM"`, default `"06:00"`.
  - `type LineEDI struct{ LineID, Mode string; Profile Profile }`
  - `type Config struct{ From, InboundDomain string; Tick time.Duration }`
  - `func NewService(pool *pgxpool.Pool, bl *blob.Store, y *yard.Service, m mail.Mailer, cfg Config) *Service`
  - `func (s *Service) ReceiveMail(ctx context.Context, in mail.Inbound) error`:
    - needs no scope; finds the company from the `edi-<token>@` recipient;
    - an unknown token is dropped with a log line, and `nil` is returned;
    - each attachment is handled as one file.
  - `func (s *Service) Upload(ctx, name string, data []byte, actor string) ([]Message, error)` (scoped)
  - `func (s *Service) Process(ctx, id, lineID, depotID, actor string) (Message, error)`: processes an `unmatched` or `error` message again, with the line and depot the office chose.
  - `type Message`: the API view of an `edi_messages` row, with `Problems []edifact.Problem`.
- **The file-handling algorithm** (one `in` row per file):
  1. Store the blob and insert a row with status `received`, its `content_hash` (SHA-256), the sender and the file name. Commit.
  2. Parse.
     - A parse failure → `error`, with the problem.
     - An UNH type other than COPARN or APERAK → kind `unknown`, `error`.
  3. Find the line: the UNB sender id equal to a `line_edi.profile.SenderID` with mode `edi`.
     - For mail, the From address must also be in `AllowedFrom` **and** `Trusted`.
     - Upload is trusted, because the actor is a signed-in manager.
     - No line → `unmatched`.
  4. Repeat check: another `in` row of the same line with the same `control_ref` **and** the same `content_hash` → `ignored`. The same ref with a different hash continues, adding problem `W_REPEAT_REF`.
  5. For each COPARN message, find the depot:
     - `edi_location` equal to the order's `Location`;
     - else the company's only depot;
     - else problem `E_DEPOT`, and the row is `error`.
  6. Map item types through `TypeMap` (theirs → ours), then:
     - **Function 9:** `FindEDIOrder`; if it exists, treat as a replacement, else `CreateOrder` with source `edi`.
     - **Function 5:** `ReplaceOrder`; a missing order → `CreateOrder`.
     - **Function 1:** `CancelOrder`; a missing order → problem `E_NO_ORDER`.
     - `kept` entries become problems with code `W_KEPT`.
  7. Final status: `done` (warnings allowed) or `error`. Set `order_id`.
  8. When `SendAPERAK` is on, queue an `out` `aperak` row with `answer_of` set to the in id, status `pending`, built with `NextRef`. It is accepted unless any `E_` problem exists.
  9. **APERAK in:** match each `RefersTo` against the company's `out` rows by `message_ref` or `control_ref`, and set that row `accepted` or `rejected` with the problems. The in row becomes `done`, or `unmatched` when nothing matches.

- [ ] **Step 1: Write failing tests** (pgtest, file mailer in a `t.TempDir()`):
  - `TestCOPARNCreatesOrder`: an upload of `coparn_release.edi` with the line profile `SenderID` matching → one open order, `source='edi'`, items as in the file, row `done`, and one pending `aperak` row.
  - `TestSameFileTwiceMakesOneOrder`: `ReceiveMail` (trusted) of the same file twice, plus one `Upload` → one order; the second and third rows are `ignored`.
  - `TestReplaceKeepsGatedItems`: the order is created, item 1 is gated out through `yard.SaveGateMove`, then a replacement drops item 1 → item 1 is still there with `used=1`, a `W_KEPT` problem, and APERAK accepted.
  - `TestCancelCancelsOrder` and `TestCancelUnknownIsError`.
  - `TestUntrustedSenderIsUnmatched`: an allowed From with `Trusted=false` → `unmatched`, and no order.
  - `TestMultiDepotWithoutLocationIsError`, then `Process` with a chosen depot → `done` and an order in that depot.
  - `TestAPERAKInMarksOutRejected`: an `out` codeco row with `message_ref` X plus an APERAK referring to X with an ERC → the row is `rejected` and its problems hold the FTX text.
  - `TestUnknownTokenIsDropped`: returns `nil`, and no rows in any company.
  - In `internal/yard/service_test.go`: `TestReplaceOrderRules` covers the four cases of the rule above.
- [ ] **Step 2:** Run `go test ./internal/edi/ ./internal/yard/`. Expected: FAIL.
- [ ] **Step 3:** Implement. Run `go test ./internal/edi/ ./internal/yard/`. Expected: PASS.
- [ ] **Step 4:** Commit `feat(edi): inbound COPARN and APERAK processing`.

---

### Task 5: CODECO from gate events, and the sender

**Files:**
- Create:
  - `internal/edi/outbound.go`
  - `internal/edi/worker.go`
  - `internal/edi/outbound_test.go`

**Interfaces:**
- **Consumes:** yard's `GateRecord(ctx, moveID)` (or a new `edi` query joining `gate_moves`, `visits`, `shipping_lines` and `damages`) for the move's current number, direction, type, full or empty, line, seal, plate, haulier code, condition, order reference and damages.
- **Produces:**
  - `func (s *Service) Tick(ctx context.Context) error`: one pass over `Companies()`. Each company runs in its own scope:
    1. **Queue CODECO.** Read events of kind `move.gate_saved`, `move.corrected` and `move.voided` with `occurred_at >= events_scanned_at - 5 minutes`, ordered by `seq`. Set `events_scanned_at` to the pass start time. Insert with `ON CONFLICT DO NOTHING` on `(event_id, line_id)`. Rules (spec 5.8.3):
       - **Eligible** means `line_edi.mode = 'edi'`, and not `EmptiesOnly`, or full/empty is `E`.
       - **`prev`** is this move's latest out codeco row that is not a cancel.
       - **`gate_saved`:** eligible → function 9.
       - **`corrected`:**
         - with `prev` and the same line → 5;
         - with `prev` on another line → 1 to `prev.line` (when that line is still mode `edi`) plus 9 to the new line when eligible;
         - no `prev` and eligible → 9.
       - **`voided`:** with `prev` → 1 to `prev.line`.
       - The raw file is built now: `edifact.CODECO` inside an interchange from the company's `party_id` to the profile's `RecipientID`, `Test` set from the profile, `control_ref` from `NextRef`, `message_ref` equal to `control_ref`, and `DocNumber` equal to the move id. It is stored as a blob, with status `pending` and `next_attempt_at = now`.
    2. **Send what is due** (the daily reports come in Task 6). Claim `pending` rows that are due, by `created_at`, with `FOR UPDATE SKIP LOCKED`.
       - Skip a row when an earlier row with the same `move_id` is still `pending` or `failed`.
       - Send by mail to the profile's `SendTo`, subject `"<KIND> <depot name> <container number or reference>"`, attachment `<kind>-<control_ref>.edi` of type `application/EDIFACT`.
       - Success → `sent`, with `sent_at`.
       - Error → `tries+1`. The next attempt is 1 min, 5 min, 30 min, then every hour. Once the first try is over 24 h old → `failed`.
       - An empty `SendTo` → `failed` with problem `E_NO_ADDRESS`.
  - `func (s *Service) Run(ctx context.Context)`: calls `Tick` every `cfg.Tick` (default 30 s); logs errors and never panics; returns when `ctx` ends. It also picks up `in` rows stuck in `received` for over 1 minute and processes them again.
  - `func (s *Service) Resend(ctx, id, actor string) error`: an out row becomes `pending` with `tries=0`.
  - `func (s *Service) SendTest(ctx, lineID, actor string) (Message, error)`: kind `test`, built from a made-up gate-in of container `TESU123456` plus its check digit (`iso6346.CheckDigit`), always with UNB test flag 1, linked to no move, and queued.

- [ ] **Step 1: Write failing tests** (pgtest; a fake `Mailer` that records mails and can be set to fail; a fixed clock):
  - `TestGateSavedQueuesAndSendsCODECO`: a line with mode `edi`, then a gate-in saved through `yard.SaveGateMove`, then `Tick` → one mail with one attachment that `edifact.Parse`s to a CODECO with BGM 34 and the container number. The row is `sent`.
  - `TestNonEDILineSendsNothing` and `TestEmptiesOnlySkipsFull`.
  - `TestCorrectionSendsReplacement`: correct the move on the same line → the second mail's BGM function is 5.
  - `TestCorrectionToOtherLineCancelsAndSends`: the old line gets function 1, and the new line gets 9.
  - `TestVoidSendsCancel`.
  - `TestSenderBacksOffKeepsOrderAndFails`:
    - the mailer fails, and a gate-in and a correction of the same move are queued;
    - ticks at +0, +1 min, +6 min and +36 min → only the first row is tried, with the tries counts as expected;
    - clock +25 h → the first row is `failed`, and the second is still not sent;
    - `Resend` on the first while the mailer works → both are sent, in order.
  - `TestGateWorksWhenEDIBroken`: the `edi_messages` table is locked by another transaction → `yard.SaveGateMove` still succeeds within 2 s.
  - `TestRescanIsIdempotent`: two ticks over the same events → one row per event.
- [ ] **Step 2:** Run `go test ./internal/edi/`. Expected: FAIL.
- [ ] **Step 3:** Implement. Run `go test ./internal/edi/`. Expected: PASS.
- [ ] **Step 4:** Commit `feat(edi): CODECO from gate events, sender with retries`.

---

### Task 6: Daily CSV for lines without EDI

**Files:**
- Create: `internal/edi/report.go` and `internal/edi/report_test.go`.
- Modify: `internal/edi/worker.go`, to call `reports` within `Tick`.

**Interfaces:**
- **Consumes:** yard `GateLog(ctx, from, to *time.Time, limit int)` (depot scope; rows carry `LineCode`, `Voided` and `At`), and each depot's `timezone`.
- **Produces:** `func (s *Service) reports(ctx context.Context, sc tenancy.Scope) error`.
  - **Which lines and depots:** for each line with mode `report` and a non-empty `ReportTo`, and each depot of the company.
  - **When it fires:** when depot-local now is past `ReportTime` today and there is no `report` row for that line, depot and `report_day` (= yesterday, depot-local).
  - **What it reports:** the previous full depot-local day (my call: "that day's moves" read as the day just ended, so no move falls between reports).
    - Rows are `GateLog` for that day where `LineCode` equals the line's code and the move is not voided.
  - **What it sends:**
    - A UTF-8 CSV with a BOM and `;` separators.
    - Columns: `Time` (depot-local `YYYY-MM-DD HH:MM`), `Direction`, `Container`, `Type`, `Full/Empty`, `Truck`, `Haulier`, `Seal`, `Condition`.
    - Subject `"<line code> gate moves <depot name> <YYYY-MM-DD>"`.
  - **What it records:**
    - an out row of kind `report`, status `sent` or `failed`;
    - with no moves, the row is `ignored` and no mail goes out.

- [ ] **Step 1: Write failing tests:**
  - `TestReportSentOncePerDay`: a `report` line, two moves yesterday (one voided) and one today. Clock 06:01 depot time → one mail whose CSV has 1 data row. A second `Tick` → no new mail.
  - `TestReportUsesDepotTimezone`: a depot in `Asia/Dubai` and a move at 21:30 UTC → it counts in the next Dubai day.
  - `TestNoMovesNoMail`.
- [ ] **Step 2:** Run `go test ./internal/edi/ -run Report`. Expected: FAIL.
- [ ] **Step 3:** Implement. Run `go test ./internal/edi/`. Expected: PASS.
- [ ] **Step 4:** Commit `feat(edi): daily gate move CSV for lines without EDI`.

---

### Task 7: API, webhook and depotd wiring

**Files:**
- Modify:
  - `api/openapi.yaml`;
  - `internal/httpapi/strict.go` (the permission map: every new operation is `auth.PermManage`);
  - `internal/httpapi/server.go` (options and the webhook route);
  - `cmd/depotd/main.go`.
- Create: `internal/httpapi/edi.go` and `internal/httpapi/edi_test.go`.

**Interfaces:**
- **Produces, endpoints** (strict, JSON unless stated):
  - `GET /api/edi/messages?direction&kind&status&lineId&from&to&limit&offset` → `{messages: EDIMessage[], total}`
  - `GET /api/edi/messages/{id}` → `EDIMessage`
  - `GET /api/edi/messages/{id}/raw` → `application/EDIFACT` with `Content-Disposition: attachment`
  - `POST /api/edi/messages/{id}/resend` → 204
  - `POST /api/edi/messages/{id}/process` with `{lineId?, depotId?}` → `EDIMessage`
  - `POST /api/edi/upload`: multipart `file`, repeatable, each at most 2 MiB → `{messages: EDIMessage[]}`
  - `GET /api/edi/lines` → `LineEDI[]` (every line, mode `off` when it has no row)
  - `PUT /api/edi/lines/{lineId}` with a `LineEDI` → `LineEDI`. 400 `E4000` for:
    - an address that `mail.ParseAddress` rejects;
    - a `ReportTime` not in `HH:MM`;
    - mode `edi` without `RecipientID`.
  - `POST /api/edi/lines/{lineId}/test` → `EDIMessage`
  - `GET /api/edi/status` → `{mail: "file"|"postmark", inboundAddress: string, problems: int}`
    - `inboundAddress` is `edi-<token>@<domain>`, or `""` with no domain.
    - `problems` counts rows that are `failed`, `rejected`, `unmatched` or `error` in the last 30 days.
  - `PUT /api/depots/{id}/edi-location` with `{location}` → 204 (`PermManage`).
- **Produces, the webhook:** `POST /hooks/postmark/inbound` sits outside `/api` and has no session.
  - It is mounted only when `Options.InboundSecret != ""`.
  - Auth: basic auth with user `postmark` and password equal to the secret, compared in constant time.
  - The body is capped at 40 MiB.
  - It calls `mail.ParsePostmarkInbound`, then `EDI.ReceiveMail`. The answer is 200 once it is stored, 401 for bad auth, 400 for an unparseable body, and 413 for a body over the cap.
- **Produces, depotd** (on `api` and `hub`, as flags with environment fallbacks):
  - `-mail` (`MAIL_DRIVER`, default `file`);
  - `-mail-dir` (`MAIL_DIR`, default `<data>/mail-out` for hub, and required for `api` when the driver is `file`);
  - `POSTMARK_SERVER_TOKEN` (environment only; required when the driver is `postmark`, or start-up fails with a clear error);
  - `POSTMARK_INBOUND_SECRET` and `EDI_INBOUND_DOMAIN` (environment);
  - `-mail-from` (`MAIL_FROM`, default `depot@localhost`);
  - `-edi-tick` (`EDI_TICK`, default `30s`).
  - `edi.Service.Run` is added to `serve`'s `background` list.

- [ ] **Step 1: Write failing tests** (`httptest` against `httpapi.New`):
  - `TestEDIEndpointsNeedManage`: a gate-role and an office-role user get 403 on `GET /api/edi/messages`.
  - `TestUploadThenList`: upload `coparn_release.edi` → listed as `done`, and its raw download returns the same bytes.
  - `TestWebhookAuth`: no auth → 401; a wrong password → 401; right → 200, and the message is stored in the token's company.
  - `TestWebhookNotMountedWithoutSecret`: → 404.
  - `TestLineSettingsValidation`: a bad address, a bad time, and `edi` without a recipient each give 400 `E4000`.
- [ ] **Step 2:** Run `go test ./internal/httpapi/ -run EDI\|Webhook\|LineSettings`. Expected: FAIL.
- [ ] **Step 3:** Implement. Run `bash scripts/gen.sh`, then `go test ./...`. Expected: PASS.
- [ ] **Step 4:** Commit `feat(api): EDI endpoints, Postmark inbound webhook, depotd mail config`.

---

### Task 8: EDI screens

**Files:**
- Create:
  - `web/src/yard/EDI.tsx` (the messages list, detail and upload)
  - `web/src/yard/EDILines.tsx` (the line settings form)
  - `web/e2e/edi.spec.ts`
  - `web/e2e/fixtures/coparn_release.edi` (a copy of the Task 2 testdata with the sender id `E2ELINE`)
- Modify:
  - `web/src/App.tsx` (nav item "EDI" for `PermManage`, with a problems badge from `/api/edi/status` like Gate issues);
  - `web/src/i18n.ts` (EN and TR);
  - `web/src/yard/YardSettings.tsx` (the depot EDI location field);
  - `web/e2e/hub.ts` (env `EDI_TICK=1s`, `MAIL_DIR=<data>/mail-out`).

**Interfaces:**
- **Consumes:** Task 7's generated Orval client.
- **The EDI screen:**
  - **Header:** a banner reading "E-mail not connected: files are saved, not sent" when `mail` is `file`, and the inbound address when there is one.
  - **Messages tab:**
    - filters: direction, kind, status, line, dates;
    - rows: time, direction arrow, kind, line, container or reference, status chip;
    - row detail: problems, a link to the order or move, raw text (segments split on `'` for reading), Download, Resend (out) or Process again with line and depot pickers (unmatched or error);
    - upload: a file input that takes several files.
  - **Lines tab:** one row per line with mode `off`, `edi` or `report`. The form edits the `Profile` fields, uses comma-separated address lists, and has a "Send test CODECO" button.

- [ ] **Step 1: Write the failing Playwright test** `edi.spec.ts`, `"COPARN upload becomes an order and a gate-out sends CODECO"`:
  1. Through `page.request`, create line `E2E` and `PUT /api/edi/lines/{id}` with mode `edi`, `SenderID:"E2ELINE"`, `RecipientID:"E2ELINE"` and `SendTo:["line@example.com"]`.
  2. Upload the fixture on the EDI screen → its row shows status "Done".
  3. Orders → the fixture's reference is listed.
  4. Gate the container in and out through the API, the same way as `heldInYard`.
  5. Expect a CODECO row with status "Sent" within 15 s.
  6. Expect a `.eml` file in the hub's `mail-out` folder whose attachment contains `BGM+36`.
- [ ] **Step 2:** Run `npm --prefix web run e2e`. Expected: the new test FAILS and the 8 existing tests pass.
- [ ] **Step 3:** Implement the screens and strings. Run `npm --prefix web run e2e`. Expected: 9 passed.
- [ ] **Step 4:** Run `SHOTS=1` for screenshots of the EDI list, the detail and the line form in EN and TR. Look at them.
- [ ] **Step 5:** Commit `feat(web): EDI screens and line EDI settings`.

---

### Task 9: Final check and notes

**Files:**
- Modify:
  - `docs/superpowers/specs/2026-11-17-stage-1b-line-edi-design.md`: add a section 8 "Built", one line per deviation (the `PermManage` instead of `edi.manage`, and the previous-day report);
  - `docs/runbooks/local-test-kit.md`: how to try EDI locally with the file mailer and an uploaded COPARN, and where the `.eml` files land;
  - `CLAUDE.md` "Where things are": the Stage 1b design and plan.

- [ ] **Step 1:** Run `bash scripts/check.sh`. Expected: `CHECK OK`.
- [ ] **Step 2:** Run `npm --prefix web run e2e`. Expected: 9 passed.
- [ ] **Step 3:** Commit `docs: stage 1b built notes and runbook`.
