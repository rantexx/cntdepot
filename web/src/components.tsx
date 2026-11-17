import { useEffect, useState } from "react";
import { getMove, validateNumber, type Attempt, type Box, type Candidate, type Move, type Photo, type Status, type Validation } from "./gen/api";
import { blobUrl, ok } from "./hubApi";
import { currentLang, useT, type Key } from "./i18n";

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

// ISO 6346 letter values skip the multiples of 11.
const LETTER_VALUES = [10, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 34, 35, 36, 37, 38];

// validateHere is the hub's number check done on the phone, for when the hub cannot be reached
// (same normalisation and arithmetic as internal/iso6346).
export function validateHere(input: string): Validation {
  const n = input.replace(/[İı]/g, "I").toUpperCase().replace(/[^A-Z0-9]/g, "");
  const v: Validation = { input, normalized: n, well_formed: false, check_digit_valid: false, expected_check_digit: null, valid: false, sum: 0, remainder: 0 };
  if (!/^[A-Z]{3}[UJZ][0-9]{7}$/.test(n)) return v;
  const steps = [...n.slice(0, 10)].map((char, i) => {
    const value = /[0-9]/.test(char) ? Number(char) : LETTER_VALUES[char.charCodeAt(0) - 65];
    return { char, value, weight: 2 ** i, product: value * 2 ** i };
  });
  const sum = steps.reduce((s, x) => s + x.product, 0);
  const expected = (sum % 11) % 10;
  const valid = Number(n[10]) === expected;
  return { ...v, well_formed: true, steps, sum, remainder: sum % 11, expected_check_digit: expected, check_digit_valid: valid, valid };
}

// CheckDigitMath validates a number on the hub and shows the arithmetic; without the hub, on the phone.
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
          if (live) setV(validateHere(number));
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

// ---- office screens ----

// Dates follow the language of the screen, not the computer.
export const dateTime = (iso: string | null) =>
  iso ? new Date(iso).toLocaleString(currentLang() === "tr" ? "tr-TR" : "en-GB", { dateStyle: "short", timeStyle: "short" }) : "—";

/** The text of a failed call, or the fallback when the error has none. */
export const errText = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);

// Ageing bands match the ageing report: 0-7 calm, 8-30 watch, 31-90 act, 90+ stop.
export function Age({ days }: { days: number }) {
  const { t } = useT();
  const band = days <= 7 ? 1 : days <= 30 ? 2 : days <= 90 ? 3 : 4;
  return <span className={`age age-${band}`}>{days} {t("daysShort")}</span>;
}

// A gate-move flag or refusal code in words: blocked, override:blocked, offline:hold_open, turned_away, ...
export function ReasonText({ code }: { code: string }) {
  const { t } = useT();
  const [head, rest] = code.split(":");
  const label = (c: string) => t(`reason_${c}` as Key) ?? c;
  return <>{(head === "override" || head === "offline") && rest ? `${t(`reason_${head}` as Key)}: ${label(rest)}` : label(head)}</>;
}

/** A flag's detail: refusal codes ("hold_open,no_order") in words; any other text stays as written. */
export function useReasonDetail() {
  const { t } = useT();
  return (detail: string) =>
    detail
      .split(",")
      .map((tok) => t(`reason_${tok.trim()}` as Key) ?? tok)
      .join(", ");
}

/** A link that downloads a CSV export of the office. */
export function CsvButton({ name, params }: { name: string; params?: Record<string, string> }) {
  const { t } = useT();
  const q = new URLSearchParams(Object.entries(params ?? {}).filter(([, v]) => v !== ""));
  return (
    <a className="button" href={`/api/export/${name}.csv${q.size ? `?${q}` : ""}`} download>
      {t("csv")}
    </a>
  );
}

/** The photos of a gate move as thumbnails that open full size. */
export function MovePhotos({ moveId }: { moveId: string }) {
  const [photos, setPhotos] = useState<Photo[]>([]);
  useEffect(() => {
    let live = true;
    ok(getMove(moveId)).then(
      (m) => live && setPhotos(m.photos ?? []),
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [moveId]);
  if (photos.length === 0) return null;
  return (
    <div className="thumbs">
      {photos.map((p) => (
        <a key={p.id} className="thumb" href={blobUrl(p.sha256)} target="_blank" rel="noreferrer">
          <img src={blobUrl(p.sha256, 320)} alt={p.role} loading="lazy" />
        </a>
      ))}
    </div>
  );
}
