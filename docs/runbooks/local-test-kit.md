# Local gate test kit: how to run it

One Windows PC runs `depotd hub`. Phones on the same Wi-Fi open it in the browser,
photograph containers, and an office screen confirms or corrects what was read.
Everything stays on the PC: database and photos are in the `depot-data` folder.

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
bin/depotd.exe hub -data depot-data -access-code 482915
```

- The log prints addresses such as `open: http://192.168.0.29:8080`. Open that on the phones.
- The first time, Windows Firewall asks whether `depotd.exe` may use the network: choose
  **Private networks** and allow. (Do this yourself; nobody should change firewall settings for you.)
- `-access-code` is optional; when set, every phone and PC asks for it once. Use at least 6 characters
  (drivers often get the depot Wi-Fi). After 5 wrong tries a device must wait a minute.
- No API key yet, or just practising? Add `-engine fake`: every photo reads as `CSQU3054383`.
- Stop with Ctrl+C. Photos that were still being read are read again at the next start, and trucks
  left half-read are finished from what was already read.

Options: `-model` and `-thinking` choose the Gemini model and thinking level (defaults `gemini-3.8-flash`, `low`;
try `-model gemini-3.5-flash-lite` with `bench` to see whether the cheaper model is accurate enough),
`-auto-accept` lets confident, check-digit-valid reads skip the person (off: keep it off while
collecting training data), `-addr :8080` changes the port.

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

## Photo tips

Fill the frame with the number, keep the phone level, avoid glare on wet paint, and for flat racks
and tanks photograph the end frame where the number is. Night photos need a light.

## Training data and accuracy

```
bin/depotd.exe export-dataset -data depot-data -out dataset-export
bin/depotd.exe bench -data depot-data -limit 50 -yes
```

`export-dataset` copies every verified, non-practice photo with labels (see `dataset-export/README.txt`).
`bench` re-reads verified trucks with the chosen `-model`/`-thinking` and prints accuracy, tokens and
time: use it to compare settings before changing them. It costs one engine call per photo.

## Backup

Stop the hub and copy the whole `depot-data` folder somewhere safe (external disk or cloud drive).

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
3. Settings → Network Information → Advanced → **Port Settings**: switch on **HTTP** and
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
copy %USERPROFILE%\go\bin\go2rtc.exe bin\go2rtc.exe
```

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
bin/depotd.exe hub -engine fake -fake-camera
```

A simulated camera "sees" a vehicle for 20 s out of every 40.

### Camera troubleshooting

| Symptom | Fix |
|---|---|
| "Kameraya ulaşma" red | Wrong address, camera unpowered or on another network, or HTTP off in Port Settings |
| "Giriş" red / status "Kullanıcı adı veya şifre yanlış" | Re-enter the password. The hub stops trying after a wrong one; the camera locks logins after several wrong tries |
| "Canlı video portu (RTSP)" red | Switch RTSP on in Port Settings |
| Live area shows photos, not video | `bin\go2rtc.exe` missing (the Camera page says so) or the stream failing; the hub log shows `go2rtc:` lines |
| Every visit is "Konteyner yok" | The camera does not see the number: check aim with Read this frame |
| Visits from cars or people | Shrink the detection zone in the Reolink app; switch people alarms off |
| Letter height "çok küçük / too small" | Move the camera closer to where the doors stop (see the table) |
