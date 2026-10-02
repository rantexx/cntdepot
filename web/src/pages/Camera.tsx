import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import {
  api,
  ApiError,
  blobUrl,
  type CameraRead,
  type CameraReport,
  type CameraUpdate,
  type CameraView,
  type Move,
} from "../api";
import { CameraOffline, LiveVideo } from "../camera/LiveVideo";
import { CandidateList, PhotoWithBox, StatusChip } from "../components";
import { useT, type Key } from "../i18n";
import { useMoveEvents } from "../live";

type Action = "save" | "test" | "auto" | "read" | "capture";

const VISITS_EVERY_MS = 2000; // move events reload the visit list at most this often

const errorText = (e: unknown) => (e instanceof ApiError ? e.message : String(e));

export function Camera() {
  const { t } = useT();
  const [view, setView] = useState<CameraView | null>(null);
  const [form, setForm] = useState<{ host: string; user: string; password: string } | null>(null);
  const [report, setReport] = useState<CameraReport | null>(null);
  const [read, setRead] = useState<CameraRead | null>(null);
  const [visits, setVisits] = useState<Move[]>([]);
  const [busy, setBusy] = useState<Action | "">("");
  const [error, setError] = useState<{ action: Action; message: string } | null>(null); // the last failed action
  const [pollError, setPollError] = useState("");
  const polling = useRef(false); // a status poll is on its way
  const writing = useRef(false); // a settings save is on its way
  const writes = useRef(0); // settings saves started; a poll older than the last one is dropped
  const visitsTimer = useRef(0);
  const visitsAt = useRef(0);

  const load = useCallback(async () => {
    if (polling.current || writing.current) return;
    polling.current = true;
    const gen = writes.current;
    try {
      const v = await api.camera();
      if (gen !== writes.current) return; // asked before a save; it could undo the save on screen
      setView(v);
      setForm((f) => f ?? { host: v.settings.host, user: v.settings.user, password: "" });
      setPollError("");
    } catch (e) {
      setPollError(errorText(e));
    } finally {
      polling.current = false;
    }
  }, []);
  const loadVisits = useCallback(async () => {
    try {
      setVisits((await api.listMovesBy({ source: "camera", limit: 20 })).moves);
    } catch {
      // the status poll reports connection problems
    }
  }, []);
  // A visit sends a burst of move events; reload at most every VISITS_EVERY_MS.
  const reloadVisitsSoon = useCallback(() => {
    if (visitsTimer.current) return;
    const wait = Math.max(0, visitsAt.current + VISITS_EVERY_MS - Date.now());
    visitsTimer.current = window.setTimeout(() => {
      visitsTimer.current = 0;
      visitsAt.current = Date.now();
      void loadVisits();
    }, wait);
  }, [loadVisits]);

  useEffect(() => {
    void load();
    void loadVisits();
    const timer = window.setInterval(() => void load(), 1000); // the "vehicle seen" light
    return () => window.clearInterval(timer);
  }, [load, loadVisits]);
  useEffect(
    () => () => {
      window.clearTimeout(visitsTimer.current);
      visitsTimer.current = 0;
    },
    [],
  );
  useMoveEvents(
    () => reloadVisitsSoon(),
    () => void loadVisits(),
  );

  async function run(name: Action, fn: () => Promise<void>) {
    setBusy(name);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError({ action: name, message: errorText(e) });
    } finally {
      setBusy("");
    }
  }

  // saveSettings writes camera settings. Status polls wait for it, and a poll
  // already on its way is dropped, so a stale answer cannot flip the page back.
  async function saveSettings(u: CameraUpdate) {
    writing.current = true;
    writes.current += 1;
    try {
      setView(await api.saveCamera(u));
    } finally {
      writing.current = false;
    }
  }

  const save = (e: FormEvent) => {
    e.preventDefault();
    if (!form) return;
    void run("save", async () => {
      await saveSettings({ host: form.host, user: form.user, password: form.password });
      setForm((f) => f && { ...f, password: "" });
      setReport(await api.testCamera());
    });
  };

  const errorFor = (...actions: Action[]) =>
    error && actions.includes(error.action) ? <p className="error">{error.message}</p> : null;

  if (!view) return <p>{pollError || t("loading")}</p>;
  if (!view.available) return <p>{t("camNotAvailable")}</p>;
  const s = view.status;
  const configured = s.state !== "unconfigured";
  const online = s.state === "online";
  const light = online ? "on" : s.state === "offline" || s.state === "auth" ? "bad" : "";
  const since =
    configured && !online && s.since
      ? t("camSince").replace("{time}", new Date(s.since).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }))
      : "";

  return (
    <section>
      {pollError && <p className="error">{pollError}</p>}
      <div className="card">
        <h3>{t("camSetup")}</h3>
        <p>
          <span className={`light ${light}`} />
          {t(`camState_${s.state}` as Key)}
          {since && ` · ${since}`}
          {s.info.model && ` · ${s.info.model} ${s.info.firmware}`}
          {s.info.width > 0 && ` · ${s.info.width}×${s.info.height}`}
        </p>
        {s.error && <p className="small muted">{s.error}</p>}
        {form && (
          <form onSubmit={save}>
            <label>
              {t("camHost")}
              <input
                value={form.host}
                onChange={(e) => setForm({ ...form, host: e.target.value })}
                placeholder="192.168.1.50"
                autoComplete="off"
              />
            </label>
            <label>
              {t("camUser")}
              <input value={form.user} onChange={(e) => setForm({ ...form, user: e.target.value })} autoComplete="off" />
            </label>
            <label>
              {t("camPassword")}
              <input
                type="password"
                value={form.password}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
                placeholder={view.settings.password_set ? t("camPasswordSaved") : ""}
                autoComplete="new-password"
              />
            </label>
            <div className="row">
              <button type="submit" className="primary" disabled={busy !== "" || !form.host.trim() || !form.user.trim()}>
                {t("camSaveTest")}
              </button>
              {configured && (
                <button
                  type="button"
                  disabled={busy !== ""}
                  onClick={() => void run("test", async () => setReport(await api.testCamera()))}
                >
                  {t("camTest")}
                </button>
              )}
            </div>
          </form>
        )}
        {(busy === "save" || busy === "test") && <p>{t("loading")}</p>}
        {errorFor("save", "test")}
        {report && (
          <div>
            <ul className="steps">
              {(report.steps ?? []).map((st) => (
                <li key={st.name} className={st.ok ? "ok" : "bad"}>
                  <b>{t(`camStep_${st.name}` as Key)}</b>
                  {st.detail && ` · ${st.detail}`}
                </li>
              ))}
            </ul>
            <details>
              <summary>{t("camRaw")}</summary>
              <pre>{report.raw}</pre>
            </details>
          </div>
        )}
      </div>

      {configured && (
        <div className="card">
          <h3>{t("camLive")}</h3>
          <p>
            <span className={`light ${s.vehicle ? "on" : ""}`} />
            {s.vehicle ? t("camVehicle") : t("camNoVehicle")}
            {s.detection === "motion" && ` · ${t("camMotionOnly")}`}
          </p>
          {/* Video and stills log in to the camera, so they wait until the
              hub's own login works: a wrong password is not retried. */}
          {online ? (
            <>
              <LiveVideo videoAvailable={view.live.state === "running"} />
              {view.live.state !== "running" && view.live.reason && (
                <p className="small muted">{view.live.reason}</p>
              )}
            </>
          ) : (
            <CameraOffline state={s.state} />
          )}
          <label className="check">
            <input
              type="checkbox"
              checked={view.settings.auto}
              disabled={busy !== ""}
              onChange={(e) => {
                const auto = e.target.checked;
                void run("auto", () => saveSettings({ auto }));
              }}
            />
            {t("camAuto")}
          </label>
          <div className="row">
            <button
              type="button"
              className="primary"
              disabled={busy !== ""}
              onClick={() => void run("read", async () => setRead(await api.readCamera()))}
            >
              {t("camReadFrame")}
            </button>
            <button
              type="button"
              disabled={busy !== "" || !!s.visit_move}
              onClick={() =>
                void run("capture", async () => {
                  const r = await api.captureCamera();
                  window.location.hash = `#/moves/${r.move_id}`;
                })
              }
            >
              {t("camCaptureNow")}
            </button>
          </div>
          {busy === "read" && <p>{t("status_reading")}</p>}
          {errorFor("read", "capture", "auto")}
          {read && <ReadResult read={read} />}
        </div>
      )}

      {configured && (
        <div className="card">
          <h3>{t("camRecent")}</h3>
          {visits.length === 0 && <p className="muted">{t("noMoves")}</p>}
          <ul className="queue">
            {visits.map((m) => {
              const first = m.photos?.find((p) => !p.deferred) ?? m.photos?.[0];
              return (
                <li key={m.id}>
                  <a href={`#/moves/${m.id}`} className="queue-row">
                    {first ? <img src={blobUrl(first.ai_sha256, 320)} alt="" loading="lazy" /> : <div className="noimg" />}
                    <div>
                      <div className="mono strong">{m.number || t("noNumber")}</div>
                      <StatusChip status={m.status} />
                      <div className="muted small">
                        {new Date(m.created_at).toLocaleString()} · {(m.photos ?? []).length} {t("camPhotos")}
                      </div>
                    </div>
                  </a>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </section>
  );
}

// ReadResult shows "read this frame": the still with the number box, the
// reading, and whether the letters are big enough.
function ReadResult({ read }: { read: CameraRead }) {
  const { t } = useT();
  const r = read.result;
  return (
    <div>
      <PhotoWithBox src={blobUrl(read.still, 1280)} box={r.attempt.reading.box} alt="" />
      {r.error && (
        <p className="error">
          {t("error")}: {r.error}
        </p>
      )}
      <CandidateList candidates={r.frame.candidates} />
      <p>
        {t("camLetter")}: <b>{read.letter_px > 0 ? `${read.letter_px} px` : "–"}</b>
        {read.verdict && ` · ${t(`camVerdict_${read.verdict}` as Key)}`}
      </p>
      <p className="small">
        {t("typeCode")}: {r.frame.type_code || "–"} · {t("color")}: {r.frame.color || "–"} · {r.attempt.latency_ms} ms
      </p>
    </div>
  );
}
