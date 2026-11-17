// The gate on the phone (spec 3.2): pick in or out, the number (typed, or read by OCR), what the
// lists say about it, the form, damages, the trucker's signature, save. Every save goes into the
// outbox first, so the gate works without network; refusals come back to the clerk to decide.
import { useCallback, useEffect, useState } from "react";
import { createEirLink, getMove, type GateForm, type MoveDetail, type YardSettingsRefusalMode } from "../gen/api";
import { ok, typedNumber } from "../hubApi";
import { CandidateList, CheckDigitMath, clock } from "../components";
import { useT, type Key } from "../i18n";
import { preparePhoto } from "../image";
import { useMoveEvents } from "../live";
import { useSession } from "../session";
import { readLocal, writeLocal } from "../storage";
import { drop, enqueue, resultOf, retry, turnAway, ulid, useOutbox, wasTurnedAway, type Entry, type NewEntry } from "./outbox";
import { Signature } from "./Signature";
import { gateStatus, suggestedLine, useLists, type Lists } from "./snapshot";

type Mode = YardSettingsRefusalMode;

interface Shot {
  key: string;
  blob: Blob;
  width: number;
  height: number;
  url: string;
}

interface Damage {
  key: string;
  location: string;
  note: string;
  photos: Shot[];
}

interface Draft {
  moveId: string;
  direction: "in" | "out";
  number: string;
  typeCode: string;
  fullEmpty: "E" | "F";
  seal: string;
  condition: "" | "sound" | "damaged";
  lineId: string; // "" = the suggested line
  haulierId: string;
  truckPlate: string;
  trailerPlate: string;
  driverName: string;
  damages: Damage[];
  signature: Blob | null;
  override: string;
  moveQueued: boolean; // an OCR photo already put the move in the outbox
}

// The last values typed, per company and depot: a phone shared between depots keeps them apart.
const RECENT = ["truckPlate", "trailerPlate", "driverName"] as const;
const recent = (scope: string, field: string): string[] => {
  try {
    return JSON.parse(readLocal(`gate-recent:${scope}:${field}`) ?? "[]") as string[];
  } catch {
    return [];
  }
};
const remember = (scope: string, field: string, value: string) => {
  if (value) {
    writeLocal(`gate-recent:${scope}:${field}`, JSON.stringify([value, ...recent(scope, field).filter((v) => v !== value)].slice(0, 10)));
  }
};

const REASONS: Record<string, Key> = {
  blocked: "reason_blocked",
  hold_open: "reason_hold_open",
  order_not_open: "reason_order_not_open",
  order_expired: "reason_order_expired",
  no_order: "reason_no_order",
  walkin: "reason_walkin",
  missed_gate_out: "reason_missed_gate_out",
  no_open_visit: "reason_no_open_visit",
  item_used_up: "reason_item_used_up",
  other_depot: "reason_other_depot",
};

function useReason() {
  const { t } = useT();
  return (code: string) => {
    const [head, rest] = code.split(":");
    const c = rest ?? head;
    const text = REASONS[c] ? t(REASONS[c]) : c;
    if (rest === undefined) return text;
    return `${head === "offline" ? t("reason_offline") : t("gatePassed")}: ${text}`;
  };
}

function useOnline() {
  const [online, setOnline] = useState(navigator.onLine);
  useEffect(() => {
    const on = () => setOnline(true);
    const off = () => setOnline(false);
    window.addEventListener("online", on);
    window.addEventListener("offline", off);
    return () => {
      window.removeEventListener("online", on);
      window.removeEventListener("offline", off);
    };
  }, []);
  return online;
}

const numberOf = (outbox: Entry[], moveId: string) => {
  for (const e of outbox) {
    if (e.moveId !== moveId) continue;
    if (e.kind === "form") return e.payload.form.number;
    if (e.kind === "turnaway") return e.payload.number;
  }
  return "";
};

