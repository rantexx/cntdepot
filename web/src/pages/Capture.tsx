import { useCallback, useEffect, useState } from "react";
import { addPhoto, createMove, enterNumber, getMove, type MoveDetail } from "../gen/api";
import { ApiError, blobUrl, ok, timeout, typedNumber, UPLOAD_TIMEOUT_MS } from "../hubApi";
import { useSession } from "../session";
import { CandidateList, CheckDigitMath, StatusChip } from "../components";
import { useT, type Key } from "../i18n";
import { preparePhoto, type Quality } from "../image";
import { useMoveEvents } from "../live";
import { localId, readLocal, readSession, removeSession, writeLocal, writeSession } from "../storage";

const SHOTS: { role: string; label: Key }[] = [
  { role: "rear", label: "shotRear" },
  { role: "left", label: "shotLeft" },
  { role: "right", label: "shotRight" },
  { role: "front", label: "shotFront" },
  { role: "other", label: "shotOther" },
];

// The truck being photographed, remembered per tab so a reload (or a trip to the
// queue and back) does not lose it.
const CURRENT_KEY = "currentMove";

interface Shot {
  key: string;
  role: string;
  file: File;
  preview: string;
  status: "uploading" | "done" | "failed";
  photoId?: string;
  quality?: Quality;
  error?: string;
}

