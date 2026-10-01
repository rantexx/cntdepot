import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, blobUrl, typedNumber, type Attempt, type MoveDetail as Detail } from "../api";
import { CandidateList, CheckDigitMath, PhotoWithBox, StatusChip } from "../components";
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

  const load = useCallback(async () => {
    try {
      const m = await api.getMove(id);
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
  useEffect(() => {
    if (!move || move.status !== "reading") return;
    const timer = window.setInterval(() => void load(), 3000);
    return () => window.clearInterval(timer);
  }, [move, load]);

  async function resolve(action: "accept" | "correct" | "refuse") {
    if (!move) return;
    setBusy(true);
    setError("");
    try {
      writeLocal("by", by);
      await api.resolve(move.id, { action, number: action === "correct" ? number : undefined, by, note });
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
      await api.reread(move.id);
      await load();
    } catch (e) {
      setError(String(e));
    }
  }

  if (!move) return <p>{error || t("loading")}</p>;

  const number = typed && typed.id === id ? typed.text : move.number;
  const setNumber = (text: string) => setTyped({ id, text });
  const latest = latestByPhoto(move.attempts);
  const resolved = move.resolved_by !== "";
  const edited = number.trim().toUpperCase() !== move.number;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void resolve(edited ? "correct" : "accept");
  };

  return (
    <section>
      <a href="#/queue">← {t("back")}</a>
      <div className="card">
        <StatusChip status={move.status} /> {move.is_test && <span className="chip">{t("testTag")}</span>}
        <div className={`big-number mono ${move.check_digit_valid ? "ok" : "bad"}`}>{move.number || t("noNumber")}</div>
        <p>
          {t("typeCode")}: <b>{move.type_code || "–"}</b> · {t("color")}: <b>{move.color || "–"}</b> ·{" "}
          {t("confidence")}: {Math.round(move.confidence * 100)}%
        </p>
        {move.reason && (
          <p className="muted">
            {t("reason")}: {move.reason}
          </p>
        )}
        {resolved ? (
          <div>
            <p>{move.verified ? `✅ ${t("verified")}` : `⚠️ ${t("unverified")}`}</p>
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
        {(move.photos ?? []).map((p) => {
          const a = latest.get(p.id);
          const reading = a?.reading;
          return (
            <figure key={p.id} className="card">
              <PhotoWithBox src={blobUrl(p.ai_sha256)} box={reading?.box} alt={p.role} />
              <figcaption>
                <b>{p.role}</b>
                {a?.error && (
                  <p className="error">
                    {t("error")}: {a.error}
                  </p>
                )}
                {a && !a.error && (
                  <p className="small">
                    {(reading?.candidates ?? []).map((c) => `${c.number} ${Math.round(c.confidence * 100)}%`).join(", ") ||
                      t("noNumber")}
                    {reading?.type_code && ` · ${reading.type_code}`}
                    {reading?.color && ` · ${reading.color}`}
                    {` · ${a.engine}/${a.model} · ${a.latency_ms} ms`}
                  </p>
                )}
                <a href={blobUrl(p.sha256)} target="_blank" rel="noreferrer" className="small">
                  {t("original")}
                </a>
              </figcaption>
            </figure>
          );
        })}
      </div>
    </section>
  );
}
