import { useCallback, useEffect, useState } from "react";
import { api, type Move } from "../api";
import { VisitHead, VisitRow } from "../components";
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
    <div className="page">
      <div className="page-head">
        <h1>{t("navQueue")}</h1>
      </div>
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
      {moves.length > 0 && (
        <ul className="visits">
          <VisitHead />
          {moves.map((m) => (
            <li key={m.id}>
              <VisitRow move={m} />
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
