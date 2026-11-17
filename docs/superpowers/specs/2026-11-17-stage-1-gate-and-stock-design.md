# Stage 1: Gate and stock — design

Date: 17 November 2026 · Status: draft for the owner's approval
Roadmap: `2026-11-15-depot-app-roadmap.md`, Stage 1 and 1b. Foundation: `2026-11-16-stage-0-foundation-design.md` (built).

## 1. Intent

A depot runs its gate and knows its stock without paper or Excel:
- A truck brings a container in, or takes one out.
- The gate clerk records it on a phone in about a minute, even with no network.
- The trucker signs.
- An EIR goes to the trucker and the customer.
- The office always sees what is in the yard, for whom, and for how long.

**Done when** the owner's depot runs gate and stock for two weeks with no paper and no Excel.

## 2. Goals, non-goals, anti-goals

**Goals**
1. **Orders:**
   - **Release orders** let a customer or line collect containers. Either named numbers ("MSKU1234565") or a quantity by type ("5 × 45G1 for booking X").
   - **Redelivery orders** announce containers coming back.
   - Both are entered by hand or imported from an Excel/CSV template.
2. **Gate in and gate out on a phone,** working offline. Each gate move records:
   - container number, checked against the check digit;
   - ISO size/type;
   - full or empty;
   - seal;
   - condition (sound / damaged);
   - each damage with up to 3 photos;
   - truck plate, trailer plate, driver name, haulier;
   - the trucker's signature on the screen.
3. **Gate checks:**
   - A container or line on the company's **block list** cannot pass.
   - A container with an open **hold** cannot leave.
   - Gate-out needs an open release order that covers the container. Gate-in may match a redelivery order; walk-ins are allowed and flagged, per a company setting.
