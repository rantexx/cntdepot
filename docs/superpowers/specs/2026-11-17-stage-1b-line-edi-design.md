# Stage 1b: shipping-line EDI (design)

Roadmap: `2026-11-15-depot-app-roadmap.md`, "Stage 1b". Builds on Stage 1 (`2026-11-17-stage-1-gate-and-stock-design.md`, built).

## 1. Why

Every competing depot system exchanges EDI with shipping lines, so depots treat it as basic (owner decision, 16 Nov).
A line sends the depot its release orders as files; the depot sends the line a gate message after every move.
Today our clerks would retype orders and the line would get nothing. Success: a line's release order arrives and
the gate can use it with nobody typing, and the line receives a correct gate message within minutes of each move.

## 2. Goals, non-goals, anti-goals

**Goals**
- **Orders in (COPARN):** a line's release or redelivery order arrives as EDI and becomes an order in the app. Replacements and cancellations update it.
- **Gate messages out (CODECO):** sent automatically after each gate-in and gate-out of a container whose line has EDI on. Corrections and voids are sent too.
- **Acknowledgements (APERAK):** we answer each COPARN, accepted or with its errors. We read the line's APERAK for our CODECO.
- **Transport: e-mail** (owner choice, 17 Nov: "option 1"). Each company has its own inbound address. CODECO goes out as an attachment.
- **By hand:** an office screen lists every EDI message in and out. Staff can upload a file received some other way, download any file, and resend.
- **Per-line differences as data:** a line's EDI profile (its codes, addresses, options) is a settings record, never code.
- **Lines without EDI:** a daily e-mail with an Excel file of that line's gate moves.

**Non-goals (this stage)**
- SFTP, AS2 and VAN connections. These come later, when a real line asks for them.
- Other messages: COPRAR, COARRI, COREOR, and repair estimates (DESTIM/WESTIM, Stage 3).
- ANSI X12, and line-specific dialects beyond what profile options cover.
- Full bookings (export loads): only the empty release and redelivery that the Stage 1 orders model already has.

**Anti-goals (a failure even if it works)**
- **The gate waits on EDI.** EDI failures never block or slow a gate move.
- **An inbound file is lost.** Every received file is stored raw before parsing, whatever happens next.
- **A re-sent COPARN creates a second order.** Inbound processing is idempotent.
- **Data goes to the wrong company or line.** Inbound and outbound stay inside one company, under RLS, like everything else.
- **We become an EDI help desk.** Problems show clearly on the screen, with the raw file, so the depot can sort it out with the line.

## 3. Constraints

- **SMDG guides:** follow SMDG's recommended D.00B guides, version 2.1.3, for COPARN and CODECO, and the APERAK guide.
  - These are free downloads from smdg.org (Container Messages). Check their terms before quoting them in the repo.
  - **Do not bundle** the SMDG code lists or UN/LOCODE (Stage 0 rule).
  - Line codes and location codes are typed in by the depot.
- **Stage 0 and 1 rules hold:** `company_id` plus RLS on every new table, generated code never hand-edited, and the gate never stops.
- **Accounts and secrets:** the owner creates the mail-service account. Secrets come from the environment and are never committed.
- **Development and tests** never need the mail service: a file mailer stands in.

## 4. Approaches

1. **E-mail first, with upload and download by hand (chosen by the owner).**
   - Works with every line and agent, small ones included.
   - The daily Excel e-mail for non-EDI lines needs sending anyway.
   - The upload and download screen means nothing depends on the mail service on day one.
2. **E-mail and SFTP together.** Covers big lines from the start, but takes about a week longer and adds an always-connected client to keep reliable.
3. **SFTP only.** Fits big lines, but leaves out the many lines and agents who work by e-mail.

## 5. Design

### 5.1 Pieces
- **`internal/edi`:** EDIFACT reading and writing.
  - Handles UNA, the release character, UNB/UNZ, UNH/UNT, and segment counts.
  - Builds and reads the three messages.
  - Uses no database. It is tested with golden files.