export function Gate() {
  const { t } = useT();
  const { current } = useSession();
  const scope = `${current?.companyId ?? ""}:${current?.depotId ?? ""}`;
  const { lists, refresh } = useLists(scope);
  const online = useOnline();
  const outbox = useOutbox();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saved, setSaved] = useState<{ moveId: string; number: string } | null>(null);
  const mode: Mode = lists?.snapshot.settings.refusalMode ?? "clerk_override";
  useEffect(() => {
    void navigator.storage?.persist?.().catch(() => undefined); // keep the outbox from being evicted
  }, []);

  // A manager's decision is announced as a move event: send the waiting form again with its approval.
  const askAgain = (id?: string) => {
    for (const e of outbox) {
      const r = e.refusal;
      if (e.state === "refused" && r?.approvalId && r.approvalStatus === "pending" && (!id || e.moveId === id)) {
        void retry(e, { approvalId: r.approvalId });
      }
    }
  };
  useMoveEvents(
    (id) => askAgain(id),
    () => askAgain(),
  );

  // Fresh lists once everything is sent: the yard changed.
  const pending = outbox.filter((e) => e.state === "pending").length;
  useEffect(() => {
    if (pending === 0) refresh();
  }, [pending, refresh]);

  if (!current?.depotId) {
    return (
      <section className="card">
        <h2>{t("navGate")}</h2>
        <p className="warn">{t("pickDepotFirst")}</p>
      </section>
    );
  }

  const start = (direction: "in" | "out") => {
    const haulierId = readLocal(`gate-haulier:${scope}`) ?? "";
    setDraft({
      moveId: ulid(),
      direction,
      number: "",
      typeCode: "",
      fullEmpty: "E",
      seal: "",
      condition: "",
      lineId: "",
      haulierId: lists?.hauliers.some((h) => h.id === haulierId) ? haulierId : "",
      truckPlate: "",
      trailerPlate: "",
      driverName: "",
      damages: [],
      signature: null,
      override: "",
      moveQueued: false,
    });
  };

  const attention = outbox.filter((e) => e.state !== "pending" && e.scope === scope);
  let body;
  if (saved) {
    body = <Result {...saved} mode={mode} online={online} onNext={() => setSaved(null)} />;
  } else if (draft) {
    body = (
      <TruckForm
        draft={draft}
        setDraft={setDraft}
        lists={lists}
        refresh={refresh}
        mode={mode}
        online={online}
        scope={scope}
        onSaved={(s) => {
          setDraft(null);
          setSaved(s);
        }}
      />
    );
  } else {
    body = (
      <>
        <div className="row">
          <button type="button" className="primary big" onClick={() => start("in")}>
            {t("gateIn")}
          </button>
          <button type="button" className="primary big" onClick={() => start("out")}>
            {t("gateOut")}
          </button>
        </div>
        {pending > 0 && (
          <p className="warn">
            {t("gateWaiting")}: {pending}
          </p>
        )}
        {attention.length > 0 && <h3>{t("gateAttention")}</h3>}
        {attention.map((e) => (
          <Decision key={e.seq} entry={e} number={numberOf(outbox, e.moveId)} mode={mode} online={online} />
        ))}
      </>
    );
  }

  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("navGate")}</h1>
        <div className="row gate-fresh">
          {!online && <span className="sign stop">{t("gateOffline")}</span>}
          <span className="small muted">
            {lists ? `${t("gateListsUpdated")} ${clock(lists.snapshot.at)}` : t("gateNoLists")}
          </span>
        </div>
      </div>
      <div className="gate-body">{body}</div>
    </div>
  );
}

