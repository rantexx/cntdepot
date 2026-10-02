# Gate camera for the local test kit: design

**Status:** approved by the owner 2026-10-02 (approach A). Builds on the local gate test kit (branch `feat/local-gate-test-kit`).
**Brings forward** part of Phase 2 of the main design (`2026-09-30-gate-ocr-phase-0-1-design.md`: fixed cameras, live video with go2rtc on the hub).

## 1. Goal and scope

The owner has a **Reolink RLC-810WA** and wants to test how well a fixed camera reads containers at their own gate:

- connect the camera to the hub and see **live video** in the web app;
- let the camera's **own vehicle detection** start a photo burst that is read automatically;
- **aim and tune** the camera with on-screen help.

**In scope:** one camera per hub, Reolink only (behind a brand-neutral interface), gate-in, local network only.
**Out of scope (later):** exits and direction detection, several cameras and lanes, other brands (Hikvision, Dahua), remote viewing over the internet (TURN), recording clips, HTTPS to the camera, retention clean-up, auto-accept of camera reads.

### Camera facts (RLC-810WA)

| Item | Value |
|---|---|
| Image | 3840 × 2160 (8 MP), main stream H.265; sub stream H.264 |
| Lens | 4.0 mm fixed, 88.8° horizontal, 45.2° vertical |
| Network | Wi-Fi 6 (2.4/5 GHz) and one 10/100 RJ45 port |
| Power | 12 V DC adapter (no PoE) |
| Detection | Built-in person / vehicle / animal detection |
| Interfaces | HTTP JSON API (`/cgi-bin/api.cgi`), RTSP (554), ONVIF. **Ports are off by default** in current firmware: enable HTTP and RTSP under Settings → Network → Advanced → Port Settings |

### Placement guidance (goes into the runbook)

- Aim at the **rear doors**: number and ISO type code are printed there together; the cab hides the front; headlights dazzle at night.
- Mount **beside the lane**, not in it (the next truck would block the view), 3.5–4.5 m high, pointing slightly down, at most about 30° off straight-on.
- Keep **4–7 m** between the camera and the doors. ISO marks are at least 100 mm tall; with this lens at 4K they come out at about 49 px at 4 m, 33 px at 6 m, 25 px at 8 m, 20 px at 10 m.
- Settings: anti-flicker 50 Hz (Turkey); vehicle detection zone over the lane only; people and animal alarms off; avoid low sun in the lens.
- Network: cable to the router preferred; on Wi-Fi use 5 GHz. Fixed address (DHCP reservation). No port forwarding, UPnP off.

## 2. What people see

### 2.1 Camera page (`#/camera`, new tile on Home)

1. **Setup:** address, user name, password; **Save and test**. The test shows one line per step: reach the camera → log in → device info (model, firmware) → ports (RTSP on?) → detection support (vehicle, or motion only) → 4K still. Each failure says what to do in plain words (for example "HTTP is off: turn it on in the Reolink app under Port Settings"). A **Show raw answer** fold-out shows the camera's last JSON reply for troubleshooting. The password is never shown again; the page says "password saved".
2. **Live video:** smooth video from the sub stream, about half a second behind. A **Vehicle seen** light (polled every second) and a status line (connected / offline since 10:42 / wrong password / not set up).
3. **Automatic photos** switch: **off by default**, so aiming the camera does not create trucks. When on, vehicle detection starts visits (§3).
4. **Read this frame:** takes a 4K still now and reads it without creating a truck. Shows the still with the number box, the reading, and the **letter height in pixels** with a verdict: good (≥ 30 px), borderline (20–29 px), too small (< 20 px).
5. **Take photos now:** starts a visit by hand (same burst as a vehicle would).
6. **Recent camera visits:** last 20 camera moves: time, photo count, outcome (number read / needs review / No container), linking to the truck screen.

If live video is unavailable (go2rtc missing or the stream fails) the video area shows a still refreshed every 2 seconds and says why.

### 2.2 Queue and truck screen

