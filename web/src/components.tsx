import { useEffect, useState } from "react";
import { api, type Box, type Candidate, type Status, type Validation } from "./api";
import { useT, type Key } from "./i18n";

export function StatusChip({ status }: { status: Status }) {
  const { t } = useT();
  return <span className={`chip chip-${status}`}>{t(`status_${status}` as Key)}</span>;
}

export function CandidateList({
  candidates,
  onPick,
}: {
  candidates: Candidate[] | null;
  onPick?: (number: string) => void;
}) {
  const { t } = useT();
  if (!candidates || candidates.length === 0) return null;
  return (
    <div>
      <h4>{t("candidates")}</h4>
      <ul className="candidates">
        {candidates.map((c) => (
          <li key={c.number}>
            <button
              type="button"
              className={c.check_digit_valid ? "cand ok" : "cand bad"}
              onClick={() => onPick?.(c.number)}
              disabled={!onPick}
            >
              <span className="mono">{c.number}</span> {Math.round(c.confidence * 100)}%
              {c.repaired_from && (
                <small>
                  {" "}
                  ({t("repairedFrom")} {c.repaired_from})
                </small>
              )}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

// PhotoWithBox draws the engine's number box over the engine copy of a photo.
// Boxes are relative to that copy, which has EXIF orientation applied.
export function PhotoWithBox({ src, box, alt }: { src: string; box: Box | null | undefined; alt: string }) {
  return (
    <div className="photo">
      <img src={src} alt={alt} loading="lazy" />
      {box && (
        <div
          className="box"
          style={{
            left: `${box.x0 * 100}%`,
            top: `${box.y0 * 100}%`,
            width: `${(box.x1 - box.x0) * 100}%`,
            height: `${(box.y1 - box.y0) * 100}%`,
          }}
        />
      )}
    </div>
  );
}

// CheckDigitMath validates a number on the hub and shows the arithmetic.
export function CheckDigitMath({ number }: { number: string }) {
  const { t } = useT();
  const [v, setV] = useState<Validation | null>(null);
  useEffect(() => {
    if (!number.trim()) {
      setV(null);
      return;
    }
    let live = true;
    const timer = window.setTimeout(() => {
      api
        .validate(number)
        .then((res) => {
          if (live) setV(res);
        })
        .catch(() => {
          if (live) setV(null);
        });
    }, 250);
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [number]);
  if (!v) return null;
  return (
    <div className={v.valid ? "math ok" : "math bad"}>
      <strong>{v.valid ? t("validNumber") : t("invalidNumber")}</strong>
      {v.steps && (
        <details>
          <summary>{t("checkDigitMath")}</summary>
          <table>
            <tbody>
              {v.steps.map((s, i) => (
                <tr key={i}>
                  <td className="mono">{s.char}</td>
                  <td>{s.value}</td>
                  <td>× {s.weight}</td>
                  <td>= {s.product}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p>
            {t("sum")}: {v.sum} · {t("remainder")}: {v.remainder} · {t("expected")}: {v.expected_check_digit}
          </p>
        </details>
      )}
    </div>
  );
}