function TruckForm({
  draft: d,
  setDraft,
  lists,
  refresh,
  mode,
  online,
  scope,
  onSaved,
}: {
  draft: Draft;
  setDraft: (f: (d: Draft | null) => Draft | null) => void;
  lists: Lists | undefined;
  refresh: () => void;
  mode: Mode;
  online: boolean;
  scope: string;
  onSaved: (s: { moveId: string; number: string }) => void;
}) {
  const { t } = useT();
  const reason = useReason();
  const { hasModule, session } = useSession();
  const savedBy = session?.user.id;
  const [error, setError] = useState("");
  const [holdAsk, setHoldAsk] = useState(false);
  const [held, setHeld] = useState(false);
  const [saving, setSaving] = useState(false);
  const [read, setRead] = useState<MoveDetail | null>(null);
  const set = (change: Partial<Draft>) => setDraft((x) => (x ? { ...x, ...change } : x));
  const setDamage = (key: string, change: Partial<Damage>) =>
    set({ damages: d.damages.map((x) => (x.key === key ? { ...x, ...change } : x)) });

  const number = typedNumber(d.number).replace(/[^A-Z0-9]/g, "");
  const suggested = d.direction === "in" && lists ? suggestedLine(lists.snapshot, number) : "";
  const status = lists && number.length >= 11 ? gateStatus(lists, { ...d, number, lineId: d.lineId || suggested }) : null;
  const visit = status?.visit;
  // Online, the status comes from the server: fresh lists as soon as the number is complete.
  const complete = number.length >= 11;
  useEffect(() => {
    if (complete) refresh();
  }, [complete, refresh]);
  const lineId = d.lineId || (d.direction === "out" ? (visit?.lineId ?? "") : suggested);
  const typeCode = d.typeCode || visit?.typeCode || read?.type_code || "";

  // OCR: the photo goes through the outbox like everything else; the read comes back on the move.
  const look = useCallback(async () => {
    try {
      const m = await ok(getMove(d.moveId));
      setRead(m);
      if (m.number) setDraft((x) => (x && !x.number ? { ...x, number: m.number } : x));
    } catch {
      // not sent yet
    }
  }, [d.moveId, setDraft]);
  useMoveEvents(
    (id) => {
      if (d.moveQueued && id === d.moveId) void look();
    },
    () => {
      if (d.moveQueued) void look();
    },
  );
  const reading = d.moveQueued && (!read || read.status === "capturing" || read.status === "reading");
  useEffect(() => {
    if (!reading) return;
    const timer = window.setInterval(() => void look(), 3000); // in case live updates are blocked
    return () => window.clearInterval(timer);
  }, [reading, look]);

  async function ocrPhoto(file: File | undefined) {
    if (!file) return;
    setError("");
    try {
      const p = await preparePhoto(file); // compressed on the phone: the engine copy is sent as the photo
      const photo: NewEntry = {
        id: ulid(),
        kind: "photo",
        moveId: d.moveId,
        scope,
        payload: { role: "rear", original: p.ai, ai: p.ai, width: p.aiWidth, height: p.aiHeight },
      };
      await enqueue(d.moveQueued ? [photo] : [moveEntry(d.moveId, scope), photo]);
      set({ moveQueued: true });
      setRead(null);
    } catch (e) {
      setError(String(e));
    }
  }

  async function damagePhoto(key: string, file: File | undefined) {
    if (!file) return;
    try {
      const p = await preparePhoto(file);
      const dm = d.damages.find((x) => x.key === key);
      if (!dm) return;
      const shot = { key: ulid(), blob: p.ai, width: p.aiWidth, height: p.aiHeight, url: URL.createObjectURL(p.ai) };
      setDamage(key, { photos: [...dm.photos, shot].slice(0, 3) });
    } catch (e) {
      setError(String(e));
    }
  }

  const damagesOk = d.damages.every((x) => x.location.trim() && x.photos.length > 0);
  const v = (s: string) => s.trim() || undefined;

  async function save() {
    setError("");
    setSaving(true);
    try {
      const base = { moveId: d.moveId, scope };
      const list: NewEntry[] = d.moveQueued ? [] : [moveEntry(d.moveId, scope)];
      const photo = (role: string, original: Blob, ai: Blob, width: number, height: number) => {
        const id = ulid();
        list.push({ ...base, id, kind: "photo", payload: { role, original, ai, width, height } });
        return id;
      };
      const damages = d.damages.map((x) => ({
        location: x.location.trim(),
        description: v(x.note),
        photoIds: x.photos.map((s) => photo("damage", s.blob, s.blob, s.width, s.height)),
      }));
      let signaturePhotoId: string | undefined;
      if (d.signature) {
        const p = await preparePhoto(d.signature); // the PNG is the photo; the JPEG copy is what the server keeps for engines
        signaturePhotoId = photo("signature", d.signature, p.ai, p.width, p.height);
      }
      const form: GateForm = {
        direction: d.direction,
        number,
        typeCode: v(typeCode.toUpperCase()),
        fullEmpty: d.fullEmpty,
        seal: v(d.seal),
        condition: d.condition || undefined,
        lineId: v(lineId),
        haulierId: v(d.haulierId),
        truckPlate: v(d.truckPlate.toUpperCase()),
        trailerPlate: v(d.trailerPlate.toUpperCase()),
        driverName: v(d.driverName),
        signaturePhotoId,
        damages,
        override: d.override.trim() ? { reason: d.override.trim() } : undefined,
        // Offline, the truck goes now: the server stores it whatever it finds and flags the conflicts.
        savedOffline: online ? undefined : true,
        savedBy,
      };
      list.push({ ...base, id: ulid(), kind: "form", payload: { form } });
      await enqueue(list);
      RECENT.forEach((f) => remember(scope, f, d[f].trim()));
      writeLocal(`gate-haulier:${scope}`, d.haulierId);
      d.damages.forEach((x) => x.photos.forEach((s) => URL.revokeObjectURL(s.url)));
      onSaved({ moveId: d.moveId, number });
    } catch (e) {
      setError(`${t("gateNotSaved")}: ${String(e)}`);
    } finally {
      setSaving(false);
    }
  }

  const ocr = hasModule("ocr");
  const refusals = status?.refusals ?? [];
  const blocked = refusals.includes("blocked");
  // Offline, nobody can answer a refusal the lists predict: the truck waits, unless the clerk lets it through with a reason.
  const mustHold = !online && refusals.length > 0 && !(mode === "clerk_override" && !blocked && d.override.trim());
  const details: Record<string, string | undefined> = { blocked: status?.block?.reason, hold_open: status?.holds[0]?.reason };

  async function refuseNow() {
    try {
      await turnAway(
        d.moveId,
        scope,
        { number, reasons: refusals.map((code) => ({ code, detail: details[code] })), savedBy },
        readLocal("lane") ?? "1",
      );
      onSaved({ moveId: d.moveId, number });
    } catch (e) {
      setError(`${t("gateNotSaved")}: ${String(e)}`);
    }
  }

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (mustHold) setHoldAsk(true);
        else void save();
      }}
    >
      <div className="card">
        <h2>{d.direction === "in" ? t("gateIn") : t("gateOut")}</h2>
        <label>
          {t("typeNumber")}
          <input
            className="big-input"
            value={d.number}
            onChange={(e) => set({ number: typedNumber(e.target.value) })}
            maxLength={11}
            autoCapitalize="characters"
            autoComplete="off"
            spellCheck={false}
            required
          />
        </label>
        <CheckDigitMath number={number} />
        {ocr &&
          (online ? (
            <label className="shot-button">
              {reading ? t("status_reading") : t("gateReadNumber")}
              <input
                type="file"
                accept="image/*"
                capture="environment"
                hidden
                onChange={(e) => {
                  void ocrPhoto(e.target.files?.[0]);
                  e.target.value = "";
                }}
              />
            </label>
          ) : (
            <p className="small muted">{t("gateTypeOffline")}</p>
          ))}
        {read && <CandidateList candidates={read.result.candidates ?? null} onPick={(n) => set({ number: n })} />}
      </div>

      {status && (
        <section className="card" aria-label={t("gateWhatWeKnow")}>
          {status.block && (
            <p className="error">
              {t("reason_blocked")}: {status.block.reason}
            </p>
          )}
          {status.holds.map((h) => (
            <p key={h.id} className="error">
              {t("reason_hold_open")}: {h.reason}
            </p>
          ))}
          {d.direction === "out" && !visit && <p className="warn">{t("gateNoVisit")}</p>}
          {d.direction === "out" && visit && (
            <p>
              {t("gateInYardSince")} {new Date(visit.inAt).toLocaleDateString()}
            </p>
          )}
          {d.direction === "in" && visit && <p className="warn">{t("gateAlreadyIn")}</p>}
          {status.orders.length > 0 ? (
            <ul className="steps">
              {status.orders.map((m) => (
                <li key={m.item.id} className={m.expired ? "bad" : "ok"}>
                  {m.order.reference} · {m.item.number || m.item.typeCode} · {m.item.quantity - m.item.used} {t("gateLeft")}
                  {m.expired && ` · ${t("reason_order_expired")}`}
                </li>
              ))}
            </ul>
          ) : (
            <p className="warn">{refusals.length === 0 && d.direction === "in" ? t("reason_walkin") : t("reason_no_order")}</p>
          )}
          {(refusals.includes("blocked") || (refusals.length > 0 && mode !== "clerk_override")) && (
            <p className="small">
              {refusals.includes("blocked")
                ? t("gateNeverPasses")
                : mode === "manager_approval"
                  ? t("gateNeedsManager")
                  : t("gateNoExceptions")}
            </p>
          )}
          {refusals.length > 0 && !refusals.includes("blocked") && mode === "clerk_override" && (
            <label>
              {t("gateOverrideReason")}
              <input value={d.override} onChange={(e) => set({ override: e.target.value })} maxLength={500} />
            </label>
          )}
        </section>
      )}

      <div className="card">
        <label>
          {t("typeCode")}
          <input
            value={d.typeCode}
            placeholder={visit?.typeCode || read?.type_code || ""}
            onChange={(e) => set({ typeCode: e.target.value.toUpperCase() })}
            maxLength={4}
            autoCapitalize="characters"
          />
        </label>
        <label>
          {t("gateFullEmpty")}
          <select value={d.fullEmpty} onChange={(e) => set({ fullEmpty: e.target.value as "E" | "F" })}>
            <option value="E">{t("gateEmpty")}</option>
            <option value="F">{t("gateFull")}</option>
          </select>
        </label>
        <label>
          {t("gateSeal")}
          <input value={d.seal} onChange={(e) => set({ seal: e.target.value })} maxLength={64} />
        </label>
        <label>
          {t("gateCondition")}
          <select value={d.condition} onChange={(e) => set({ condition: e.target.value as Draft["condition"] })}>
            <option value="">–</option>
            <option value="sound">{t("gateSound")}</option>
            <option value="damaged">{t("gateDamaged")}</option>
          </select>
        </label>
        <label>
          {t("gateLine")}
          <select value={lineId} onChange={(e) => set({ lineId: e.target.value })}>
            <option value="">–</option>
            {lists?.lines.map((l) => (
              <option key={l.id} value={l.id}>
                {l.code} · {l.name}
              </option>
            ))}
          </select>
        </label>
      </div>

      <div className="card">
        <h4>{t("gateDamages")}</h4>
        {d.damages.map((x, i) => (
          <fieldset key={x.key} className="gate-damage">
            <legend>
              {t("gateDamage")} {i + 1}
            </legend>
            <label>
              {t("gateDamageLocation")}
              <input value={x.location} onChange={(e) => setDamage(x.key, { location: e.target.value })} maxLength={500} required />
            </label>
            <label>
              {t("gateDamageNote")}
              <input value={x.note} onChange={(e) => setDamage(x.key, { note: e.target.value })} maxLength={500} />
            </label>
            <div className="thumbs">
              {x.photos.map((s) => (
                <div key={s.key} className="thumb done">
                  <img src={s.url} alt={`${t("gateDamage")} ${i + 1}`} />
                </div>
              ))}
            </div>
            <div className="row">
              {x.photos.length < 3 && (
                <label className="shot-button">
                  {t("gateDamagePhoto")}
                  <input
                    type="file"
                    accept="image/*"
                    capture="environment"
                    hidden
                    onChange={(e) => {
                      void damagePhoto(x.key, e.target.files?.[0]);
                      e.target.value = "";
                    }}
                  />
                </label>
              )}
              <button
                type="button"
                className="danger"
                onClick={() => {
                  x.photos.forEach((s) => URL.revokeObjectURL(s.url));
                  set({ damages: d.damages.filter((y) => y.key !== x.key) });
                }}
              >
                {t("gateRemoveDamage")}
              </button>
            </div>
          </fieldset>
        ))}
        <button
          type="button"
          onClick={() => set({ damages: [...d.damages, { key: ulid(), location: "", note: "", photos: [] }], condition: "damaged" })}
        >
          {t("gateAddDamage")}
        </button>
      </div>

      <div className="card">
        <label>
          {t("gateHaulier")}
          <select value={d.haulierId} onChange={(e) => set({ haulierId: e.target.value })}>
            <option value="">–</option>
            {lists?.hauliers.map((h) => (
              <option key={h.id} value={h.id}>
                {h.code} · {h.name}
              </option>
            ))}
          </select>
        </label>
        {RECENT.map((f) => (
          <label key={f}>
            {t(f === "truckPlate" ? "gateTruck" : f === "trailerPlate" ? "gateTrailer" : "gateDriver")}
            <input
              value={d[f]}
              onChange={(e) => set({ [f]: e.target.value } as Partial<Draft>)}
              list={`gate-${f}`}
              maxLength={f === "driverName" ? 200 : 64}
              autoCapitalize={f === "driverName" ? "words" : "characters"}
            />
            <datalist id={`gate-${f}`}>
              {recent(scope, f).map((r) => (
                <option key={r} value={r} />
              ))}
            </datalist>
          </label>
        ))}
      </div>

      <div className="card">
        <h4>{t("gateSignature")}</h4>
        <Signature onChange={(png) => set({ signature: png })} />
      </div>

      {holdAsk && mustHold && (
        <section className="card" aria-label={t("gateHoldTitle")}>
          <span className="sign stop">{t("gateHoldTitle")}</span>
          <ul className="steps">
            {refusals.map((c) => (
              <li key={c} className="bad">
                {reason(c)}
                {details[c] ? `: ${details[c]}` : ""}
              </li>
            ))}
          </ul>
          {mode === "clerk_override" && !blocked && <p className="small">{t("gateHoldOrReason")}</p>}
          <div className="row">
            <button
              type="button"
              onClick={() => {
                setHoldAsk(false);
                setHeld(true);
              }}
            >
              {t("gateHold")}
            </button>
            <button type="button" className="danger" onClick={() => void refuseNow()}>
              {t("gateRefuseTruck")}
            </button>
          </div>
        </section>
      )}
      {held && mustHold && <p className="warn">{t("gateHeldNote")}</p>}
      {!damagesOk && <p className="warn">{t("gateDamageNeeds")}</p>}
      {error && <p className="error">{error}</p>}
      <button type="submit" className="primary big" disabled={saving || !number || !damagesOk}>
        {t("save")}
      </button>
      <button type="button" className="big" onClick={() => setDraft(() => null)}>
        {t("cancel")}
      </button>
    </form>
  );
}