- Camera moves carry a **Camera** badge.
- Moves with outcome **No container** are left out of the Review tab; the All tab and the Camera page list them.
- The truck screen shows camera photos in time order, marks which ones were read, and has a **Live** button that opens the live video.
- A person always confirms camera moves in this kit: they are never auto-accepted, whatever `-auto-accept` says.

## 3. Visit logic

A **visit** is one vehicle pass. Defaults (constants, tunable later):

| Setting | Default |
|---|---|
| Ask the camera "vehicle now?" | every 500 ms |
| Photo interval during a visit | 1 s (next still starts when the previous finished, if it took longer) |
| Photos per visit | at most 15 |
| Visit ends | vehicle absent for 3 s, or 30 s after it started |
| Engine reads per visit | at most 8, two at a time |
| Early stop | two read photos agree on the same check-digit-valid number |

1. Vehicle state turns on (and automatic photos are on, and no visit is running, and the previous visit's vehicle has cleared) → create a move with `source = camera`, status `capturing`, and take the first still immediately.
2. Every still is stored as a photo with role `rear` and `want_read = 0`.
3. **Choosing what to read:** whenever fewer than two reads are in flight, fewer than 8 have been made and no early stop happened, pick a photo: while the visit runs, the **newest** unread photo; after it ended, unread photos spread **evenly** over the visit. The chosen photo gets `want_read = 1` and goes to the engine.
4. Camera photos go to the engine **at full resolution** with Gemini's per-part `mediaResolution` set to `MEDIA_RESOLUTION_ULTRA_HIGH` (small marks at distance). Phone photos keep the existing 1568 px copy and default resolution.
5. **Settling** (after the visit ended and every chosen read finished): if no read found any container number → status `no_container`. Otherwise the normal reconcile result, with status `needs_review` (never auto-accept).
6. A vehicle still present after the 30 s cap does not start a new visit until the state has been off for 3 s.
7. **Restart:** a visit cut short by a hub restart ends at the restart. Photos already chosen (`want_read = 1`) but unread are queued as today; unchosen photos are never read automatically. The move then settles as in step 5.
8. **Take photos now** runs steps 1–5 without waiting for the vehicle state; the visit ends after 10 photos.

Unread photos are kept: they are training data for the owner's own model.

## 4. Architecture

```
Reolink camera ──HTTP API──▶ camera.Reolink ──▶ gatecam.Watcher ──▶ gate.Service ──▶ store / blobs / engine
      │                                              ▲
      └──RTSP sub stream──▶ go2rtc (127.0.0.1 only) ─┼─WebSocket─▶ httpapi /api/live/ws ──▶ browser (MSE player)
                                                     └── httpapi /api/camera/* (setup, status, read, capture)
```

### 4.1 `internal/camera`

```go
type Camera interface {
    Info(ctx) (Info, error)              // model, firmware, image size, ports, detection support
    Snapshot(ctx) ([]byte, error)        // full-resolution JPEG
    Detect(ctx) (Detection, error)       // vehicle present now (or motion when vehicle detection is unsupported)
    LiveSource() string                  // RTSP URL of the sub stream, credentials included
}
```

- **`Reolink`**: talks to `http://<host>/cgi-bin/api.cgi`.
  - Login: `POST ?cmd=Login` with `[{"cmd":"Login","action":0,"param":{"User":{"userName":…,"password":…}}}]`; keep `value.Token.name` until `leaseTime` minus 60 s; log in again on `rspCode -6` ("please login first").
  - Snapshot: `GET ?cmd=Snap&channel=0&rs=<random>&token=…`.
  - Detection: `GetAiState` (channel 0), accepting both reply shapes: old (`vehicle_state`) and new (`vehicle: {alarm_state, support}`). When vehicle detection is unsupported, use `GetMdState` (motion) and report `Detection.Kind = motion`.
  - Info: `GetDevInfo` (model, firmVer), `GetNetPort` (rtspEnable, httpEnable), `GetEnc` (main stream size).
  - Wrong user name or password: return `ErrAuth` and **stop polling** until the settings change (avoid the camera's own lock-out).
  - Live source: `rtsp://<user>:<pass>@<host>:554/h264Preview_01_sub`, credentials URL-escaped.
  - Every reply's raw JSON (with the token removed) is kept for **Show raw answer**.
- **`Fake`**: scripted camera for tests and the demo engine mode: serves test JPEGs, scripted vehicle on/off, failure injection.

### 4.2 `internal/gatecam`

- **Watcher** runs while a camera is configured. It polls `Detect`, keeps a status (state, since, last error, vehicle now, detection kind, info) for the API, and runs visits (§3) through the gate service. Its clock is injectable for tests.
- **Visit** tracks one move: photos taken, reads in flight and made, early stop, and settling.

### 4.3 `internal/gate` changes

- `NewCameraMove(ctx, actor)` → move with `source = camera`.
- `AddFrame(ctx, moveID, jpeg, width, height)` → stores a photo with `want_read = 0`; the original doubles as the engine copy.
- `ReadFrame(ctx, photoID)` → sets `want_read = 1` and queues it; the visit learns about finished reads through a callback the service calls after storing each attempt.
- `FinishVisit(ctx, moveID)` → marks capturing done so `recompute` may settle the move.
- `recompute`: photos with `want_read = 0` are neither pending nor frames. Camera moves: never auto-accept; settle to `no_container` when no read found a number; stay `capturing`/`reading` until the visit is finished.
- `Start`: re-queue only `want_read = 1` photos without attempts; finish camera moves still `capturing`.
- `Reread`: re-reads only `want_read = 1` photos.

### 4.4 `internal/recognition` change

`Image` gains `Detail` (`""` or `"ultra_high"`). Gemini sends `"mediaResolution": {"level": "MEDIA_RESOLUTION_ULTRA_HIGH"}` on that part when set. The fake engine ignores it.

### 4.5 Store (migration v2, appended)

- `gate_moves.source TEXT NOT NULL DEFAULT 'phone'`
- `photos.want_read INTEGER NOT NULL DEFAULT 1`
- new status value `no_container`
- `settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL)` holding the camera host, user, password and the automatic-photos switch
- `ListFilter.Source` for `GET /api/moves?source=camera`

### 4.6 Live video (`internal/live`)

- **Supervisor** starts `go2rtc` (pinned **v1.9.14**, MIT) when a camera is configured and the binary exists (`bin\go2rtc.exe` next to `depotd.exe`, overridable with `-go2rtc`). It writes `go2rtc.yaml` into the data folder:
  - `api: listen: "127.0.0.1:<free port>"`;
  - `rtsp: listen` and `webrtc: listen` on `127.0.0.1:0`, so nothing listens on the network;
  - one stream named `gate` = the camera's `LiveSource()`.
  It restarts go2rtc on exit with backoff (1 s doubling to 30 s), restarts it when camera settings change, and stops it with the hub. If v1.9.14 supports restricting API paths, only `/api/ws` is allowed.
- **Proxy:** `GET /api/live/ws` is reverse-proxied to go2rtc `ws://127.0.0.1:<port>/api/ws?src=gate`. The stream name is fixed on the server, and **no other go2rtc path is exposed**. Host and Origin are rewritten to go2rtc's address. The existing host guard and access code apply.
- **Browser player** (`web/src/live/`): small MSE player for go2rtc's protocol:
  1. send `{"type":"mse","value":"<supported codecs>"}`;
  2. receive `{"type":"mse","value":"<mime>"}`;
  3. append binary fMP4 to a SourceBuffer, keeping about 5 s and staying at the live edge.
  `ManagedMediaSource` is used where available (iPhone, iOS 17.1+). It reconnects with backoff, and falls back to the refreshing still after repeated failures.
- **Installing go2rtc:** `go install github.com/AlexxIT/go2rtc@v1.9.14`, then copy the binary to `bin\go2rtc.exe`. The runbook documents it, and the owner approves the download.

### 4.7 HTTP API (all under the existing guards)

| Method and path | Purpose |
|---|---|
| `GET /api/camera` | settings (no password; `passwordSet`), automatic-photos switch, watcher status, live availability and reason |
| `PUT /api/camera` | save host, user, password (empty keeps the saved one), automatic switch; reconnects |
| `POST /api/camera/test` | run the setup test steps now; returns steps, info and raw answer |
| `POST /api/camera/read` | 4K still and engine read, no move created; returns still digest, attempt, frame, letter height and verdict |
| `POST /api/camera/capture` | start a manual visit; returns the move id |
| `GET /api/camera/still` | current still (fallback live view) |
| `GET /api/live/ws` | live video WebSocket (proxied) |
| `GET /api/moves?source=camera` | camera moves |

**Letter height:** box height in pixels (box × image size) when the box is at least as wide as it is tall (one line). Otherwise box height ÷ 11 (number written vertically).

## 5. Errors

| Situation | Behaviour |
|---|---|
| Camera unreachable | Status "offline since …"; retry every 5 s; a running visit skips failed stills and carries on |
| Wrong user name or password | Status "wrong user name or password"; polling stops until settings change |
| HTTP port off / RTSP off | Setup test names the switch to turn on |
| Token expired / camera rebooted | Log in again transparently |
| Snapshot slower than 1 s | Next still starts when it finishes; per-still timeout 5 s |
| go2rtc missing, crashed or stream failing | Supervisor restarts it with backoff; the page falls back to refreshing stills and says why |
| Engine down | Photos stay stored; Reread works as today |
| Vehicle parked in the zone | Visit capped at 30 s; next visit only after it clears |

## 6. Security

- The camera password is stored only in the hub's local database and `go2rtc.yaml` inside the data folder. It is never sent to a browser, never logged, and redacted from raw answers and errors.
- go2rtc listens on 127.0.0.1 only (no firewall prompt, not reachable from the LAN). Only the single fixed WebSocket path is proxied.
- Hub ↔ camera uses HTTP on the LAN in this kit. HTTPS with the camera certificate pinned on first use comes later.
- The runbook tells the owner to keep the camera off the internet (no port forwarding, UPnP off) and to use a strong camera password.

## 7. Testing

- **Reolink adapter:** `httptest` fake camera covering login and token reuse, `rspCode -6` re-login, both `GetAiState` shapes, motion fallback, Snap, wrong password (no further polling), HTTP errors, and redaction of the token and password in raw answers.
- **Watcher and visits** with a fake clock and fake camera:
  - visit start and end, the 30 s cap and the clear-before-next rule;
  - frame choice (newest, then evenly spread);
  - early stop on two agreeing valid reads;
  - `no_container` settling, the manual 10-photo visit, and restart recovery.
- **Gate service:** `want_read` rules in recompute, Start and Reread; camera moves never auto-accept.
- **Store:** migration v2 on a v1 database; settings round trip; source filter.
- **Live:** proxy to a fake go2rtc WebSocket server (fixed `src`, Origin rewrite, other paths refused); supervisor restart and backoff with a fake process.
- **Gemini:** request carries `mediaResolution` only when `Detail` is set.
- **Web:** typecheck and build; manual check of the Camera page with `depotd hub -engine fake -fake-camera` (the hub flag `-fake-camera` replaces the saved camera with `camera.Fake`, which serves demo stills and toggles a vehicle every 20 s; live video then shows the fallback still).
- **Owner's check with the real camera:**
  1. the setup test is all green;
  2. live video plays on the PC and a phone;
  3. Read this frame on a stacked container shows the letter height;
  4. a vehicle pass with automatic photos on creates a camera move that reads the number.

## 8. Later

Exits and direction, several cameras per lane with roles (left, right, rear, top), Hikvision and Dahua adapters, ONVIF events, recording evidence clips, remote live view through the cloud (TURN), HTTPS to cameras, retention of unread photos, auto-accept rules for camera reads, and the camera-health checks of the main design (focus, infrared).
