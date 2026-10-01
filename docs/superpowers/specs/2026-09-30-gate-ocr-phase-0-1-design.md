# Depot Platform: Phase 0–1 Design (Foundation + Gate OCR, gate-in)

- **Status:** Approved in brainstorming, awaiting written-spec review
- **Date:** 2026-09-30
- **Scope of this document:** Phase 0 (platform foundation) and Phase 1 (gate-in OCR with admin and testing panel). Later phases get their own design documents.
- **Implementation plans:** one per milestone (M0–M6, §14), because Phase 0–1 is too large for a single plan.
- **Prototype this replaces:** `gate_ocr (3).py` (single-file Python/FastAPI), copied to `C:\Users\OZGUN\gate-ocr-prototype\gate_ocr.py`

---

## 1. Purpose

A single platform that runs a container depot end to end, **starting at the gate**. Its first job is to get trucks through the gate fast, with an accurate container number and **photo proof of condition** captured automatically, so damage disputes are settled by evidence, not argument. The same gate events then feed automatic reports to shipping lines (CODECO), yard inventory, repairs, billing and the owner's container sales website.

It is sold to **container depot owners, Turkey first**, as a multi-tenant subscription: **a monthly fee per depot, plus paid modules**. It must be simple enough to demo in 15 minutes and set up without us on site.

While operating, it collects verified photos and labels to **train our own OCR model**, which will run offline at the gate and replace paid cloud AI calls over time.

## 2. Market and customer facts that shape the design

| Fact | Design consequence |
|---|---|
| Buyer is usually the **depot owner** | Sales by demo; built-in demo depot; owner phone dashboard; simple pricing |
| #1 selling point: **gate speed** | Hard timing targets (§8); reading starts after the first photo; one-key accept |
| Top pains: **damage disputes**, **finding containers**, **manual reporting to lines** | Tamper-evident photo evidence from Phase 1; structured yard positions; CODECO generation in Phase 1 |
| Depots use paper/Excel, local or old software, or commercial depot software | Outgoing connectors (Excel/CSV, CODECO, webhooks, drop folder); no double entry |
| **Pre-advice usually exists** (EDI/Excel/email), but walk-ins happen | Pre-advice import is a Phase 1 module; walk-in policy per depot with per-line override |
| Partner pilot depot has **unreliable internet**, **no gate cameras**, clerks write by hand | Gate hub with offline mode in Phase 1; tablet capture in Phase 1; fixed cameras in Phase 2 |
| Staff: office has email, **gate staff often don't** | Two login methods (§11) |
| Containers: empty, laden, reefer, special (tank, flat rack, open top, OOG) | Seal numbers in the model; OCR and camera plan cover non-standard number positions |
| Markets: **Turkey first**, then worldwide | Turkish + English UI from day one; KVKK; EU cell first; e-Fatura later (billing module) |

## 3. Roadmap (one product, modules switched on per customer)

| Phase | Content |
|---|---|
| **0. Foundation** | Repo, CI/CD, infrastructure as code, logins, tenants, roles, audit, events, API skeleton, web shell, demo depot |
| **1. Gate OCR (gate-in)** | Tablet capture, gate hub with offline mode, cloud + local OCR, pre-advice, policies, exceptions and alerts, slip printing, connectors, admin and testing panel, dataset export. Designed so gate-out is a small addition. |
| 1b. Gate-out | Same flow, checked against release orders |
| 2. Fixed gates | Standard camera kit per lane, live video, evidence clips, own model in shadow mode |
| 3. Sellable | Automation levels in production, EIR variants, usage metering, onboarding at scale, status page, SMS/WhatsApp receipts |
| 4. Depot management | Yard (incl. 3D view), M&R (CEDEX, estimates), billing (e-Fatura), customer portal |
| 5. Sales integration | Stock sync and orders with the owner's sales website |

## 4. Principles

1. **Few, boring, proven parts, each with one job.** Every extra product is long-term maintenance.
2. **One product, one codebase, one version.** Never custom code per customer.
3. **Configuration instead of customization:** policies, gate steps, mappings, templates.
4. **The gate never stops because of us.** Every failure degrades to a simpler working mode.
5. **Arithmetic beats opinion.** Nothing failing its ISO 6346 check digit is ever auto-accepted.
6. **Keep what the machine saw.** Raw engine output is stored beside the resolved value, forever.
7. **Evidence over hope.** Accuracy, cost and the impact of setting changes are measured and shown, not assumed.