- **Records:**
  - **`line_edi`:** one row per shipping line with EDI on, holding the profile.
  - **`edi_messages`:** one row per message in or out.
    - Holds: direction, line, kind, status, UNB control reference, the move or order it concerns, and error text.
    - The raw file lives in the existing blob store.
  - **`company_edi`:** the company's inbound address token and its own EDI party id.
  - **Per depot:** its EDI location code (for example a UN/LOCODE plus a depot code, typed in).
- **Mail:** a `Mailer` interface with two drivers.
  - **Postmark** (my call: one service for sending and receiving, with a simple JSON webhook).
  - **A file driver** that writes `.eml` files to a folder, for development, tests and the local test kit.
- **Workers:** a background sender, and the daily report job.
  - They find companies with work through a narrow SECURITY DEFINER function, the same way as `eir_lookup`.
  - Then they do the work inside that company's scope.

### 5.2 Line EDI profile (data)
- **Parties:**
  - our sender id and the line's recipient id (UNB), with qualifiers;
  - the line's code as used in messages, which defaults to the line's code.
- **Addresses:**
  - where CODECO and APERAK are sent (one or more);
  - which sender addresses may send us COPARN.
- **Options:**
  - send CODECO for empties only, or full and empty;
  - send our APERAK (on or off);
  - a test indicator (UNB test flag) while a line is being set up;
  - a container type code map for lines that still use old codes (at most a short list of pairs).
- **Non-EDI report:** recipients and the local send time, for lines without EDI.

### 5.3 Orders in (COPARN)
1. **How a file arrives:**
   - by e-mail to `edi-<token>@<inbound domain>`, through the Postmark webhook; the webhook is protected by a secret, and its body is capped;
   - or by upload on the EDI screen.
2. **Stored first:** the raw file is stored and an `in` message row is written before any parsing.
3. **Which line:**
   - The line is matched by the UNB sender id against line profiles.
   - For e-mail, the sender address must also be allowed for that line.
   - No match means the file is "unmatched": it is held on the screen, where staff can pick the line and process it again.
