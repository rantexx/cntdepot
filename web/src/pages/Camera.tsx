import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, ApiError, blobUrl, type CameraRead, type CameraReport, type CameraView, type Move } from "../api";
import { LiveVideo } from "../camera/LiveVideo";
import { CandidateList, PhotoWithBox, StatusChip } from "../components";
import { useT, type Key } from "../i18n";
import { useMoveEvents } from "../live";

export function Camera() {
  const { t } = useT();
  const [view, setView] = useState<CameraView | null>(null);
  const [form, setForm] = useState<{ host: string; user: string; password: string } | null>(null);
  const [report, setReport] = useState<CameraReport | null>(null);
  const [read, setRead] = useState<CameraRead | null>(null);
  const [visits, setVisits] = useState<Move[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const v = await api.camera();
      setView(v);
      setForm((f) => f ?? { host: v.settings.host, user: v.settings.user, password: "" });
    } catch (e) {
      setError(String(e));
    }
  }, []);
  const loadVisits = useCallback(async () => {
    try {
      setVisits((await api.listMovesBy({ source: "camera", limit: 20 })).moves);
    } catch {
      // the status poll reports connection problems
    }
  }, []);

  useEffect(() => {
    void load();
    void loadVisits();
    const timer = window.setInterval(() => void load(), 1000); // the "vehicle seen" light
    return () => window.clearInterval(timer);
  }, [load, loadVisits]);
  useMoveEvents(
    () => void loadVisits(),
    () => void loadVisits(),
  );

  async function run(name: string, fn: () => Promise<void>) {
    setBusy(name);
    setError("");
    try {
      await fn();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy("");
    }
  }

  const save = (e: FormEvent) => {
    e.preventDefault();
    if (!form) return;
    void run("save", async () => {
      setView(await api.saveCamera({ host: form.host, user: form.user, password: form.password }));
      setForm({ ...form, password: "" });
      setReport(await api.testCamera());
    });
  };

  if (!view) return <p>{error || t("loading")}</p>;
  if (!view.available) return <p>{t("camNotAvailable")}</p>;
  const s = view.status;
  const configured = s.state !== "unconfigured";
  const light = s.state === "online" ? "on" : configured ? "bad" : "";

  return (
    <section>
      <div className="card">
        <h3>{t("camSetup")}</h3>
        <p>
          <span className={`light ${light}`} />
          {t(`camState_${s.state}` as Key)}
          {configured && s.state !== "online" && s.since && ` · ${t("camSince")} ${new Date(s.since).toLocaleTimeString()}`}
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
          <LiveVideo videoAvailable={view.live.state === "running"} />
          {view.live.state !== "running" && view.live.reason && <p className="small muted">{view.live.reason}</p>}
          <label className="check">
            <input
              type="checkbox"
              checked={view.settings.auto}
              disabled={busy !== ""}
              onChange={(e) => {
                const auto = e.target.checked;
                void run("auto", async () => setView(await api.saveCamera({ auto })));
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
          {read && <ReadResult read={read} />}
        </div>
      )}

      {error && <p className="error">{error}</p>}

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
                        {new Date(m.created_at).toLocaleTimeString()} · {(m.photos ?? []).length} {t("camPhotos")}
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
