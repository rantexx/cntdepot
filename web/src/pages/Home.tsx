import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type CameraView, type Move, type MoveDetail } from "../api";
import { CameraOffline, LiveVideo } from "../camera/LiveVideo";
import { GATE_TARGET_S, GateTime, ReadCrop, SignPlate, VisitHead, VisitRow, clock, fmtSeconds, gateSeconds } from "../components";
import { useT } from "../i18n";
import { useMoveEvents } from "../live";
import { readLocal } from "../storage";

const RELOAD_EVERY_MS = 1000; // a camera visit sends a burst of move events
const CAMERA_POLL_MS = 2000; // the vehicle light and the visit in progress

const sameDay = (iso: string, day: Date) => new Date(iso).toDateString() === day.toDateString();

// pickCurrent is the truck the operator should look at now: the visit the
// camera is taking photos of, else the newest truck waiting for a person.
function pickCurrent(moves: Move[], view: CameraView | null): string {
  const visit = view?.status.visit_move;
  if (visit) return visit;
  const waiting = moves.find((m) => m.status === "needs_review");
  if (waiting) return waiting.id;
  return moves.find((m) => m.source === "camera" && (m.status === "reading" || m.status === "capturing"))?.id ?? "";
}

export function Home() {
  const { t, lang } = useT();
  const [moves, setMoves] = useState<Move[]>([]);
  const [view, setView] = useState<CameraView | null>(null);
  const [current, setCurrent] = useState<MoveDetail | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const reloadTimer = useRef(0);

  const loadMoves = useCallback(async () => {
    try {
      setMoves((await api.listMovesBy({ limit: 200 })).moves.filter((m) => !m.is_test));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  }, []);
  const loadCamera = useCallback(async () => {
    try {
      setView(await api.camera());
    } catch {
      setView(null);
    }
  }, []);
  const reloadSoon = useCallback(() => {
    if (reloadTimer.current) return;
    reloadTimer.current = window.setTimeout(() => {
      reloadTimer.current = 0;
      void loadMoves();
    }, RELOAD_EVERY_MS);
  }, [loadMoves]);

  useEffect(() => {
    void loadMoves();
    void loadCamera();
    const timer = window.setInterval(() => void loadCamera(), CAMERA_POLL_MS);
    return () => {
      window.clearInterval(timer);
      window.clearTimeout(reloadTimer.current);
    };
  }, [loadMoves, loadCamera]);
  useMoveEvents(reloadSoon, () => void loadMoves());

  const currentId = pickCurrent(moves, view);
  const listed = moves.find((m) => m.id === currentId);
  useEffect(() => {
    if (!currentId) {
      setCurrent(null);
      return;
    }
    let live = true;
    api.getMove(currentId).then(
      (m) => live && setCurrent(m),
      () => live && setCurrent(null),
    );
    return () => {
      live = false;
    };
  }, [currentId, listed?.updated_at]);

  async function confirm() {
    if (!current) return;
    setBusy(true);
    setError("");
    try {
      await api.resolve(current.id, { action: "accept", by: readLocal("by") || "office" });
      await loadMoves();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  const now = new Date();
  const today = moves.filter((m) => sameDay(m.created_at, now));
  const trucks = today.filter((m) => m.status !== "no_container");
  const count = (s: Move["status"]) => today.filter((m) => m.status === s).length;
  const times = today.map(gateSeconds).filter((s): s is number => s !== null);
  const avg = times.length ? Math.round(times.reduce((a, b) => a + b, 0) / times.length) : null;
  const target = fmtSeconds(GATE_TARGET_S);

  return (
    <div className="page">
      <div className="page-head">
        <h1>
          {t("navLiveGate")}
          <small>{now.toLocaleDateString(lang === "tr" ? "tr-TR" : "en-GB", { day: "numeric", month: "long", year: "numeric" })}</small>
        </h1>
        <div className="key" aria-hidden="true">
          <span>{t("stateKey")}</span>
          <span className="sign reading">{t("status_reading")}</span>
          <span className="sign act">{t("actNow")}</span>
          <span className="sign caution">{t("status_needs_review")}</span>
          <span className="sign safe">{t("status_accepted")}</span>
          <span className="sign stop">{t("status_refused")}</span>
          <span className="sign none">{t("status_no_container")}</span>
        </div>
      </div>

      <div className="today">
        <div>
          <b>{trucks.length}</b>
          <span>{t("todayTrucks")}</span>
        </div>
        <div>
          <b>
            <i className="tile accepted" />
            {count("accepted")}
          </b>
          <span>{t("todayConfirmed")}</span>
        </div>
        <div>
          <b>
            <i className="tile needs_review" />
            {count("needs_review")}
          </b>
          <span>{t("todayWaiting")}</span>
        </div>
        <div>
          <b>
            <i className="tile refused" />
            {count("refused")}
          </b>
          <span>{t("todayRefused")}</span>
        </div>
        <div>
          <div className="target">
            <b>{avg === null ? "—" : fmtSeconds(avg)}</b>
            <GateTime seconds={avg} bare />
          </div>
          <span>{t("todayGateTime").replace("{target}", target)}</span>
        </div>
      </div>

      <section className="live-row">
        <CameraPanel view={view} current={current} />
        <ReadPanel move={current} busy={busy} onConfirm={() => void confirm()} />
      </section>
      {error && <p className="error">{error}</p>}

      <div className="visits-head">
        <h2>{t("recentVisits")}</h2>
        <span>{t("recentHint").replace("{target}", target)}</span>
      </div>
      {moves.length === 0 ? (
        <p className="muted">{t("noMoves")}</p>
      ) : (
        <ul className="visits">
          <VisitHead />
          {moves.slice(0, 10).map((m) => (
            <li key={m.id}>
              <VisitRow move={m} />
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function CameraPanel({ view, current }: { view: CameraView | null; current: MoveDetail | null }) {
  const { t } = useT();
  const state = view?.available ? view.status.state : null;
  const online = state === "online";
  const visiting = !!view?.status.visit_move && current?.id === view.status.visit_move;
  const video = view?.live.state === "running";
  // shortcut: the simulated camera is recognised by its model name, until the hub reports demo mode itself.
  const demo = view?.status.info.model === "Fake camera";
  return (
    <div className="cam">
      <span className="lane-plate">{t("gateLane")}</span>
      <span className="live-badge">
        <i className={online && video ? "on" : ""} />
        {demo ? t("demoBadge") : video ? t("liveBadge") : t("stillsBadge")}
      </span>
      {online && view ? (
        <LiveVideo videoAvailable={view.live.state === "running"} />
      ) : (
        <div className="cam-empty">
          {state && state !== "unconfigured" ? <CameraOffline state={state} /> : <p>{t("camState_unconfigured")}</p>}
          <a className="button" href="#/camera">
            {t("setupCamera")}
          </a>
        </div>
      )}
      {online && (view?.status.vehicle || visiting) && (
        <div className="cam-overlay">
          <span>
            <b>{t("visitDetected")}</b>
            {visiting && current && ` ${clock(current.created_at)}`}
          </span>
          {visiting && current && <span>{t("visitPhotos").replace("{n}", String(current.photos?.length ?? 0))}</span>}
        </div>
      )}
    </div>
  );
}

function ReadPanel({ move, busy, onConfirm }: { move: MoveDetail | null; busy: boolean; onConfirm: () => void }) {
  const { t } = useT();
  if (!move) {
    return (
      <div className="read">
        <div className="read-head">{t("currentRead")}</div>
        <div className="read-empty">{t("noTruckWaiting")}</div>
      </div>
    );
  }
  const waiting = move.status === "needs_review";
  const read = (move.photos ?? []).filter((p) => !p.deferred).length;
  return (
    <div className="read">
      <div className="read-head">
        {t("currentRead")}
        {waiting && ` · ${t("waitingForYou")}`}
      </div>
      <SignPlate status={move.status} number={move.number} />
      <ReadCrop photos={move.photos} attempts={move.attempts} />
      {move.number && (
        <div className="read-check">
          <span className={`sign ${move.check_digit_valid ? "safe" : "stop"}`}>
            {move.check_digit_valid ? t("validTag") : t("invalidTag")}
          </span>
          {move.check_digit_valid ? t("checkValid").replace("{d}", move.number.slice(-1)) : t("checkInvalid")}
        </div>
      )}
      <div className="facts">
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
          <b>{read}</b>
        </div>
        <div>
          <span>{t("confidence")}</span>
          <b>{Math.round(move.confidence * 100)}%</b>
        </div>
      </div>
      <div className="read-actions">
        <button type="button" className="primary" disabled={!waiting || !move.number || busy} onClick={onConfirm}>
          {t("confirmRead")}
        </button>
        <a className="button" href={`#/moves/${move.id}`}>
          {waiting ? t("correctNumber") : t("openRecord")}
        </a>
      </div>
      {move.source === "camera" && <p className="small muted">{t("cameraReadsNote")}</p>}
    </div>
  );
}