4. **Each COPARN maps to an order:**
   - **Order kind:** release or redelivery, from the message's document code as the SMDG guide defines it.
   - **Reference:** the booking or release number becomes the order reference. One line plus one reference is one order.
   - **Items:** an equipment line with a container number becomes a named item. A line with only a type and a quantity becomes a quantity item.
   - **Fields:** validity date, full or empty, and the depot (from the location code, or the company's only depot).
5. **Message function:**
   - **Original:** creates the order. If it already exists, it is treated as a replacement.
   - **Replacement:** updates the order. Items already used at the gate are kept, and the change is recorded in the order's history.
   - **Cancellation:** cancels the order (Stage 1 behaviour).
6. **Repeats:** the same UNB control reference from the same line is ignored, so a re-sent file never duplicates an order.
7. **Errors:**
   - Any error (no such depot, a bad container number, an unknown type, a cancelled order that does not exist) stops that message only.
   - The message shows on the screen with the reason. When on, an APERAK with the error goes back to the line.
   - Messages without errors get an accepting APERAK.
8. **Origin:** orders that come from EDI are marked as such and link to their message.

### 5.4 Gate messages out (CODECO)
- **When:**
  - A stored gate-in or gate-out (`move.gate_saved`), for a container whose line has EDI on, queues one CODECO in the same transaction.
  - Offline moves are sent when they reach the server, with the real gate time.
- **Changes after sending:**
  - A correction sends a replacement.
  - A void sends a cancellation.
  - A turned-away truck sends nothing.
- **Content:**
  - the container, type, full or empty, gate time, direction, the order reference used, seal, truck plate, haulier, and condition;
  - damages as the guide allows;
  - the depot's location code.
- **Sending:**
  - One interchange per e-mail, as an attachment, to the line's addresses.
  - Pending messages are sent by the background sender with retries: 1 min, 5 min, 30 min, then hourly for a day, then "failed" on the screen.
  - The order of messages per container is kept.
- **Answers:** an APERAK from the line is matched to our message by reference and marks it accepted or rejected, with the line's error text.

### 5.5 EDI screen (office)
- **The list:**
  - Shows messages in and out. Filters: line, kind, status (pending, sent, accepted, rejected, failed, unmatched, error) and date.
  - A count of problems shows on the navigation, like Gate issues.
- **Each message:** its status, the order or move it concerns, the errors, the raw file (view and download), and Resend or Process again.
- **Upload:** one or more EDI files are handled like inbound e-mail.
- **Line EDI settings:** a form for the profile, including "send a test CODECO".
- **Language:** English and Turkish, like every screen.
- **Permission:** a new permission, `edi.manage`, for owners and managers.

### 5.6 Lines without EDI
- Once a day, at the time set on the line (in the depot's time zone), an e-mail goes to the line's recipients.
- It carries a CSV file (UTF-8 with BOM, opens in Excel) of that depot-local day's gate moves for the line. Voided moves are left out; corrected moves show as corrected.
- Nothing is sent on a day with no moves.
- My call: CSV, because the app has no Excel writer yet. A real .xlsx means a new library, so it waits until someone asks.

### 5.7 Configuration
- **Environment:** the mail driver (`postmark` or `file`), the Postmark server token, the inbound webhook secret, the inbound domain, the from address, and the file mailer folder.
- **Without the mail service:** with no Postmark token the app uses the file driver, and the screen says e-mail is not connected. Upload and download still work.

## 5.8 Rules settled at review (my calls)
1. **Which order a COPARN updates:**
   - An EDI order is keyed by company, line, kind and reference. A unique index covers EDI-origin orders only, so hand-made orders are unaffected.
   - One COPARN message is one order.
   - Equipment for another depot, or both kinds in one message, is an error for that message.
2. **Replacement on a partly used order:**
   - Named items match by container number. Quantity items match by type plus full or empty.
   - Changes that would undo gate history are kept as they are and listed as errors in the APERAK and on the screen. That covers dropping a named container already gated, or lowering a quantity below what was used.
   - Every other change is applied.
3. **The line of a move changes after sending:**
   - A correction that changes the line or the number sends a cancellation to the old line (if it has EDI) and an original to the new one (if it has EDI).
   - A move with no line sends nothing. If a line is set later, CODECO is sent then.
4. **Repeats and control references:**
   - A file counts as a repeat only when the line, the UNB control reference and a hash of the file content all match.
   - A repeated reference with different content is processed, with a warning, so no file is ever dropped.
   - Our outbound control references come from a per-company counter.
5. **Trusting the sender of an inbound e-mail:** an allowed address counts only when Postmark reports SPF or DKIM passing. Otherwise the file is "unmatched": held, never lost.
6. **Workers:** pending messages are claimed with `FOR UPDATE SKIP LOCKED`, so a second instance later is safe. Today depotd runs as one instance.
7. **Test CODECO:** built from a made-up sample move with the test flag, stored as a message of kind "test" and linked to no real move.
8. **Type code map:** applies in both directions.
9. **Depot from a COPARN:** the location code picks the depot.
   - With no code, a company with one depot uses it.
   - A company with several depots gets an error for that message, held on the screen, where staff can pick the depot and process it again.

## 6. Open, for the owner (not blocking the build)
- **Postmark account:** create it and check that inbound processing is on the chosen plan.
- **Domains:** inbound needs a domain (MX record) and sending needs DKIM/SPF on our domain, so this waits on the brand name.
- **A real COPARN sample:** one from a partner depot's line, so we can check our reading against it.

## 7. Left to the builder
- **Segment and code values** follow the SMDG 2.1.3 guides, read at plan time:
  - the exact segments and qualifiers;
  - the document and function codes;
  - the APERAK error codes.
- Table and column names, API shapes, and the screen layout follow existing patterns.
- How the sender keeps per-container order, and backs off.