## 5. Technology stack (locked 2026-09-30)

| Layer | Choice |
|---|---|
| All always-running software (cloud backend **and** gate hub) | **Go**, one program `depotd` with modes |
| All screens (clerk, office, admin, testing, owner, capture app; later 3D yard and sales site) | **TypeScript + React**; capture app is an installable web app (PWA) |
| Model lab (training, datasets, evaluation) | **Python**; models exported to **ONNX** |
| Cloud database and job queue | **PostgreSQL** (managed, AWS RDS); job queue stored in Postgres (e.g. River) |
| Hub database | **SQLite**, embedded in the Go program |
| Photos and evidence | **S3** (private, versioned, replicated to a second region) |
| Hosting | **AWS**, ECS Fargate, **no Kubernetes**; one **EU cell (Frankfurt)** first |
| Logins | Managed identity service (e.g. WorkOS or Auth0) for office users |
| Model runtime | ONNX Runtime called from Go; image operations via libvips |
| Live video (Phase 2) | go2rtc on the hub; managed TURN relay for remote viewing |
| Notifications | Web push (browser and installed PWA) |
| Monitoring | OpenTelemetry to one managed backend; Sentry for errors |
| Infrastructure | Terraform; GitHub + GitHub Actions |

**Regional cells:** each region runs a complete, identical copy of the stack. A company's data lives in one cell. Scaling means adding cells. A Turkish-hosted cell is added only if customers require it.

## 6. Architecture

```
 DEPOT (LAN)                                       CLOUD: EU cell
┌──────────────────────────────┐                 ┌───────────────────────────────────────┐
│ Tablet: capture app (PWA)    │──LAN──┐         │ depotd api     screens + public API   │
│ Office PC / owner phone      │──LAN──┤         │ depotd worker  OCR, alerts, imports,  │
│                              │       ▼         │                connectors, jobs       │
│ Gate hub: depotd hub         │──outbound──────▶│ depotd migrate database upgrades      │
│  • local SQLite              │  HTTPS only     │                                       │
│  • cached pre-advice         │  (no inbound    │ PostgreSQL · S3                       │
│  • check digit + policies    │   ports)        │                                       │
│  • local OCR fallback (ONNX) │                 │ AI OCR (Gemini; backup engine later)  │
│  • serves gate + exception   │                 │ Identity · Push · (Phase 2: TURN)     │
│    screens on the LAN        │                 └───────────────────────────────────────┘
│  • slip printer, 4G backup   │
└──────────────────────────────┘
```

### 6.1 Decisions
1. **One Go program, several modes:** `api`, `worker`, `migrate`, `hub`. Core logic (validation, combining frames, policies, sync format) is written once and shared by hub and cloud.
2. **Modular monolith.** Modules each own their tables and expose an interface; they communicate through interfaces and events:
   `tenancy`, `gate`, `recognition`, `preadvice`, `policy`, `evidence`, `alerts`, `audit`, `connectors`, `dataset`, `ops`.
3. **Every screen uses the public API.** Integrations can do anything the UI can.
4. **Live updates via Server-Sent Events (SSE).**
5. **OCR engines behind one interface** (§9).
6. **Nothing at the depot accepts incoming connections.** Devices always call out.
7. **Gate hub from Phase 1** (partner internet is unreliable). The hub serves the gate screen and exception queue on the LAN during outages. Admin, setup and reports are cloud-only.
8. **Degradation ladder:** hub + cloud → hub only (offline) → tablet direct to cloud (hub down) → tablet local queue with manual entry (both down).
9. **Environments:** development, staging, production.
10. **Demo depot** with realistic generated traffic, for sales.

### 6.2 Standard pilot hardware (per gate)
Rugged Android tablet · small fanless mini-PC as hub · thermal receipt printer · 4G/5G backup router. Exact models are chosen in the implementation plan. The business model for hardware (rent, sell, or customer-bought to our spec) is decided after the pilot; the software supports all three.

## 7. Data model