4. **EIR:** one page per gate move, with the details above and the photos.
   - It can be printed or saved as PDF from the browser.
   - It is shared by an expiring link (WhatsApp/SMS/e-mail from the clerk's phone).
5. **Container record (the visit):**
   - one record per container stay, from gate-in to gate-out;
   - stack position (block, row, tier);
   - grade (A, B, C, food grade);
   - holds;
   - days in yard.
6. **Stock list and reports:**
   - containers in the yard per line or customer, with filters;
   - ageing (days in yard);
   - gate log;
   - a dashboard with today's in/out counts;
   - CSV/Excel export of every list.
7. **Start from Excel:** import the depot's current stock from a template, so day one starts with a real yard.
8. **OCR as an option:** when the company has the `ocr` module, the existing camera/phone reading fills the number and type. Without it, the clerk types them.

**Non-goals in Stage 1:**
- shipping-line EDI (Stage 1b);
- tariffs and charges (Stage 3);
- repairs, M&R (Stage 4);
- truck slot booking;
- the yard map;
- the e-mailed daily summary (it needs an e-mail service; comes with Part 0b accounts);
- a server-made PDF.

**Anti-goals:**
- a gate move that cannot be recorded because the network, the database or the AI is down;
- a photo not tied to its gate move;
- a container in two places at once (two open visits for one number in one company).

## 3. How it works

### 3.1 Records

All are company data with `company_id` and row-level security, following the Stage 0 rules.
- **`release_orders`:**
  - fields: id, depot, kind (`release` | `redelivery`), reference (booking/release number), line, customer, valid_until, note, status (`open` | `done` | `cancelled`);
  - **items:** either a named container number, or a quantity of an ISO type;
  - each item tracks how many have been used.
- **`visits`:**
  - fields: id, depot, container number, ISO type, line, customer, status (`in_yard` | `out`), the gate-in move and the gate-out move, in_at, out_at, position (block, row, tier), grade, full/empty;
  - at most one visit per container number in a company may be `in_yard` (unique partial index).
- **`holds`:**
  - fields: id, visit, reason, placed_by, placed_at, released_by, released_at;
  - an open hold blocks gate-out.
- **`block_list`:**
  - fields: id, kind (`container` | `line`), value, reason, active;
  - a match blocks gate in and out, with the reason shown.
- **`gate_moves`** (exists) gains:
  - direction `in` | `out`, visit_id, order_id, order_item_id;
  - truck_plate, trailer_plate, driver_name, haulier_id;
  - seal, full_empty, condition;
  - signature (a blob digest).
- **`damages`:** fields id, move_id, location (free text now; CEDEX codes come with M&R), description, photo ids (1–3).
- **Settings per company:** walk-ins allowed or not; EIR link lifetime (default 30 days).

### 3.2 Gate flow (phone)
1. **Pick in or out.**
2. **Enter the container.** The number is typed or read by OCR, and the check digit is shown live.
3. **The app shows what it knows:**
   - matching orders;
   - an open visit (for gate-out), or a block-list or hold reason.
4. **Fill the rest:**
   - type, full or empty, seal, condition;
   - for each damage: location, note and photos;
   - truck, trailer, driver, haulier.
5. **The trucker signs.**
6. **Save.** The move is stored with its photos, and the EIR link is offered for sharing.

Gate-in opens a visit; gate-out closes it and uses up an order item. A gate-out with no matching order, or blocked by a hold or the block list, is refused with the reason. A manager can override it with a recorded reason.

### 3.3 Working offline
- **Outbox on the phone.** The phone keeps an outbox in IndexedDB: each move with its photos and signature, plus an id the phone generates. The web app's service worker keeps the app usable offline.
- **Sending.** When the network returns, the outbox sends in order. The server treats a repeated id as the same move, so a resend is harmless.
- **Checks while offline:**
  - order and block-list checks use a copy cached on the phone (refreshed when online);
  - a move saved offline that the server later refuses goes to the office review queue with the reason, never lost.

### 3.4 EIR
- **The page:**
  - the EIR is an HTML page in the Depot Gate design, laid out for A4 printing, reachable at `/eir/{token}`;
  - the token is random, expiring and stored hashed, like sessions;
  - anyone with the link sees that one EIR, and nothing else.
- **Sharing:** "Share" uses the phone's share sheet (WhatsApp, SMS, e-mail). "Print / PDF" uses the browser's print dialog. No PDF server and no e-mail service are needed.

### 3.5 Stock and reports
- **Stock list:** visits that are `in_yard`, filterable by line, customer, type, grade, hold and days in yard.
- **Other views:**
  - the gate log (moves by day);
  - ageing buckets (0–7, 8–30, 31–90, 90+ days);
  - a dashboard with today's moves, in-yard count by line, and open holds.
- **Export:** every list exports as CSV, which Excel opens.

### 3.6 Excel import
- Fixed templates (downloadable) for stock and orders.
- **Stock import** creates `in_yard` visits without gate moves, marked "imported".
- Rows that fail (bad check digit, unknown line, duplicate in-yard number) are listed with the reason, and nothing is half-imported.

### 3.7 API and screens
- **API:** new operations in `api/openapi.yaml` for orders, visits, holds, block list, the move details above, EIR tokens, imports and exports; generated code as in Stage 0.
- **Screens:**
  - **Gate:** the phone flow.
  - **Yard:** stock list, visit detail with holds, position and grade.
  - **Orders:** list, detail, import.
  - **Block list.**
  - **Reports.**
- **Permissions:**
  - gate staff use the Gate screens;
  - office staff use everything else;
  - managers can override refusals.

### 3.8 Rules settled during review (my calls unless marked)
- **One move, one record.**
  - A gate move is one `gate_moves` row: OCR fills the number on it, and the clerk's form completes it.
  - The visit opens or closes, and an order item is used, when the clerk **saves** the move.
  - A camera lane read with no clerk form waits as "gate form pending" and creates no visit until a clerk completes it.
  - Existing rows are test data and stay as they are.
- **The truth at the gate wins; conflicts are flagged, never lost.** A move is always stored; a conflict marks it for the office review queue (office role), with the reason:
  - **Gate-in of a number already in the yard** (any depot of the company): the old visit is closed as "missed gate-out", the new one opens, and the move is flagged.
  - **Gate-out with no open visit:** a closed visit is created from this move, and the move is flagged.
  - **Order item used up, or order expired, by the time an offline move syncs:** the move is stored, and flagged.
  - **Two phones offline gate-in the same number:** the second to sync is handled as a gate-in of a number already in the yard.
- **Line and customer of a visit:**
  - They come from the matched order.
  - Otherwise the clerk picks the line (customer optional). The app suggests the line last used for that owner prefix (the first four letters of the number).
  - Imported stock gives them in its columns.
  - The block list matches by container number, or by line when the visit has one.
- **Order matching:**
  - The line must match when the order names one.
  - Full/empty must match the order kind (release = empty unless marked full).
  - An expired order (`valid_until` in the past) refuses.
  - If several orders match, the clerk picks one.
  - Redelivery orders have quantity items too.
  - A release order cannot be over-collected.
  - Cancelling an order closes its remaining items.
- **Corrections:**
  - The office can correct a move's details or number, or void it with a reason. Both are recorded as events.
  - A correction moves the visit and the order item to the right ones; a void releases the order item.
  - The EIR link then shows the corrected version, marked "corrected".
- **Offline cache:**
  - **What each phone keeps for its depot:** open orders with their items, the active block list, open holds, and the numbers in the yard (for gate-out checks).
  - **Freshness:** refreshed whenever online, with the age shown ("lists updated 14:02").
- **Import:**
  - Each stock row needs a gate-in date, so ageing is right from day one.
  - Lines and customers are matched by code.
  - A file is imported whole or not at all.
- **OWNER decision (17 Nov) — refusals at the gate** (no matching order, an open hold, or an expired order). How they are handled is a company setting with three choices:
  1. **Clerk lets it through** with a written reason; the move is flagged for manager review.
  2. **Manager approves first.** The refusal appears instantly in the manager's queue on their own phone or PC, with the photos; the clerk's screen shows the answer. It needs the network; while offline, the clerk can only hold the truck or refuse it.
  3. **No exceptions.**

  Default: choice 1. Block-list matches can never be passed at the gate under any setting.
- **OWNER decision (17 Nov) — type matching for quantity orders:**
  - An item counts containers of the same **size and height** class (for example 45G0 and 45G1 both count as 40' high cube general purpose).
  - The type group (G/R/U…) must also match, so a reefer never fills a dry order.

## 4. Testing
- **Store and RLS tests** for every new table, following Stage 0's pattern (company A cannot see company B).
- **Gate rules, table-driven:**
  - gate-out without an order is refused;
  - a hold or the block list refuses;
  - an order item is used up only once;
  - two open visits for one number cannot exist;
  - a resent offline move is stored once.
- **EIR link tests:** expired and wrong tokens give 404; the page shows only its own move.
- **Import tests:** good rows are imported, and a file with bad rows imports nothing.
- **Two Playwright tests** (phone layout):
  - an offline gate-in that is sent later;
  - a gate-out refused by a hold.

## 5. What the owner provides (to confirm the fields above)
A filled EIR or gate slip, a release order and a booking, the stock Excel, and the list of lines, customers and hauliers. If the real documents show fields this design lacks, they are added before building.

## 6. Calls I made (owner can overrule)
- EIR as a shareable web page with browser print-to-PDF, instead of a PDF server and e-mail service (fewer moving parts; works before Part 0b accounts).
- Release orders support both named containers and quantity-by-type items.
- Walk-ins are allowed by default, but flagged, with a company setting to refuse them.
- Damage location is free text now; CEDEX codes come with M&R (Stage 4).
- Refusal handling is a company setting (owner decision); every override is recorded with a reason.
- Stock import creates visits marked "imported" without gate moves.

## 7. Built - deviations from this design
- **Turn away.** A phone that is refused and gives up calls `POST /api/moves/{id}/turn-away`. It voids the move ("turned away"), records the number and reasons, and raises a `turned_away` flag for the office. It goes through the offline outbox and is safe to send twice.
- **Order not open is a refusal.** An expired, done or cancelled order raises `order_not_open` and follows the refusal setting like any other reason. An expired redelivery with walk-ins allowed is treated as a walk-in and flagged.
- **Approvals are tied to reasons and the number.** An approval only lets a truck pass if it covers the reasons the truck is refused for now, for the container number it was asked for. The gate phone hears the decision live.
- **Corrections.** They record the truth even if a rule would refuse it, and leave an `override:<reason>` flag for the office (a blocked number included). A blocked number still matches its order item, so a correction keeps the item used and its flags. A correction cannot change direction: void it and record again. A number correction moves the visit. Voiding a gate-in closes the visit as "voided". A gate-out reversal is refused (409) if the number has a newer visit in the yard.
- **Gate-out from another depot** closes the old visit there (`other_depot_gate_out`) and raises an `other_depot` flag.
- **EIR.** The time is the first form save and never changes on correction. Times use the depot time zone. It shows the phone's container photos, damage and signature, never camera-lane frames (a driver may be in them).
- **Days in the yard** (`daysInYard`) are depot-local calendar days, the same rule as the ageing report. The server computes it, and the yard screen filter uses it.
- **Flag notes.** Resolving a gate flag takes an optional note (migration `004_flag_note.sql`). Migration 003 adds the container number to approvals.
- **CSV files.** A file that is not UTF-8 is read as Windows-1254, which is what Turkish Excel saves. Imports apply all or nothing.
- **Offline phone.**
  - The service worker needs HTTPS to reload the app offline on a phone. Over plain `http://` on the LAN (the hub) the outbox still works while the page stays open.
  - Entries left in the outbox after sign-out are sent by the next user who signs in on that phone, and that user becomes the recorded actor. Each entry also carries `savedBy` (who was signed in on the phone when it was saved), kept in the move event as a claim only. The phone warns on sign-out when entries are unsent.
  - An offline save that is predicted to be refused shows Hold/Refuse, not success, unless the mode is clerk override with a reason.
  - A form saved offline is sent with `savedOffline`. The truck has gone, so the server stores it in every refusal mode, block list included, and turns each refusal it finds into an `offline:<reason>` flag in Gate issues. The phone never offers to turn such a truck away.

### Deferred
- Indexes on gate move visit/order keys; paging on order, approval, flag and block lists; gate log and export caps (5k / 100k rows) are silent.
- Order reference and line on the EIR.
- A "recently sent" list on the phone; phone recents are not cleared at sign-out.
- Office times follow the browser time zone, not the depot's.
- Import: row numbers skip blank lines; a duplicate number inside one order is not caught.
- A midnight/time zone edge test for `daysInYard`.
