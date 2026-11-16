import { useEffect, useState } from "react";
import { validateNumber, type Attempt, type Box, type Candidate, type Move, type Photo, type Status, type Validation } from "./gen/api";
import { blobUrl, ok } from "./hubApi";
import { useT, type Key } from "./i18n";

// Every state wears its ISO 3864 safety colour: green done, yellow needs a
// person, red refused, grey nothing to do; reading is an outlined sign.
export function StatusChip({ status }: { status: Status }) {
  const { t } = useT();
  return <span className={`sign sign-${status}`}>{t(`status_${status}` as Key)}</span>;
}

// ContainerNumber sets an ISO 6346 number the way it is painted on the
// doors: owner code, serial, and the check digit in its own box.
export function ContainerNumber({ number, className }: { number: string; className?: string }) {
  if (number.length !== 11) return <span className={`cn ${className ?? ""}`}>{number}</span>;
  return (
    <span className={`cn ${className ?? ""}`}>
      {number.slice(0, 4)} {number.slice(4, 10)}
      <span className="cd">{number[10]}</span>
    </span>
  );
}

// The hub gives every camera move this reason; in a list it is noise.
export const ROUTINE_REASON = "camera reads are confirmed by a person";

// shortcut: one gate-time target for every depot, until depots can set their own.
export const GATE_TARGET_S = 60;

// gateSeconds is how long a decided truck took from arrival to decision.
export function gateSeconds(m: { status: Status; created_at: string; updated_at: string }): number | null {
  if (m.status !== "accepted" && m.status !== "refused") return null;
  const s = (Date.parse(m.updated_at) - Date.parse(m.created_at)) / 1000;
  return Number.isFinite(s) && s >= 0 ? Math.round(s) : null;
}

export function fmtSeconds(s: number): string {
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  return s % 60 ? `${m} min ${s % 60} s` : `${m} min`;
}

// GateTime draws a truck's gate time on a bar whose tick is the target.
export function GateTime({ seconds, bare }: { seconds: number | null; bare?: boolean }) {
  const pct = seconds === null ? 0 : Math.min(100, (seconds / (GATE_TARGET_S * 2)) * 100);
  const over = seconds !== null && seconds > GATE_TARGET_S;
  return (
    <span className={`gt ${over ? "over" : ""}`}>
      <span className="gt-bar" aria-hidden="true">
        <span style={{ width: `${pct}%` }} />
      </span>
      {!bare && <b>{seconds === null ? "—" : fmtSeconds(seconds)}</b>}
    </span>
  );
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
      ok(validateNumber(number))
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

export const clock = (iso: string) =>
  new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });

// SignPlate shows a truck's number on a plate in its state colour; while a
// person must act the plate is safety blue and says so.
const PLATE: Record<Status, string> = {
  needs_review: "",
  capturing: "none",
  reading: "none",
  accepted: "safe",
  refused: "stop",
  no_container: "none",
};
export function SignPlate({ status, number }: { status: Status; number: string }) {
  const { t } = useT();
  return (
    <div className={`plate ${PLATE[status]}`}>
      <span className="plate-label">{status === "needs_review" ? t("confirmThisNumber") : t(`status_${status}` as Key)}</span>
      {number ? <ContainerNumber number={number} /> : <span className="cn">{t("noNumber")}</span>}
    </div>
  );
}

// VisitRow is one truck with fixed furniture: state, time, number, type,
// state sign and reason, gate time against the target.
export function VisitRow({ move: m }: { move: Move }) {
  const { t } = useT();
  return (
    <a className="visit" href={`#/moves/${m.id}`}>
      <i className={`tile ${m.status}`} />
      <span>{clock(m.created_at)}</span>
      {m.number ? <ContainerNumber number={m.number} /> : <span className="dim">{t("noNumber")}</span>}
      <span className={m.type_code ? "" : "dim"}>{m.type_code || "—"}</span>
      <span className="state">
        <StatusChip status={m.status} />
        {m.status !== "accepted" && m.reason && m.reason !== ROUTINE_REASON && <small>{m.reason}</small>}
      </span>
      <GateTime seconds={gateSeconds(m)} />
    </a>
  );
}

export function VisitHead() {
  const { t } = useT();
  return (
    <li className="visit head" aria-hidden="true">
      <span />
      <span>{t("colTime")}</span>
      <span>{t("colContainer")}</span>
      <span>{t("colType")}</span>
      <span>{t("colState")}</span>
      <span style={{ textAlign: "right" }}>{t("colGateTime")}</span>
    </li>
  );
}

const CROP_MARGIN = 0.35; // context kept around the number box, as a share of its size

// ReadCrop shows where on the photo the number was read: the engine's box
// with a margin, cut from its photo. Nothing renders without a box.
export function ReadCrop({ photos, attempts }: { photos: Photo[] | null; attempts: Attempt[] | null }) {
  const { t } = useT();
  const a = [...(attempts ?? [])].reverse().find((x) => !x.error && x.reading?.box);
  const photo = a && photos?.find((p) => p.id === a.photo_id);
  const box = a?.reading?.box;
  if (!photo || !box) return null;
  const mx = (box.x1 - box.x0) * CROP_MARGIN;
  const my = (box.y1 - box.y0) * CROP_MARGIN;
  const x0 = Math.max(0, box.x0 - mx);
  const x1 = Math.min(1, box.x1 + mx);
  const y0 = Math.max(0, box.y0 - my);
  const y1 = Math.min(1, box.y1 + my);
  const w = x1 - x0;
  const h = y1 - y0;
  const aspect = photo.width && photo.height ? (w * photo.width) / (h * photo.height) : w / h;
  const pos = (start: number, size: number) => (size >= 1 ? 0 : (start / (1 - size)) * 100);
  return (
    <figure className="read-crop">
      <div
        role="img"
        aria-label={t("readFrom")}
        style={{
          aspectRatio: String(aspect),
          backgroundImage: `url(${blobUrl(photo.ai_sha256, 1280)})`,
          backgroundSize: `${100 / w}% ${100 / h}%`,
          backgroundPosition: `${pos(x0, w)}% ${pos(y0, h)}%`,
        }}
      />
      <figcaption>{t("readFrom")}</figcaption>
    </figure>
  );
}