### 7.1 Ground rules
- Every row has `tenant_id`; **PostgreSQL row-level security** enforces isolation.
- IDs are **ULIDs**, so the hub creates IDs offline without collisions.
- Times stored in UTC, shown in the depot's time zone.
- `gate_moves`, `photos`, `recognition_attempts` and `events` are **partitioned by month**.

### 7.2 Records

| Area | Records | Key contents |
|---|---|---|
| Tenancy | Company → Depot → Lane; User; Role; **Device** (tablet/hub, its public key, status); Module license; **Training consent** (flag, date, contract version) | Ownership and entitlements |
| Master data | Shipping line (SCAC), Haulier, Customer | Shared reference lists per company |
| Gate | **TruckVisit** (truck and trailer plate, driver, haulier, lane, arrival and departure, slip number) → one or more **GateMove** | GateMove: direction (in/out); status `capturing → reading → needs_review → accepted / refused`; container number, ISO type, color, full/empty, seals, line, booking, pre-advice link; **policy decision with reason code**; condition and damage tags; weight; structured yard position; `corrected_from`, `corrected_by`, `verified` (bool) |
| Recognition | **RecognitionAttempt** (one per engine per move) | Engine, **model version, prompt version**, raw output, candidates, number location, confidence, latency, tokens and cost, `shadow` flag |
| Evidence | **Photo**; ShareLink | SHA-256 fingerprint (taken on the device), role/side, device, resolution, storage key, **number bounding box**, label status, training eligibility, retention class; share links expire and are revocable |
| Pre-advice | Booking/Release → Item (specific number, or quantity × ISO type for a line); ImportJob; **ImportMapping** template | Expected containers and their source |
| Policy | GatePolicy (per depot) + LineOverride; **GateStepConfig** (per depot, lane, direction) | Walk-ins, automation level per lane, mismatch handling, which gate steps are on |
| Alerts | **Exception** | Reason code, assignee, escalation state and timer, resolution |
| Audit & sync | **Event** (append-only) | Actor, device, time, payload; source for audit, hub↔cloud sync and connectors |
| Future-ready | YardLocation (block/bay/row/tier), DepotLayout (coordinates); UsageCounter | 3D view later; cost and usage tracking |

### 7.3 State + events, written together
Every change writes the updated record **and** an append-only event **in one transaction**. Events drive the audit trail, hub↔cloud sync (idempotent, safe to re-deliver, as in the prototype) and outgoing connectors. Screens read current-state tables.

### 7.4 Hub database
SQLite holding the hub's working set: today's and upcoming pre-advice, recent moves, users/PIN hashes for its depot, the outgoing event queue, and photos pending upload.

## 8. Gate flow (Phase 1, tablet at the gate)

