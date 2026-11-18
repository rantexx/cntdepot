# Local gate test kit: how to run it

One Windows PC runs `depotd hub`. Phones on the same Wi-Fi open it in the browser,
photograph containers, and an office screen confirms or corrects what was read.
Everything stays on the PC: the hub runs its own PostgreSQL (data in `depot-data/pg`) and keeps photos
in `depot-data/blobs`. The depot keeps running `depot-pc-v1` (the old SQLite hub build) for real gate
work; this build is for testing the new platform and does not read the old `depot.db`.

## One-time setup

1. Install Go 1.27+ (https://go.dev/dl/; `go.mod` requires it) and Node 20+.
2. Create a Gemini API key in Google AI Studio (https://aistudio.google.com/apikey) on a project
   **with billing enabled**: on the unpaid tier Google may use submitted photos to improve its
   products. In **your own** terminal run `setx GEMINI_API_KEY "<key>"`, then open a new terminal.
   Never paste the key in chat or commit it.
3. Build, from `C:\Users\OZGUN\depot-platform`:

   ```
   npm --prefix web install
   npm --prefix web run build
   go build -o bin/depotd.exe ./cmd/depotd
   ```

## Every day

```
bin/depotd.exe hub -data depot-data
```

- **Phones need `-addr :8080`.** By default the hub listens on `127.0.0.1:8080` (this PC only). With
  `-addr :8080` the log prints addresses such as `open: http://192.168.0.29:8080`; open that on the phones.
  **Warning:** the hub uses the dev login, so with a LAN address anyone on that network can sign in as the
  owner. Only do this on a trusted network, never on guest or public Wi-Fi.
- The first time, Windows Firewall asks whether `depotd.exe` may use the network: choose
  **Private networks** and allow. (Do this yourself; nobody should change firewall settings for you.)
- First start downloads PostgreSQL once (needs internet) and takes a minute. Sign in with the dev login
  as `owner@local` (change with `-dev-email`); the hub creates company `local`, depot `own-depot` and
  that owner by itself, and repeats safely at every start.
- No API key yet, or just practising? Add `-engine fake`: every photo reads as `CSQU3054383`.
- Stop with Ctrl+C. Photos that were still being read are read again at the next start, and trucks
  left half-read are finished from what was already read.

Options: `-model` and `-thinking` choose the Gemini model and thinking level (defaults `gemini-3.8-flash`, `low`;
try `-model gemini-3.5-flash-lite` with `bench` to see whether the cheaper model is accurate enough),
`-auto-accept` lets confident, check-digit-valid reads skip the person (off: keep it off while
collecting training data), `-addr` changes the address and port (see the warning above).

## Using it

- **Phone, New gate-in:** pick the lane, start a truck, tap **Rear doors** first (number and ISO
  code are clearest there), then the sides. Tick **Practice** for test trucks: they never enter
  the training data. A warning under a photo means it looks too dark or blurry: retake it.
- **Office, Queue:** everything that needs a person. Open a truck, check the photos (the orange
  box shows where the number was read), fix the number if needed (the check-digit math updates as
  you type) and press Enter. Your name is remembered on that device. The **Reading** tab shows trucks
  whose photos are still being read; if one stays there, open it and press **Read again**.
- If the phone reloads the page while the camera is open (common on low-memory phones), the app
  returns to the same truck and shows the photos already saved.
- **Test read:** try one photo without creating a record. Shows the raw engine answer, time and tokens.

## Against your own database (api mode)

Instead of the hub's built-in database, run `migrate` once as the database owner, then `api`
as the `depot_app` role. Secrets come from flags or environment only, never from files:

```
export DATABASE_OWNER_URL=postgres://owner:...@host/depot
export DATABASE_APP_PASSWORD=...        # chosen password for the depot_app role
bin/depotd.exe migrate -platform-admin you@example.com
export DATABASE_URL=postgres://depot_app:...@host/depot
bin/depotd.exe api -addr :8080 -blobs depot-data/blobs -mail-dir depot-data/mail-out -engine fake
```

`api -env production` refuses `-dev-login` and sets secure cookies; until a real identity provider
exists it also refuses to start, so use `-env development` for now. `-fallback-models a,b` lists
Gemini models to try when the main one is overloaded.

## Photo tips

Fill the frame with the number, keep the phone level, avoid glare on wet paint, and for flat racks
and tanks photograph the end frame where the number is. Night photos need a light.

## Training data and accuracy

```
bin/depotd.exe export-dataset -db "$DATABASE_URL" -company local -blobs depot-data/blobs -out dataset-export
bin/depotd.exe bench -db "$DATABASE_URL" -company local -blobs depot-data/blobs -limit 50 -yes
```

`-db` is the `depot_app` URL of the database to read (see "Against your own database" below).

`export-dataset` copies every verified, non-practice photo with labels (see `dataset-export/README.txt`).
`bench` re-reads verified trucks with the chosen `-model`/`-thinking` and prints accuracy, tokens and
time: use it to compare settings before changing them. It costs one engine call per photo.

## Backup

Stop the hub and copy the whole `depot-data` folder somewhere safe (external disk or cloud drive).
Once a camera is set up, `depot-data` also holds the **camera password** (in the database), so
keep backups private: an encrypted disk, or a cloud folder only you can open.

## Troubleshooting

| Problem | Fix |
|---|---|
| Phone can't open the address | Same Wi-Fi as the PC? Guest Wi-Fi often isolates devices. Firewall allowed for private networks? Is the Wi-Fi marked "Public" in Windows? |
| `GEMINI_API_KEY is not set` | Run `setx ...` in your own terminal and open a new one, or use `-engine fake`. |
| Reason shows `engine rejected the request` with a model name | The model id is wrong or retired: check the current list in the Gemini docs and pass `-model`. If it mentions thinking, run with `-thinking ""`. |
| Reason shows `engine rejected the credentials` | The API key is wrong or revoked: create a new one. |
| Reason shows `engine unavailable` | Internet or Gemini temporarily down (already retried twice): press **Read again** later, or type the number. |
| `The web app is not built yet` | Run `npm --prefix web run build`, then `go build ...` again. |
| Camera opens the front camera | Switch cameras in the phone's camera screen; the app asks for the rear one but some phones ignore it. |

## Fixed camera (Reolink RLC-810WA)

### Prepare the camera (once, in the Reolink app)

1. Power the camera (12 V adapter) and add it to the depot network. A **network cable to the
   router** is more reliable than Wi-Fi; on Wi-Fi use the 5 GHz network.
2. Update the firmware.
3. Settings → Network → Advanced → **Port Settings** (in some app versions: Network Information → Advanced): switch on **HTTP** and
   **RTSP**. New firmware ships with both off.
4. In the router, give the camera a **fixed address** (DHCP reservation). Write it down.
5. Use a strong camera password. Do **not** forward any router port to the camera, and switch
   UPnP off: the camera must only be reachable inside the depot.
6. Detection: draw the **vehicle detection zone** over the gate lane only; switch people and
   animal alarms off; set **anti-flicker to 50 Hz**.

### Mount and aim

- Aim at the **rear doors** of a container standing where trucks stop: the number and the type
  code (e.g. 45G1) are printed there together.
- Mount it **beside** the lane (not in it), 3.5–4.5 m high, pointing slightly down, at most
  about 30° off straight-on, **4–7 m** from the doors. Avoid low sun in the lens.
- Check with **Bu kareyi oku / Read this frame** on the Camera page: the letter height should
  say *iyi / good* (30 px or more). *Sınırda / borderline* (20–29 px): move closer. Under
  20 px it will not read reliably.

| Distance to the doors | Letter height (4K, this lens) |
|---|---|
| 4 m | ≈ 49 px |
| 6 m | ≈ 33 px |
| 8 m | ≈ 25 px |
| 10 m | ≈ 20 px |

### Live video (go2rtc, once)

Live video needs go2rtc next to `depotd.exe` (already done on the depot PC: `bin\go2rtc.exe`). To set it up on another PC, build it from source with Go:

```
go install github.com/AlexxIT/go2rtc@v1.9.14
Copy-Item $env:USERPROFILE\go\bin\go2rtc.exe bin\go2rtc.exe
```

(PowerShell, from `C:\Users\OZGUN\depot-platform`, like the other commands here.)

The hub starts go2rtc by itself, bound to 127.0.0.1 with a password generated at every start. It
asks for no firewall permission and nothing else can use it. Without go2rtc the Camera page
shows a photo refreshed every 2 seconds instead of video.

### Connect it in the app

1. Start the hub as usual and open **Kamera / Camera**.
2. Enter the camera address, user name and password, then **Kaydet ve test et / Save and test**.
   Every line should be green. A red line says what to switch on or fix.
3. Leave **Otomatik fotoğraf** off while aiming. Switch it on when the camera is in place:
   every vehicle in the zone then starts a visit (a 4K photo every second, read until two photos
   agree). A person always confirms camera trucks in this kit.
4. **Şimdi fotoğraf çek / Take photos now** takes 10 photos by hand, for example to test on
   stacked containers when no truck is coming.

Visits where no photo showed a container number (a car, a forklift) are filed as
**Konteyner yok / No container**: not in the review queue, but listed on the Camera page.

### Try it without a camera

```
bin/depotd.exe hub -engine fake -fake-camera -data demo-data -addr :8090
```

A simulated camera "sees" a vehicle for 20 s out of every 40. The demo uses its own `demo-data`
folder and port 8090 (open `http://localhost:8090`), so it never mixes fake trucks or settings into
the real `depot-data`, and it can run next to the real hub.

To test with photos you took yourself, put the `.jpg` files in a folder and point the simulated
camera at it. Each truck visit takes the next photos in name order, so the real engine reads your
real containers without a camera at the gate:

```
bin/depotd.exe hub -fake-camera-photos C:\gate-photos -data demo-data -addr :8090
```

Leave out `-engine fake` to read them with Gemini (it needs `GEMINI_API_KEY`, as for the real hub).

### Camera troubleshooting

| Symptom | Fix |
|---|---|
| "Kameraya erişim" red | Wrong address, camera unpowered or on another network, or HTTP off in Port Settings |
| "Giriş" red / status "Kullanıcı adı veya şifre yanlış" | Re-enter the password. The hub stops trying after a wrong one; the camera locks logins after several wrong tries |
| "Canlı video portu (RTSP)" red | Switch RTSP on in Port Settings |
| Live area shows photos, not video | `bin\go2rtc.exe` missing (the Camera page says so) or the stream failing; the hub log shows `go2rtc:` lines |
| Every visit is "Konteyner yok" | The camera does not see the number: check aim with Read this frame |
| Visits from cars or people | Shrink the detection zone in the Reolink app; switch people alarms off |
| Letter height "çok küçük / too small" | Move the camera closer to where the doors stop (see the table) |

## Gate flow (phone) and yard

Phones open the hub address, sign in, and pick **Gate**.

- **Truck in:** pick **In**, type or scan the container number (the app checks the check digit),
  take the photos, fill in the form (condition, damage, seal, plate, driver) and save.
- **Truck out:** pick **Out**, enter the number. The app checks stock, holds, blocks and the release
  order. If an order lists a quantity, the container is counted against it.
- **No signal:** keep going. Saves wait in the phone's outbox and are sent when the network
  returns; the status line shows how many are waiting. The same save is never recorded twice.
  Offline, the phone can reload the app only if the page is served over HTTPS (the hub is not).
  Do not sign out with entries waiting: the next person who signs in on that phone sends them
  under their own name.
- **Refused trucks.** Set under **Yard rules**, **Refused trucks**:
  - *Clerk overrides*: the clerk passes the truck by writing a reason.
  - *Manager approval*: the phone waits; a manager approves or denies on **Approvals**.
  - *No exceptions*: the truck cannot pass.
  - On any mode, **Refuse** turns the truck away and the office sees it under **Gate issues**.
- **EIR (equipment interchange receipt):** open the visit on **Yard**, use the EIR link, and print
  it to PDF from the browser. Anyone with the link can open it, so share it only with the driver or
  the line.

## Importing stock and orders

Open **Import**. Each card has **Download template**: `stock.csv` and `orders.csv`.
Fill it in Excel and save as **CSV** or **CSV UTF-8**. Separator `,` or `;` both work.

- `stock.csv` columns: `number, type_code, line_code, customer_code, full_empty, in_date, block, row, tier, grade`.
- `orders.csv` columns: `kind, reference, line_code, customer_code, valid_until, number, type_code, full_empty, quantity`.
  One row per item; rows with the same `reference` make one order. `kind` is `release` or `redelivery`.
  Give a `number` for a named container, or `type_code` and `quantity` for a quantity.
- Dates: `dd.MM.yyyy` (31.01.2026) or `yyyy-MM-dd`.
- `full_empty` is `F` or `E`.
- It is all or nothing: if any row is wrong, the page lists each bad row and column and nothing
  is imported. Fix the file and upload it again.

## Trying shipping-line EDI locally

Run the hub as usual. It writes mail to files, so nothing leaves the PC: every message the app
"sends" lands as an `.eml` in `depot-data/mail-out` (change with `-mail-dir`).

1. Open **EDI** (needs the manage permission). Set the company EDI party id (our UNB id, for example `DEPOTX`).
   An EDI line cannot be switched on before this is set.
2. Pick a line, set its mode to **EDI** and fill the profile: sender id `MSC` (what the file's UNB says),
   recipient id, and where to send (any address, it is only a file).
3. Upload `internal/edifact/testdata/coparn_release.edi` on the EDI screen (the line's sender id must match
   the file's UNB sender). The release order appears under **Orders**; an APERAK `.eml` lands in `mail-out`.
   Problems show on the message row.
4. Gate a container of that order out on a phone. After the next worker tick (`-edi-tick`, default 30s)
   a CODECO `.eml` (`BGM+111`) appears in `mail-out`. Open it in any text editor; the EDIFACT is the attachment.

Flags (each has an environment fallback):

| Flag | Env | Meaning |
|---|---|---|
| `-mail` | `MAIL_DRIVER` | `file` (default) or `postmark` |
| `-mail-dir` | `MAIL_DIR` | folder for the file driver (hub default `<data>/mail-out`) |
| `-mail-from` | `MAIL_FROM` | from address, default `depot@localhost` |
| `-edi-tick` | `EDI_TICK` | worker interval, default `30s` |
| | `POSTMARK_SERVER_TOKEN` | needed by `-mail postmark` |
| | `POSTMARK_INBOUND_SECRET` | password of the inbound webhook |
| | `EDI_INBOUND_DOMAIN` | the domain lines mail files to |

Real mail (later, no Postmark account yet): the owner sets Postmark's inbound webhook to
`https://postmark:<secret>@<host>/hooks/postmark/inbound`, where `<secret>` is `POSTMARK_INBOUND_SECRET`.