const moveEntry = (moveId: string, scope: string): NewEntry => ({
  id: ulid(),
  kind: "move",
  moveId,
  scope,
  payload: { lane: readLocal("lane") ?? "1" },
});

// Decision shows a refused or unsent truck with what the company's refusal mode allows.
function Decision({
  entry,
  number,
  mode,
  online,
  onHold,
}: {
  entry: Entry;
  number: string;
  mode: Mode;
  online: boolean;
  onHold?: () => void;
}) {
  const { t } = useT();
  const reasonText = useReason();
  const { session } = useSession();
  const [reason, setReason] = useState("");
  const refusals = entry.refusal?.refusals ?? [];
  // A truck saved offline has already gone: turning it away would delete the record of it.
  const gone = entry.kind === "form" && !!entry.payload.form.savedOffline;
  const refuse = !gone && (
    <button
      type="button"
      className="danger"
      onClick={() =>
        void turnAway(entry.moveId, entry.scope, { number, reasons: refusals, savedBy: session?.user.id }, readLocal("lane") ?? "1")
      }
    >
      {t("gateRefuseTruck")}
    </button>
  );
  if (entry.state === "failed") {
    return (
      <section className="card" aria-label={t("gateFailed")}>
        <h3 className="cn">{number}</h3>
        <p className="error">
          {entry.kind === "turnaway" ? t("gateTurnAwayFailed") : t("gateFailed")}: {entry.error}
        </p>
        <div className="row">
          <button type="button" onClick={() => void retry(entry)}>
            {t("retry")}
          </button>
          {entry.kind === "turnaway" ? (
            <button type="button" className="danger" onClick={() => void drop(entry)}>
              {t("gateRemove")}
            </button>
          ) : (
            refuse
          )}
        </div>
      </section>
    );
  }
  const r = entry.refusal;
  const blocked = r?.error.code === "E4099";
  const candidates = r?.candidates ?? [];
  const approval = r?.approvalStatus;
  if (r?.approvalId) mode = "manager_approval"; // the server's answer beats the cached setting
  return (
    <section className="card" aria-label={t("gateRefused")}>
      <h3 className="cn">{number}</h3>
      <span className="sign stop">{t("gateRefused")}</span>
      <ul className="steps">
        {refusals.map((x) => (
          <li key={x.code} className="bad">
            {reasonText(x.code)}
            {x.detail ? `: ${x.detail}` : ""}
          </li>
        ))}
      </ul>
      {candidates.length > 0 && (
        <>
          <h4>{t("gatePickOrder")}</h4>
          <div className="row">
            {candidates.map((c) => (
              <button key={c.itemId} type="button" onClick={() => void retry(entry, { chosenOrderItemId: c.itemId })}>
                {c.number || c.typeCode} · {c.fullEmpty} · {c.remaining} {t("gateLeft")}
              </button>
            ))}
          </div>
        </>
      )}
      {blocked && <p className="error">{t("gateNeverPasses")}</p>}
      {!blocked && candidates.length === 0 && mode === "clerk_override" && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void retry(entry, { override: { reason: reason.trim() } });
          }}
        >
          <label>
            {t("gateOverrideReason")}
            <input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} required />
          </label>
          <button type="submit" className="primary big" disabled={!reason.trim()}>
            {t("gateLetThrough")}
          </button>
        </form>
      )}
      {!blocked && mode === "manager_approval" && (
        <p className={approval === "denied" ? "error" : "warn"}>
          {approval === "denied" ? t("gateManagerDenied") : online ? t("gateWaitingManager") : t("gateNeedsNetwork")}
        </p>
      )}
      {!blocked && mode === "none" && <p className="warn">{t("gateNoExceptions")}</p>}
      <div className="row">
        {gone && (
          <button type="button" onClick={() => void retry(entry)}>
            {t("retry")}
          </button>
        )}
        {onHold && mode === "manager_approval" && !blocked && (
          <button type="button" onClick={onHold}>
            {t("gateHold")}
          </button>
        )}
        {refuse}
      </div>
    </section>
  );
}

