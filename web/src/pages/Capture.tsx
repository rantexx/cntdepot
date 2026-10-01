import { useCallback, useEffect, useState } from "react";
import { api, type MoveDetail } from "../api";
import { CandidateList, StatusChip } from "../components";
import { useT, type Key } from "../i18n";
import { preparePhoto, type Quality } from "../image";
import { useMoveEvents } from "../live";
import { localId, readLocal, writeLocal } from "../storage";

const SHOTS: { role: string; label: Key }[] = [
  { role: "rear", label: "shotRear" },
  { role: "left", label: "shotLeft" },
  { role: "right", label: "shotRight" },
  { role: "front", label: "shotFront" },
  { role: "other", label: "shotOther" },
];

interface Shot {
  key: string;
  role: string;
  file: File;
  preview: string;
  status: "uploading" | "done" | "failed";
  quality?: Quality;
  error?: string;
}

export function Capture() {
  const { t } = useT();
  const [lane, setLane] = useState(() => readLocal("lane") ?? "1");
  const [practice, setPractice] = useState(false);
  const [move, setMove] = useState<MoveDetail | null>(null);
  const [shots, setShots] = useState<Shot[]>([]);
  const [error, setError] = useState("");

  const refresh = useCallback(async (id: string) => {
    try {
      setMove(await api.getMove(id));
    } catch (e) {
      setError(String(e));
    }
  }, []);

  useMoveEvents((id) => {
    if (move && id === move.id) void refresh(id);
  });

  // Safety net if live updates are blocked on this network.
  useEffect(() => {
    if (!move || move.status !== "reading") return;
    const timer = window.setInterval(() => void refresh(move.id), 3000);
    return () => window.clearInterval(timer);
  }, [move, refresh]);

  async function start() {
    setError("");
    try {
      writeLocal("lane", lane);
      const created = await api.createMove(lane, practice);
      shots.forEach((s) => URL.revokeObjectURL(s.preview));
      setShots([]);
      await refresh(created.id);
    } catch (e) {
      setError(String(e));
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
      await api.uploadPhoto(moveId, shot.role, shot.file, prepared.ai, prepared.width, prepared.height);
      patch(shot.key, { status: "done" });
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

  if (!move) {
    return (
      <section className="card">
        <h2>{t("navCapture")}</h2>
        <label>
          {t("lane")}
          <input value={lane} onChange={(e) => setLane(e.target.value)} inputMode="numeric" />
        </label>
        <label className="check">
          <input type="checkbox" checked={practice} onChange={(e) => setPractice(e.target.checked)} />
          {t("practice")}
        </label>
        <button type="button" className="primary big" onClick={() => void start()}>
          {t("startMove")}
        </button>
        {error && <p className="error">{error}</p>}
      </section>
    );
  }

  const open = move.resolved_by === "";
  const numberClass = move.check_digit_valid ? "ok" : move.number ? "bad" : "";
  return (
    <section>
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
    </section>
  );
}
