import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import { getMove, rereadMove, resolveMove, type Attempt, type MoveDetail as Detail, type Photo } from "../gen/api";
import { blobUrl, camera, ok, typedNumber, type CameraView } from "../hubApi";
import { CameraOffline, LiveVideo } from "../camera/LiveVideo";
import { CandidateList, CheckDigitMath, GateTime, PhotoWithBox, ReadCrop, SignPlate, StatusChip, gateSeconds } from "../components";
import { useT } from "../i18n";
import { useMoveEvents } from "../live";
import { readLocal, writeLocal } from "../storage";

function latestByPhoto(attempts: Attempt[] | null): Map<string, Attempt> {
  const latest = new Map<string, Attempt>();
  for (const a of attempts ?? []) latest.set(a.photo_id, a); // oldest first, so later wins
  return latest;
}

export function MoveDetail({ id }: { id: string }) {
  const { t } = useT();
  const [move, setMove] = useState<Detail | null>(null);
  // What the person typed, tagged with the move it was typed for. Until they
  // edit the field it follows move.number, so a re-read shows the new number.
  const [typed, setTyped] = useState<{ id: string; text: string } | null>(null);
  const [by, setBy] = useState(() => readLocal("by") ?? "");
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // null: live view hidden; otherwise the hub's camera view when it was
  // opened, or "failed" when the hub could not be asked
  const [live, setLive] = useState<CameraView | "failed" | null>(null);

  const load = useCallback(async () => {
    try {
      const m = await ok(getMove(id));
      setMove(m);
      setError("");
    } catch (e) {
      setError(String(e));
    }
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);
  useMoveEvents(
    (changed) => {
      if (changed === id) void load();
    },
    () => void load(),
  );
  // Poll while the hub is working on the move. Only a camera visit leaves
  // "capturing" by itself; a phone move waits for the clerk's photos.
  const working =
    !!move && (move.status === "reading" || (move.status === "capturing" && move.source === "camera"));
  useEffect(() => {
    if (!working) return;
    const timer = window.setInterval(() => void load(), 3000);
    return () => window.clearInterval(timer);
  }, [working, load]);

  async function resolve(action: "accept" | "correct" | "refuse") {
    if (!move) return;
    setBusy(true);
    setError("");
    try {
      writeLocal("by", by);
      await resolveMove(move.id, { action, number: action === "correct" ? number : undefined, by, note });
      await load();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  async function reread() {
    if (!move) return;
    try {
      await rereadMove(move.id);
      await load();
    } catch (e) {
      setError(String(e));
    }
  }

  async function toggleLive() {
    if (live !== null) {
      setLive(null);
      return;
    }
    try {
      setLive(await camera.get());
    } catch {
      setLive("failed");
    }
  }

  if (!move) return <p>{error || t("loading")}</p>;

  const number = typed && typed.id === id ? typed.text : move.number;
  const setNumber = (text: string) => setTyped({ id, text });
  const latest = latestByPhoto(move.attempts);
  const resolved = move.resolved_by !== "";
  const photos = move.photos ?? [];
  const isCamera = move.source === "camera";
  const readPhotos = photos.filter((p) => !p.deferred);
  const otherPhotos = photos.filter((p) => p.deferred);
  const edited = number.trim().toUpperCase() !== move.number;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void resolve(edited ? "correct" : "accept");
  };
  // Live video logs in to the camera, so it is only opened while the hub's
  // own login works; otherwise a short line says why.
  let livePanel: ReactNode = null;
  if (live === "failed") livePanel = <p className="muted">{t("networkError")}</p>;
  else if (live && !live.available) livePanel = <p className="muted">{t("camNotAvailable")}</p>;
  else if (live && live.status.state !== "online") livePanel = <CameraOffline state={live.status.state} />;
  else if (live) livePanel = <LiveVideo videoAvailable={live.live.state === "running"} />;

  return (
    <div className="page">
      <div className="page-head">
        <a className="back" href="#/queue">
          {t("backToQueue")}
        </a>
        <div className="row" style={{ marginTop: 0, alignItems: "center" }}>
          <StatusChip status={move.status} />
          {isCamera && <span className="chip">{t("cameraTag")}</span>}
          {move.is_test && <span className="chip">{t("testTag")}</span>}
          <GateTime seconds={gateSeconds(move)} />
        </div>
      </div>
      <div className="card" style={{ marginTop: 16 }}>
        <SignPlate status={move.status} number={move.number} />
        <ReadCrop photos={move.photos} attempts={move.attempts} />
        {isCamera && (
          <div className="row">
            <button type="button" onClick={() => void toggleLive()}>
              {t("camLive")}
            </button>
          </div>
        )}
        {livePanel}
        <div className="facts" style={{ marginTop: 14 }}>
          <div>
            <span>{t("typeCode")}</span>
            <b>{move.type_code || "—"}</b>
          </div>
          <div>
            <span>{t("color")}</span>
            <b>{move.color || "—"}</b>
          </div>
          <div>
            <span>{t("photosRead")}</span>
            <b>{readPhotos.length}</b>
          </div>
          <div>
            <span>{t("confidence")}</span>
            <b>{Math.round(move.confidence * 100)}%</b>
          </div>
        </div>
        {move.reason && (
          <p className="muted">
            {t("reason")}: {move.reason}
          </p>
        )}
        {resolved ? (
          <div>
            <p>
              <span className={`sign ${move.verified ? "safe" : "caution"}`}>{move.verified ? t("verified") : t("unverified")}</span>
            </p>
            {move.corrected_from && (
              <p>
                {t("correctedFrom")} <span className="mono">{move.corrected_from}</span>
              </p>
            )}
            <p>
              {t("resolvedBy")} {move.resolved_by}
              {move.note && ` · ${move.note}`}
            </p>
          </div>
        ) : (
          <form onSubmit={submit}>
            <CandidateList candidates={move.result.candidates ?? null} onPick={setNumber} />
            <label>
              {t("number")}
              <input
                className="mono big-input"
                value={number}
                onChange={(e) => setNumber(typedNumber(e.target.value))}
                autoFocus
              />
            </label>
            <CheckDigitMath number={number} />
            <label>
              {t("yourName")}
              <input value={by} onChange={(e) => setBy(e.target.value)} />
            </label>
            <label>
              {t("note")}
              <input value={note} onChange={(e) => setNote(e.target.value)} />
            </label>
            <div className="row">
              <button type="submit" className="primary" disabled={busy || !number.trim()}>
                {edited ? t("correct") : t("accept")}
              </button>
              <button type="button" className="danger" disabled={busy} onClick={() => void resolve("refuse")}>
                {t("refuse")}
              </button>
              <button type="button" disabled={busy} onClick={() => void reread()}>
                {t("reread")}
              </button>
            </div>
          </form>
        )}
        {error && <p className="error">{error}</p>}
      </div>
      <h3>{t("photos")}</h3>
      <div className="photo-grid">
        {readPhotos.map((p) => (
          <PhotoCard key={p.id} photo={p} attempt={latest.get(p.id)} chosen={isCamera} />
        ))}
      </div>
      {otherPhotos.length > 0 && (
        <details>
          <summary>
            {t("otherPhotos")} ({otherPhotos.length})
          </summary>
          <div className="photo-grid">
            {otherPhotos.map((p) => (
              <PhotoCard key={p.id} photo={p} attempt={undefined} chosen={false} />
            ))}
          </div>
        </details>
      )}
    </div>
  );
}

function PhotoCard({ photo, attempt, chosen }: { photo: Photo; attempt: Attempt | undefined; chosen: boolean }) {
  const { t } = useT();
  const reading = attempt?.reading;
  return (
    <figure className="card">
      <PhotoWithBox src={blobUrl(photo.ai_sha256, 640)} box={reading?.box} alt={photo.role} />
      <figcaption>
        <b>{photo.role}</b> {chosen && attempt && !attempt.error && <span className="chip">{t("readTag")}</span>}
        {attempt?.error && (
          <p className="error">
            {t("error")}: {attempt.error}
          </p>
        )}
        {attempt && !attempt.error && (
          <p className="small">
            {(reading?.candidates ?? []).map((c) => `${c.number} ${Math.round(c.confidence * 100)}%`).join(", ") ||
              t("noNumber")}
            {reading?.type_code && ` · ${reading.type_code}`}
            {reading?.color && ` · ${reading.color}`}
          </p>
        )}
        {attempt && (
          <details className="small muted">
            <summary>{t("engineDetails")}</summary>
            {attempt.engine}/{attempt.model} · {attempt.latency_ms} ms
          </details>
        )}
        <a href={blobUrl(photo.sha256)} target="_blank" rel="noreferrer" className="small">
          {t("original")}
        </a>
      </figcaption>
    </figure>
  );
}