export function Capture() {
  const { t } = useT();
  const { hasModule } = useSession();
  const ocr = hasModule("ocr");
  const [typed, setTyped] = useState("");
  const [saving, setSaving] = useState(false);
  const [lane, setLane] = useState(() => readLocal("lane") ?? "1");
  const [practice, setPractice] = useState(false);
  const [move, setMove] = useState<MoveDetail | null>(null);
  const [shots, setShots] = useState<Shot[]>([]);
  const [error, setError] = useState("");
  const [restoring, setRestoring] = useState(() => readSession(CURRENT_KEY) !== null);

  const refresh = useCallback(async (id: string) => {
    try {
      const m = await ok(getMove(id));
      if (m.resolved_by !== "" && readSession(CURRENT_KEY) === m.id) removeSession(CURRENT_KEY);
      setMove(m);
    } catch (e) {
      setError(String(e));
    }
  }, []);

  // Pick the current truck up again after a reload. Local shot files are gone by
  // then, so the photos already on the hub are shown instead.
  useEffect(() => {
    const saved = readSession(CURRENT_KEY);
    if (!saved) return;
    let live = true;
    ok(getMove(saved))
      .then((m) => {
        if (!live) return;
        if (m.resolved_by === "") setMove(m);
        else removeSession(CURRENT_KEY);
      })
      .catch((e: unknown) => {
        if (!live) return;
        if (e instanceof ApiError && e.status === 404) removeSession(CURRENT_KEY);
        else setError(String(e));
      })
      .finally(() => {
        if (live) setRestoring(false);
      });
    return () => {
      live = false;
    };
  }, []);

  useMoveEvents(
    (id) => {
      if (move && id === move.id) void refresh(id);
    },
    () => {
      if (move) void refresh(move.id);
    },
  );

  // Safety net if live updates are blocked on this network.
  useEffect(() => {
    if (!move || move.status !== "reading") return;
    const timer = window.setInterval(() => void refresh(move.id), 3000);
    return () => window.clearInterval(timer);
  }, [move, refresh]);

  const unfinished = shots.some((s) => s.status !== "done");

  async function start() {
    if (unfinished && !window.confirm(t("leaveUnfinished"))) return;
    setError("");
    try {
      writeLocal("lane", lane);
      const created = await ok(createMove({ lane, is_test: practice }));
      setTyped("");
      writeSession(CURRENT_KEY, created.id);
      shots.forEach((s) => URL.revokeObjectURL(s.preview));
      setShots([]);
      await refresh(created.id);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  }

  function patch(key: string, change: Partial<Shot>) {
    setShots((list) => list.map((s) => (s.key === key ? { ...s, ...change } : s)));
  }

  async function upload(moveId: string, shot: Shot) {
    patch(shot.key, { status: "uploading", error: undefined });
    try {
      const prepared = await preparePhoto(shot.file);
      patch(shot.key, { quality: prepared.quality });
      const photo = await ok(
        addPhoto(
          moveId,
          { role: shot.role, width: prepared.width, height: prepared.height, original: shot.file, ai: prepared.ai },
          timeout(UPLOAD_TIMEOUT_MS),
        ),
      );
      patch(shot.key, { status: "done", photoId: photo.id });
    } catch (e) {
      patch(shot.key, { status: "failed", error: String(e) });
    }
  }

  function onFile(role: string, files: FileList | null) {
    const file = files?.[0];
    if (!move || !file) return;
    const shot: Shot = { key: localId(), role, file, preview: URL.createObjectURL(file), status: "uploading" };
    setShots((list) => [...list, shot]);
    void upload(move.id, shot);
  }

  // Without OCR the clerk types the number once the photos are in; the check digit is shown as they type.
  async function saveNumber() {
    if (!move) return;
    setSaving(true);
    setError("");
    try {
      await ok(enterNumber(move.id, { number: typed.trim() }));
      await refresh(move.id);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  const practiceCheck = (
    <label className="check">
      <input type="checkbox" checked={practice} onChange={(e) => setPractice(e.target.checked)} />
      {t("practice")}
    </label>
  );

  if (restoring) return <p>{t("loading")}</p>;

  if (!move) {
    return (
      <section className="card">
        <h2>{t("navCapture")}</h2>
        <label>
          {t("lane")}
          <input value={lane} onChange={(e) => setLane(e.target.value)} inputMode="numeric" />
        </label>
        {practiceCheck}
        <button type="button" className="primary big" onClick={() => void start()}>
          {t("startMove")}
        </button>
        {error && <p className="error">{error}</p>}
      </section>
    );
  }

  const open = move.resolved_by === "";
  const serverPhotos = (move.photos ?? []).filter((p) => !shots.some((s) => s.photoId === p.id));
  // A typed number the clerk saved and the hub accepted carries no reading to check, so it is not shown as failed.
  const numberClass = move.check_digit_valid || move.status === "accepted" ? "ok" : move.number ? "bad" : "";
  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("navCapture")}</h1>
      </div>
      <div className="card">
        <StatusChip status={move.status} /> {move.is_test && <span className="chip">{t("testTag")}</span>}
        <div className={`big-number mono ${numberClass}`}>{move.number || t("noNumber")}</div>
        <p>
          {t("typeCode")}: <b>{move.type_code || "–"}</b> · {t("color")}: <b>{move.color || "–"}</b>
        </p>
        {move.reason && (
          <p className="muted">
            {t("reason")}: {move.reason}
          </p>
        )}
        <CandidateList candidates={move.result.candidates ?? null} />
        <div className="row">
          <a className="button" href={`#/moves/${move.id}`}>
            {t("openMove")}
          </a>
          <button type="button" onClick={() => void start()}>
            {t("newTruck")}
          </button>
          {practiceCheck}
        </div>
      </div>
      {open && (
        <div className="shots">
          {SHOTS.map((s) => (
            <label key={s.role} className="shot-button">
              {t(s.label)}
              <input
                type="file"
                accept="image/*"
                capture="environment"
                hidden
                onChange={(e) => {
                  onFile(s.role, e.target.files);
                  e.target.value = "";
                }}
              />
            </label>
          ))}
        </div>
      )}
      {open && !ocr && (
        <form
          className="card"
          onSubmit={(e) => {
            e.preventDefault();
            void saveNumber();
          }}
        >
          <label>
            {t("typeNumber")}
            <input
              className="big-input"
              value={typed}
              onChange={(e) => setTyped(typedNumber(e.target.value))}
              maxLength={11}
              autoCapitalize="characters"
              autoComplete="off"
              spellCheck={false}
              required
            />
          </label>
          <CheckDigitMath number={typed} />
          <button type="submit" className="primary big" disabled={saving || unfinished || typed.trim() === ""}>
            {t("saveNumber")}
          </button>
          {unfinished && <p className="small muted">{t("waitForPhotos")}</p>}
        </form>
      )}
      {serverPhotos.length > 0 && (
        <div className="server-photos">
          <h4>{t("photosOnServer")}</h4>
          <div className="thumbs">
            {serverPhotos.map((p) => (
              <div key={p.id} className="thumb done">
                <img src={blobUrl(p.ai_sha256)} alt={p.role} loading="lazy" />
                <small>{p.role}</small>
              </div>
            ))}
          </div>
        </div>
      )}
      <div className="thumbs">
        {shots.map((s) => (
          <div key={s.key} className={`thumb ${s.status}`}>
            <img src={s.preview} alt={s.role} />
            <small>
              {s.role} · {s.status === "uploading" ? t("uploading") : s.status === "done" ? t("uploaded") : t("uploadFailed")}
            </small>
            {s.quality?.tooDark && (
              <small className="warn">
                {t("tooDark")}, {t("retakeHint")}
              </small>
            )}
            {s.quality?.tooBlurry && (
              <small className="warn">
                {t("tooBlurry")}, {t("retakeHint")}
              </small>
            )}
            {s.status === "failed" && (
              <button type="button" onClick={() => void upload(move.id, s)}>
                {t("retry")}
              </button>
            )}
          </div>
        ))}
      </div>
      {error && <p className="error">{error}</p>}
    </div>
  );
}