### 8.1 Steps
1. **Truck arrives.** Clerk taps *New gate-in*; the truck plate is photographed first and read by OCR.
2. **Guided photos** with on-screen frame guides: rear doors, left side, right side, seal (if laden). An on-tablet quality check asks for a retake immediately if blurry or dark. **Photos upload to the hub in the background; reading starts after the first photo.**
3. **Reading:** online, the hub sends a cropped/scaled image to cloud AI. If offline, or no cloud answer within **~4 s**, the local OCR result is used. All answers are stored.
4. **Checking:** combine frames → check digit → pre-advice match (a single uncertain character may be resolved only if exactly one expected container fits, and the result must still pass the check digit) → ISO type code validity → "already in yard?".
5. **Policy decision** with a stated reason:
   - ✅ valid + matched → green, fields prefilled, one key to accept (or auto-accept if the lane's automation level allows)
   - 🟡 valid walk-in → amber, clerk picks line/booking or refuses per policy
   - 🔴 invalid, mismatch, unreadable → red, exception to office with photos; clerk may correct if their role allows
6. **Clerk completes:** driver, full/empty, seal, **quick damage tags** on photos. Corrections are stored as `corrected_from`.
7. **Done:** slip printed with QR code (links to the digital receipt with photos); yard position suggested; events to connectors and sync.

### 8.2 Configurable gate steps
Per depot, lane and direction, admins switch steps on or off: plate photo, container photos, seal, damage tags, **full survey** (uses the M&R survey form once it exists), driver signature on tablet, print slip.

### 8.3 Timing targets (measured in the testing panel)
- Number on screen **≤ 3 s after the first photo**, online, 90th percentile.
- **< 60 s clerk time** for a matched container (median).

### 8.4 Failure behaviour

| Situation | Behaviour |
|---|---|
| Internet down | Hub runs everything locally; screen shows pre-advice freshness ("list updated 14:02"); office exceptions work on LAN. After reconnection the cloud **re-reads offline moves in the background** and flags disagreements for review. |
| Hub down, internet up | Tablet talks to the cloud directly |
| Both down | Tablet local queue with manual entry, flagged *unverified*, synced later |
| Number physically unreadable | Manual entry, flagged unverified, photos kept |
| Two 20' containers on one truck | One TruckVisit, two GateMoves, one slip |

### 8.5 Exceptions and alerts
Web push with photo and reason to office staff; exception card shows all photos, engine candidates, expected container, and one-tap actions (accept suggestion, type correct number, ask clerk/driver, refuse). Unanswered exceptions **escalate** after a configurable time (default 60 s) to the next person. In Phase 2 the card adds a live video button.

### 8.6 Receipt
Printed thermal slip with QR code (works offline) → digital receipt with condition photos. Driver signature optional per depot. SMS/WhatsApp in Phase 3.

## 9. Recognition pipeline

### 9.1 Engines (one `Recognizer` interface: photos in → candidates, ISO code, color, bounding box, per-character confidence out)
- **Cloud AI:** Gemini (owner's choice, 2026-10-01), called over plain HTTPS. A second cloud engine as automatic failover is added in a later plan behind the same interface. Use a billed Google Cloud project: on the unpaid tier, submitted content may be used to improve Google's products.
- **Local generic OCR:** PaddleOCR detection + recognition models in ONNX on the hub; offline fallback.
- **Own model:** added version by version; shadow first.
- Truck plates use the same pipeline with Turkish plate formats.

### 9.2 Escalation ladder
1. A **fast, low-cost vision model** reads the best 1–2 photos, scaled down. Done if the check digit passes and a second photo or pre-advice confirms.
2. Otherwise more photos at higher resolution go to a **stronger model**.
3. Otherwise a human decides.

Exact models are chosen by benchmarking on the golden photo set (§13), not assumed. Cloud answers must match a **strict output schema**; non-conforming answers are rejected.

### 9.3 Constrained reading
Owner code: 3 letters + category letter (U, J or Z); serial: 6 digits; check digit: 1 digit. Local OCR output is constrained to this shape. ISO size/type codes are validated against the ISO 6346 code list.

### 9.4 Shadow mode and promotion
- Extra engines can run silently on every move.
- Testing panel shows agreement and accuracy per engine against verified truth, broken down by depot, camera side, day/night and container type.
- **Promotion is manual:** an admin switches an engine to primary when it meets the configured threshold (default: ≥ 99% on ≥ 2,000 verified moves at that depot). One-click rollback.

### 9.5 True accuracy
**Random blind audit:** 1–2% (configurable) of auto-accepted moves go to a reviewer without the engine's answer shown. This is the reported accuracy figure.

### 9.6 Cost and privacy
- Cost per move tracked; monthly budget per company with alerts.
- Attempts keyed by photo fingerprint + engine + model + prompt version, so **replays never pay twice** unless forced.
- **Prompts are versioned** and tested by replay before going live.
- Cloud AI used only under terms where providers don't train on our data. A company setting **disables cloud AI entirely** (local-only).

### 9.7 Training dataset export (Phase 1)
Filtered export (depots, dates, camera side, verified-only, day/night) writes a package to a private bucket: full-resolution photos + labels (number, ISO code, bounding boxes) in **YOLO and COCO JSON** formats. **Only companies with training consent are included.** Optional blurring of faces and plates. Dataset screens come in Phase 2; Phase 1 has an admin command/button.

## 10. Admin and testing panel

### 10.1 Setup
Onboarding wizard: company → depots → lanes → **devices (paired by QR code)** → users, roles, PINs → lines and hauliers → gate policy and gate steps → pre-advice import mapping → printer and slip layout → connectors → AI budget → training consent. **Go-live checklist**: every item green/red; going live with red items requires a recorded admin override.

### 10.2 Testing tools
- **Test read:** photo in; every engine side by side; bounding box overlay; per-character confidence; check digit math step by step; latency; cost.
- **Policy tester:** container + line + lane → decision and reason.
- **Gate replay:** re-run past moves with another engine, prompt or policy; difference report.
- **Import dry-run:** row-level validation of a pre-advice file before saving.
- **Connector tester:** generate and validate CODECO for a move; send to a test endpoint; webhook ping with delivery log.
- **Practice lane:** sandbox for training clerks; records marked test, never billed or sent to lines.
- **Offline drill:** hub simulates internet loss on demand.
- **Device health:** online/last seen, version, queue length, disk, clock drift, internet and 4G state, printer state. (Phase 2: live camera view, focus and infrared checks.)

### 10.3 Troubleshooting
- **Truck timeline:** every photo, engine answer, decision reason, human action, sync state and connector delivery for one truck visit.
- **Error catalogue:** every error has a code, plain-language cause and fix, in Turkish and English (carried over from the prototype's 1xxx–9xxx codes).
- **Diagnostic bundle:** one-click export of hub logs and settings with secrets removed.
- **Support access:** granted by the customer, time-limited, every action visible to them.

### 10.4 Safe configuration
Every settings change is versioned, diffable and revertible. **Impact preview:** risky changes (e.g. raising automation) replay last week's moves and show how many would have been accepted and how many would have been wrong.

### 10.5 Dashboards
Operations (trucks/hour, gate time, exceptions by reason, true accuracy, cost per move, hub uptime) and a simple **owner phone view** (today's volume, trucks waiting, open disputes and exceptions).

### 10.6 Platform admin (us only)
Companies, module licenses, cells, staged releases, feature switches, engine defaults, dataset export, usage data.

### 10.7 Roles
Clerk → Supervisor → Depot admin → Company admin; Support (ours, time-limited, customer-approved); Platform admin (us).

## 11. Security

- **Office users:** identity service login; **MFA required for admins**; SAML/OIDC single sign-on available.
- **Gate staff:** personal PIN on an enrolled device; attempt limit; idle auto-lock; PINs stored as slow hashes and verified locally by the hub (works offline).
- **Devices:** key pair generated on the device at pairing; private key never leaves it; mutual TLS to the cloud; one-click revocation; hub disk encryption.
- **Integrations:** scoped, expiring API keys stored as hashes; HMAC-signed webhooks.
- **Tenant isolation:** row-level security plus automated cross-tenant access tests on every build.
- **Encryption** in transit and at rest; secrets in AWS Secrets Manager only.
- **Photos:** private storage; short-lived signed URLs; expiring, revocable share links.
- **Tamper evidence:** photo fingerprints taken on the device; a daily hash-chained digest of fingerprints and events per depot.
- **KVKK/GDPR:** per-company retention rules (default routine photos 12 months; disputed evidence retained until resolved plus a configurable period), deletion tooling, data processing agreement, camera signage, optional blurring.
- **Supply chain:** PR-only changes, dependency vulnerability scans, static security analysis, secret scanning, **signed releases** verified by the hub.
- **Independent penetration test** before the first paying customer; SOC 2 / ISO 27001 later.

## 12. Operations

| Area | Design |
|---|---|
| Deployment | Terraform; GitHub Actions; zero-downtime rolling deploys; **backward-compatible (expand/contract) database migrations** |
| Staged release | Staging → owner's depot → partner depot → all |
| Hub updates | Download → verify signature → swap → health check → **automatic rollback** on failure; only in each depot's quiet hours |
| Backups | Postgres point-in-time recovery; S3 versioning + cross-region replication; **monthly automated restore test**. Targets: RPO ≤ 5 min, RTO ≤ 4 h. Gate continuity is provided by the hub. |
| Hub replacement | Pair a new box; it pulls configuration and working set |
| Monitoring | OpenTelemetry + Sentry. Pager alerts only for gate down, sync backlog, error spikes; everything else in a daily summary |
| Time | Hubs sync clocks; drift alarm |
| Remote support | Remote health, logs, restart; emergency access only with customer approval, logged |
| Transparency | Public status page; incident runbooks in the repository |

## 13. Testing and quality

| Layer | Content |
|---|---|
| Core logic | Prototype self-test cases ported as fixed input/output tests; property-based tests over generated valid/invalid numbers |
| Shared test vectors | One set of JSON test files run by Go (hub and cloud) and TypeScript (tablet) |
| Integration | Real PostgreSQL and S3 emulator; hub↔cloud sync with offline periods, duplicate and out-of-order delivery |
| Security | Cross-tenant tests on every build |
| API contract | OpenAPI generated from Go; TypeScript client generated from OpenAPI |
| End-to-end | Playwright: matched gate-in, walk-in, office-resolved exception, offline gate-in |
| OCR benchmark | Hand-verified **golden photo set** (day, night, rain, dirt, damage, tanks, flat racks); CI uses recorded engine responses; live benchmark nightly; **no release if accuracy drops** |
| Failure | Network loss, hub killed mid-upload, power cut, clock skew: nothing lost |
| Load | Simulated 1,000-depot traffic on staging |

**Definition of done:** tests, Turkish and English strings, documented error codes, audit events, updated docs.

## 14. Milestones

| # | Milestone | Exit criteria |
|---|---|---|
| M0 | Foundation | Log in to staging; create company/depot/lane; all actions audited; isolation tests pass |
| M1 | Recognition core (prototype logic ported to Go, test-read tool, golden set of 300–500 photos from owner's yard) | Accuracy measured on the golden set |
| M2 | Capture app + online gate flow + exceptions and push | Complete online gate-in at owner's depot |
| M3 | Gate hub + offline + PIN login + printer | Gate-in works with internet unplugged; correct sync afterwards |
| M4 | Pre-advice (Excel mapping, dry-run, COPARN) + connectors (Excel export, CODECO, webhooks, drop folder) | Partner's real booking file imports; partner's Excel layout exports |
| M5 | Admin completeness (wizard, go-live checklist, timeline, replay, impact preview, dashboards, dataset export, blind audit) | Partner go-live checklist all green |
| M6 | Partner pilot (paper backup in week 1) | ≥ 95% of matched containers correct with no edits · median clerk time < 60 s · zero moves lost in outages · clerks prefer it to paper |

## 15. Prerequisites outside the software

1. Ask the partner depot: software name and maker; desktop or web; import options (Excel/CSV/EDI/API); what the clerk types per gate-in (screenshot); how they report to lines; whether the vendor would cooperate.
2. Collect sample pre-advice files and the partner's Excel layout.
3. Pilot agreement with a **training data-rights clause**, reviewed by a lawyer.
4. Buy pilot hardware (§6.2).
5. Start photographing the owner's yard stock (all sides, day/night, clean/dirty).

## 16. Deferred decisions (with when they get decided)

| Decision | Decided when |
|---|---|
| Partner's depot software and integration method | After the partner answers §15.1; before M4 |
| Hardware business model (rent/sell/customer-bought) | After the pilot |
| Exact cloud AI models per escalation step | M1 benchmark |
| Identity provider (WorkOS vs Auth0) | M0, on price and SSO features |
| Turkish-hosted cell | When a customer requires it |
| Whether Turkish customs (BİLGE) reporting applies | Before Phase 3 sales |
| Pricing levels | Before Phase 3 |
| Product name | Any time; code uses the working name `depotd` |

## 17. Prototype reuse map

From a code graph of the prototype (267 nodes, 13 communities):

| Prototype area | Fate |
|---|---|
| Check digit & frame reconcile; `cmd_selftest` cases | **Port** to Go with tests (core of M1) |
| AI OCR providers (`Provider`, Anthropic, Google, error classification) | **Port** behind the `Recognizer` interface |
| Event log & sync (`EventLog`, idempotent receive) | **Port the idea** into §7.3 events and hub sync |
| Error code catalogue (1xxx–9xxx with title + fix) | **Carry over** into the error catalogue (§10.3) |
| Backups & blob store | Replaced by managed Postgres/S3 backups; blob content-addressing kept |
| Settings & config (env vars, setup wizard) | Replaced by the onboarding wizard and versioned settings |
| `build_app()` (48 connections, 8 communities) | **Split** into the modules of §6.1 |
| Self-install venv, launchers, console window handling | **Dropped**: unnecessary with a single Go binary |
