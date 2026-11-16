# Depot app roadmap and how to proceed

Date: 16 November 2026 (v2) · Status: draft for the owner's approval

**What this replaces.** It replaces the phase order in `2026-09-30-gate-ocr-phase-0-1-design.md` §3, and v1 of this file (15 Nov, Turkey-first, depot PC first).

**What changed in v2.** The owner asked for a global, mass-market product with billing detached and optional. Two research reports shape it:
- `C:\Users\OZGUN\reports\Depot Master improvement opportunities.md`
- `C:\Users\OZGUN\reports\Faster global depot SaaS build.md`

**What still applies from the 2026-09-30 design:** its principles, data rules and Go + React stack. Its AWS ECS + Terraform plan does not.

## 1. Where we are

**Built and working (on one PC at the owner's depot):**
- One Go program (`depotd`) with a React web app, SQLite and photo storage.
- Gate visits from a phone or a Reolink camera, with AI reading checked against the ISO 6346 check digit.
- A review queue for people, the "Depot Gate" design (day/night, English/Turkish), live updates.
- Rows already carry a company id and a depot id.
- Gemini read 10/10 of the owner's test photos.

**Not built yet:**
- logins and roles;
- customers and shipping lines;
- release orders, gate-out, stock list, EIR;
- tariffs and charges;
- repairs;
- the cloud, signup, payments.

**Repo gaps:** no CI and no project instruction file.

**What the research says:**
- **Competitors:** DepotMaster (Swiss) has offline-less mobile, an 8-week setup and no public prices. New AI-first rivals (ContainerHub $0–999/month, Lotics $0–280/month) sell self-serve with public prices.
- **Market size:** an estimated 2,000–5,000 independent depots worldwide (from lessor and network sizes, not a count).
- **How depots buy:** "self-serve start, assisted finish". A depot can start alone; shipping-line EDI and tariffs need help.
- **Speed:** comes from renting the platform pieces and letting automatic checks (types, tests, database rules) catch mistakes, not from the AI alone.

## 2. Goal

A depot anywhere in the world signs up, imports its stock from Excel, and issues its first photo EIR **within one hour, without contacting us.**

The core is what every depot shares:
- the container visit (gate-in, stay, gate-out);
- photos tied to events;
- the EIR;
- the stock;
- a record of charges.

Everything country-specific (invoices, tax, e-invoicing, local hosting) is an optional add-on.

**Not goals now:**
- line EDI breadth;
- 3D yard;
- reefer PTI;
- repair auditing for lines;
- single sign-on;
- several regions.

**Would count as failure even if it works:**
- custom code for one customer;
- becoming an EDI help desk;
- billing rules in the core;
- anything that stops the gate;
- photos that can't be tied to an event.

## 3. Key decisions

Each decision shows my recommendation. **D1 and D8 need the owner's answer.**

**D1. Cloud first, for many companies, from Stage 0.**
- The depot PC becomes an optional offline box for depots that ask, built after the first paying cloud customers.
- Today's working depot PC keeps running at the owner's depot until the cloud version replaces it.

**D2. Hosting: rent it.**
- One European region on a simple hosting platform: one Go container plus managed PostgreSQL.
- Photos in Cloudflare R2, errors in Sentry.
- No AWS ECS and Terraform.
- Gulf or Turkey regions only when a customer requires local hosting.
- Turkish customers get a ready-made KVKK contract pack.

**D3. Database: PostgreSQL now, before new features.**
- Every query written for SQLite would be written twice.
- Every customer's data is separated by the database itself (row-level security), so a forgotten filter can't leak data.

**D4. Logins: WorkOS.**
- Free until a customer wants single sign-on.
- Companies, depots and roles live in our own tables, so the vendor can be swapped.

**D5. Phones work offline as a web app.**
- Gate moves wait in an outbox on the phone, and photos upload straight to storage.
- A sync service (PowerSync) only when phones must also read the stock offline.

**D6. Billing split in two:**
- **Core, always on:** tariffs and a charge record that is never edited, created from gate moves and days in the yard. Plus a statement per customer and a CSV/Excel export. No tax, no invoice numbers, no currency conversion.
- **Billing module, optional and paid:** invoice drafts, tax mapping, the exchange rate frozen on each invoice, numbering. Output goes to an accounting program or an e-invoice service through **country packs**:
  - **Turkey first:** export to the depot's accounting program (as decided).
  - **Then:** Xero and Odoo.
  - **E-invoicing services** (Peppol, others) only when a paying depot needs one.

**D7. Faster building with automatic checks.**
- One API description (OpenAPI) generates the Go server code and the typed web-app code.
- The database code is generated from SQL (sqlc).
- List and edit screens come from a framework (Refine) styled with our design.
- CI runs the tests on every change, a short project instruction file holds the check command, and a hook refuses to finish while tests fail.

**D8. Charging our own customers: a merchant-of-record service** (Paddle or Polar). It handles VAT and sales tax worldwide so we don't register in each country. Stripe's equivalent doesn't accept sellers based in Turkey.
- **Open:** which country will the selling company be registered in? This decides which service is possible.

**D9. Unchanged from v1:**
- Customer rules are kept as settings, never code.
- EDI only when a paying depot needs a specific line.
- Gate OCR is an add-on, sold per read, with automatic fallback to another model.

## 4. Stages

Durations are rough guesses for me building with the owner reviewing.

### Stage 0: Foundation (about 2–3 weeks)
- **The checks:** CI, the project instruction file and the test hook.
- **PostgreSQL** with per-customer separation in the database.
- **API-first generation:** OpenAPI + generated Go and web code + sqlc.
- **Logins** through WorkOS, with our own companies, depots and roles.
- **Hosting:** the EU region on a rented platform, photos in R2, Sentry.
- **Reference data:**
  - shipping-line codes (SMDG lists, free);
  - container size and type codes;
  - port codes (UN/LOCODE).
- **Real documents from the owner's depot** (EIR, release order, tariff sheet, stock Excel, invoices) to shape Stages 1–2.

### Stage 1: Gate and stock (about 4–6 weeks)
- **Release and redelivery orders,** by hand or Excel import.
- **Gate in and out on a phone,** offline:
  - check digit;
  - full or empty, condition, seal;
  - in-app photos stamped and tied to the move;
  - damage noted at the gate with up to 3 photos per damage;
  - truck, trailer and driver details;
  - **the trucker signs on the phone.**
- **Gate checks:**
  - blacklist and whitelist checked at the moment of the move;
  - gate-out only against a valid release order;
  - blocked or held boxes can't leave.
- **EIR as PDF** (made from HTML with Gotenberg), emailed automatically and shared by link or WhatsApp. It covers what Maersk's own EIR app captures: container, damage with photos, trucker, signature.
- **Container record:**
  - stack position (block, row, tier);
  - grade (A/B/C, food grade);
  - holds;
  - days in yard and free days.
- **Stock list** with days in yard, per line.
- **Basic reports**, which every competitor has:
  - container ageing (days in yard);
  - a daily status summary e-mailed to the manager and each line;
  - truck time at the gate;
  - a dashboard;
  - Excel export of every list.
- **Camera and OCR** plug in as an optional reader.

Done when the owner's depot runs gate and stock for two weeks with no paper or Excel. Meanwhile, the old depot PC version retires.

### Stage 1b: Standard shipping-line EDI (about 2–3 weeks)
Every competitor has this, so it is basic (owner decision, 16 Nov).
- **One generic profile** following SMDG's recommended D.00B guides (version 2.1.3):
  - **release and booking orders in (COPARN):** arriving as release orders;
  - **gate moves out (CODECO):** sent straight after each move;
  - **acknowledgements and errors (APERAK).**
- **Sending and receiving** by e-mail or SFTP.
- **Per line:** small adjustments to the profile, stored as data. A line without EDI gets an automatic PDF/Excel e-mail instead.

### Stage 2: Ready for anyone to sign up (about 5–7 weeks)
- **Self-serve signup** and a demo depot.
- **Excel import** of stock, lines and customers.
- **Public price list:**
  - a free tier;
  - paid tiers by gate moves per month;
  - payments through the merchant-of-record service turning paid modules on and off.
- **Languages:** English, Turkish and one more (Arabic or Spanish, after Checkpoint A).
- **Legal basics:** country blocking at signup (sanctions), a data processing agreement, the KVKK pack.
- **Read-only portal** for each line or customer: stock, EIRs, photos.
- **An admin console for us:** health, logs, support access only with the customer's permission.

Done when a depot in another country signs up, imports its Excel and issues a photo EIR within one hour without contacting us.

### Checkpoint A: Check the market (alongside Stage 2)
- **Interviews:** about 10 depot operators across three regions — Türkiye, the Gulf, and one Mediterranean or Indian hub — picked from lessor depot lists. Ask what they use today, what they would pay, and whether they would pay for setup help.
- **Payment check:** confirm that the merchant-of-record service accepts our selling company's country.

### Stage 3: Charges (core) and the billing module (optional) (about 4–6 weeks)
- **Core:**
  - tariffs per customer or line (lift-on, lift-off, storage with free days, extras; currency and start date);
  - the never-edited charge record;
  - the monthly statement;
  - CSV/Excel export.
- **Billing module:**
  - invoice drafts with tax mapping, frozen exchange rate and numbering;
  - each line's evidence pack (EIRs, photos);
  - the Turkey pack (export to the depot's accounting program);
  - then Xero and Odoo connectors;
  - **self-billing check:** import a line's self-billing file and match it line by line against our charge record (DepotMaster offers this).

### Stage 4: Repairs, M&R (paid module, about 6 weeks)
- **Damage codes:** CEDEX tables seeded from IICL's free code standards.
- **Damage survey:** a photo for each damage line.
- **Estimates** to the IICL, UCIRC, CW or WWT standard, priced from each line's repair tariff.
- **Approvals:**
  - estimate out as the generic repair-estimate message (WESTIM/DESTIM), or PDF/Excel;
  - approvals back by message or bulk import;
  - approval chasing.
- **Work orders** for the workshop, with repair-completion photos for every approved line; completion reported back to the line.
- **Rule:** no repair before approval and pre-repair photos.
- **Each line's rules stored as a reusable profile.** Every new depot that works with that line starts with them.
- **AI draft of the estimate (paid add-on):** Gemini proposes estimate lines from the damage photos and a surveyor confirms them. EOS's repair app has shipped AI estimate drafting and checks since 2025, so this is where the market is moving.
- **Later, as its own module:** reefer machinery and PTI results.

### Stage 5: Add-ons, only when a paying customer asks
- **Line APIs per customer,** such as Maersk's EIR system (DepotMaster connects to it), so gate staff don't enter data twice.
- **Truck slot booking (paid module):** a trucker portal to look up orders and book slots; depots may charge truckers for slots.
- **Damage recovery:** bill the trucker or customer who caused a damage.
- **Gate camera and OCR sold per read,** including plate reading.
- **Other add-ons:**
  - e-invoice adapters per country;
  - the offline depot PC (signed installer, automatic updates with rollback);
  - links to repair and trading networks (EOS AI, Container xChange);
  - single sign-on;
  - more regions.

- **Container sales and leasing (module, high priority among add-ons):**
  - stock for sale;
  - quotes;
  - sales orders;
  - lease-out and on-hire;
  - a buyer portal linked to the owner's sales website.

  The research found no depot system that covers sales, so it is open ground, and the owner already sells containers.

**Not now:** 3D yard, reefer PTI, DCSA standards (watch from 2027).

Rough total to "anyone can sign up" (end of Stage 2): **about 3–4 months.** This is a guess. Billing comes off the critical path, and the rented building blocks save platform work.

## 5. What carries over from today's code

**Reused as is:**
- the gate logic;
- the check digit;
- the event log;
- photo fingerprints;
- the review queue;
- live updates;
- the design system and English/Turkish texts;
- the camera and OCR code.

**Changes:**
- SQLite gives way to PostgreSQL.
- Hand-written handlers and API calls give way to generated ones.
- The shared access code gives way to personal logins.
- "Gate move" becomes one step in a container visit.

## 6. Risks

| Risk | What we do |
|---|---|
| Nobody pays enough (no price data anywhere) | Free tier + Checkpoint A interviews in three regions before the billing module |
| Too many rented services | Standard PostgreSQL, S3-compatible storage, logins only from the vendor, paid modules from our own table: each can be swapped |
| The payment service won't accept our company's country | Check at Checkpoint A; register the selling company where needed |
| AI-written code with hidden bugs | Generated types, database-level customer separation, CI and the test hook |
| Legal surprises (KVKK, sanctions, local hosting) | KVKK pack, country blocking, local regions only on demand |
| The first foreign depot struggles | The one-hour onboarding target measures exactly this |
| Several facts are unverified (half the web lookups failed) | Check the open items in the second report before spending money |

## 7. Next steps after approval
1. The owner answers D1 and D8, and gathers the depot documents.
2. I write the design for Stage 0 and Stage 1. A reviewer reads it first, and I may come back once with a few questions.
3. After approval: the build plan, then Stage 0.
