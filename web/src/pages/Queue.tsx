import { useCallback, useEffect, useState } from "react";
import { api, blobUrl, type Move } from "../api";
import { StatusChip } from "../components";
import { useT, type Key } from "../i18n";
import { useMoveEvents } from "../live";

const FILTERS: { status: string; label: Key }[] = [
  { status: "needs_review", label: "filterNeedsReview" },
  { status: "reading", label: "filterReading" },
  { status: "", label: "filterAll" },
  { status: "accepted", label: "filterAccepted" },
  { status: "refused", label: "filterRefused" },
];

export function Queue() {
  const { t } = useT();
  const [status, setStatus] = useState("needs_review");
  const [moves, setMoves] = useState<Move[]>([]);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      setMoves((await api.listMoves(status)).moves);
      setError("");
    } catch (e) {
      setError(String(e));
    }
  }, [status]);

  useEffect(() => {
    void load();
  }, [load]);
  useMoveEvents(
    () => void load(),
    () => void load(),
  );

  return (
    <section>
      <div className="tabs">
        {FILTERS.map((f) => (
          <button
            key={f.label}
            type="button"
            className={f.status === status ? "active" : ""}
            onClick={() => setStatus(f.status)}
          >
            {t(f.label)}
          </button>
        ))}
      </div>
      {error && <p className="error">{error}</p>}
      {moves.length === 0 && <p className="muted">{t("noMoves")}</p>}
      <ul className="queue">
        {moves.map((m) => {
          const first = m.photos?.[0];
          return (
            <li key={m.id}>
              <a href={`#/moves/${m.id}`} className="queue-row">
                {first ? <img src={blobUrl(first.ai_sha256)} alt="" loading="lazy" /> : <div className="noimg" />}
                <div>
                  <div className="mono strong">{m.number || t("noNumber")}</div>
                  <StatusChip status={m.status} /> {m.is_test && <span className="chip">{t("testTag")}</span>}
                  <div className="muted small">
                    {new Date(m.created_at).toLocaleString()} · {m.reason}
                  </div>
                </div>
              </a>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