// Result follows the saved truck through the outbox: on the phone, sending, refused, or saved with its EIR.
function Result({
  moveId,
  number,
  mode,
  online,
  onNext,
}: {
  moveId: string;
  number: string;
  mode: Mode;
  online: boolean;
  onNext: () => void;
}) {
  const { t } = useT();
  const reasonText = useReason();
  const outbox = useOutbox();
  const [link, setLink] = useState("");
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const mine = outbox.filter((e) => e.moveId === moveId);
  const stuck = mine.find((e) => e.state !== "pending");
  const turning = mine.some((e) => e.kind === "turnaway");
  const result = resultOf(moveId);

  async function eir(): Promise<string> {
    if (link) return link;
    const l = window.location.origin + (await ok(createEirLink(moveId))).url;
    setLink(l);
    return l;
  }
  async function share() {
    setError("");
    setNote("");
    try {
      const url = await eir();
      if (navigator.share) await navigator.share({ title: `EIR ${number}`, url });
      else if (navigator.clipboard) {
        await navigator.clipboard.writeText(url);
        setNote(t("gateLinkCopied"));
      }
    } catch (e) {
      if (!(e instanceof DOMException && e.name === "AbortError")) setError(String(e)); // AbortError: the clerk closed the sheet
    }
  }
  async function print() {
    setError("");
    try {
      const w = window.open(await eir(), "_blank");
      w?.addEventListener("load", () => w.print());
    } catch (e) {
      setError(String(e));
    }
  }

  let body;
  if (stuck) {
    body = <Decision entry={stuck} number={number} mode={mode} online={online} onHold={onNext} />;
  } else if (turning || wasTurnedAway(moveId)) {
    body = (
      <div className="card">
        <div className="plate stop">
          <span className="plate-label">{t("gateTurnedAway")}</span>
          <span className="cn">{number}</span>
        </div>
        <p>{turning ? (online ? t("gateSending") : t("gateSavedOfflineHint")) : t("gateTurnedAwaySent")}</p>
      </div>
    );
  } else if (mine.length > 0) {
    // Online with no failed send yet: still sending. Otherwise the truck is kept on the phone.
    const sending = online && !mine.some((e) => e.tries > 0);
    body = (
      <div className="card">
        <div className="plate none">
          <span className="plate-label">{sending ? t("gateSending") : t("gateSavedOffline")}</span>
          <span className="cn">{number}</span>
        </div>
        {!sending && <p>{t("gateSavedOfflineHint")}</p>}
      </div>
    );
  } else if (result) {
    body = (
      <div className="card">
        <div className="plate safe">
          <span className="plate-label">{t("gateSaved")}</span>
          <span className="cn">{result.move.number}</span>
        </div>
        {result.flags.length > 0 && (
          <ul className="steps">
            {result.flags.map((f) => (
              <li key={f.code} className="bad">
                {reasonText(f.code)}
                {f.detail ? `: ${f.detail}` : ""}
              </li>
            ))}
          </ul>
        )}
        <div className="row">
          <button type="button" className="primary" onClick={() => void share()}>
            {t("gateShareEir")}
          </button>
          <button type="button" onClick={() => void print()}>
            {t("gatePrint")}
          </button>
        </div>
        {link && <input readOnly value={link} aria-label="EIR" onFocus={(e) => e.target.select()} />}
        {note && <p className="small">{note}</p>}
        {error && <p className="error">{error}</p>}
      </div>
    );
  } else {
    // Sent from another tab, or this tab was reloaded: the move's own record says how it ended.
    body = (
      <p className="card">
        {t("gateNothingToSend")} <a href={`#/moves/${moveId}`}>{t("openMove")}</a>
      </p>
    );
  }
  return (
    <>
      {body}
      <button type="button" className="big" onClick={onNext}>
        {t("newTruck")}
      </button>
    </>
  );
}
