import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Age, ContainerNumber, dateTime, errText, MovePhotos, ReasonText, useReasonDetail, validateHere } from "../components";
import {
  correctMove,
  createEirLink,
  getVisit,
  listVisits,
  placeHold,
  releaseHold,
  setVisitGrade,
  setVisitPosition,
  voidMove,
  type GateForm,
  type MoveForm,
  type VisitDetail as Detail,
} from "../gen/api";
import { ok, typedNumber } from "../hubApi";
import { useT } from "../i18n";

const GRADES = ["", "A", "B", "C", "FOOD"];

// Runs a call, reloads the page's data after it, and keeps its failure in the message line.
function useAct(reload: () => void) {
  const { t } = useT();
  const [msg, setMsg] = useState("");
  const act = async (fn: () => Promise<unknown>, after?: () => void) => {
    setMsg("");
    try {
      await fn();
      after?.();
      reload();
    } catch (e) {
      setMsg(errText(e, t("networkError")));
    }
  };
  return { msg, act };
}

// One gate form of the visit: facts, damages, photos, EIR link, correction and void.
function MoveBlock({ form, label, reload }: { form: MoveForm; label: string; reload: () => void }) {
  const { t } = useT();
  const { msg, act } = useAct(reload);
  const [mode, setMode] = useState<"" | "correct" | "void">("");
  const [reason, setReason] = useState("");
  const [draft, setDraft] = useState({
    number: form.number,
    typeCode: form.typeCode,
    fullEmpty: form.fullEmpty || "E",
    seal: form.seal,
    condition: form.condition === "sound" || form.condition === "damaged" ? form.condition : "",
    truckPlate: form.truckPlate,
    trailerPlate: form.trailerPlate,
    driverName: form.driverName,
  });
  const [eir, setEir] = useState("");
  const check = validateHere(draft.number);
  const detail = useReasonDetail();

  async function openEir() {
    try {
      const l = window.location.origin + (await ok(createEirLink(form.id))).url;
      setEir(l);
      window.open(l, "_blank");
    } catch (e) {
      setEir(errText(e, t("networkError")));
    }
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (mode === "void") {
      void act(() => voidMove(form.id, { reason }), () => setMode(""));
      return;
    }
    // The correction rewrites the whole form: keep what the office does not edit here.
    const body: GateForm = {
      direction: form.direction as GateForm["direction"],
      ...draft,
      number: typedNumber(draft.number).trim(),
      lineId: form.lineId ?? undefined,
      customerId: form.customerId ?? undefined,
      haulierId: form.haulierId ?? undefined,
      signaturePhotoId: form.signaturePhotoId ?? undefined,
      chosenOrderItemId: form.orderItemId ?? undefined,
      damages: form.damages.map((d) => ({ location: d.location, description: d.description, photoIds: d.photoIds })),
    };
    void act(() => correctMove(form.id, { reason, form: body }), () => setMode(""));
  }
  const set = (k: keyof typeof draft) => (e: { target: { value: string } }) => setDraft({ ...draft, [k]: e.target.value });

  return (
    <section className="card">
      <div className="page-head">
        <h3>
          {label} <small className="muted">{dateTime(form.formSavedAt)}</small>
          {form.voided && (
            <span className="chip">
              {t("voided")}: {form.voidReason}
            </span>
          )}
        </h3>
        <div className="row">
          {eir && <input readOnly value={eir} aria-label="EIR" onFocus={(e) => e.target.select()} />}
          <button type="button" onClick={() => void openEir()}>
            {t("openEir")}
          </button>
          {!form.voided && (
            <>
              <button type="button" onClick={() => setMode(mode === "correct" ? "" : "correct")}>
                {t("correctForm")}
              </button>
              <button type="button" className="danger" onClick={() => setMode(mode === "void" ? "" : "void")}>
                {t("voidMove")}
              </button>
            </>
          )}
        </div>
      </div>
      <dl className="facts-grid">
        <div>
          <dt>{t("gateTruck")}</dt>
          <dd>{form.truckPlate || "—"}</dd>
        </div>
        <div>
          <dt>{t("gateTrailer")}</dt>
          <dd>{form.trailerPlate || "—"}</dd>
        </div>
        <div>
          <dt>{t("gateDriver")}</dt>
          <dd>{form.driverName || "—"}</dd>
        </div>
        <div>
          <dt>{t("gateSeal")}</dt>
          <dd>{form.seal || "—"}</dd>
        </div>
        <div>
          <dt>{t("gateCondition")}</dt>
          <dd>{form.condition === "damaged" ? t("gateDamaged") : form.condition === "sound" ? t("gateSound") : form.condition || "—"}</dd>
        </div>
        <div>
          <dt>{t("corrections")}</dt>
          <dd>{form.corrections}</dd>
        </div>
      </dl>
      {form.flags.length > 0 && (
        <p>
          {form.flags.map((f) => (
            <span key={f.id} className="chip" title={detail(f.detail)}>
              <ReasonText code={f.code} />
            </span>
          ))}
        </p>
      )}
      {form.damages.length > 0 && (
        <>
          <h4>{t("gateDamages")}</h4>
          <ul>
            {form.damages.map((d) => (
              <li key={d.id}>
                <b>{d.location}</b> {d.description}
              </li>
            ))}
          </ul>
        </>
      )}
      <MovePhotos moveId={form.id} />
      {mode && (
        <form className="admin-form" onSubmit={submit}>
          {mode === "correct" && (
            <>
              <label>
                {t("number")}
                <input value={draft.number} onChange={set("number")} maxLength={64} autoCapitalize="characters" />
                <span className={check.valid ? "ok-text small" : "error small"}>{check.valid ? t("validNumber") : t("invalidNumber")}</span>
              </label>
              <label>
                {t("typeCode")}
                <input value={draft.typeCode} onChange={set("typeCode")} maxLength={8} />
              </label>
              <label>
                {t("gateFullEmpty")}
                <select value={draft.fullEmpty} onChange={set("fullEmpty")}>
                  <option value="E">{t("gateEmpty")}</option>
                  <option value="F">{t("gateFull")}</option>
                </select>
              </label>
              <label>
                {t("gateSeal")}
                <input value={draft.seal} onChange={set("seal")} />
              </label>
              <label>
                {t("gateCondition")}
                <select value={draft.condition} onChange={set("condition")}>
                  <option value="">–</option>
                  <option value="sound">{t("gateSound")}</option>
                  <option value="damaged">{t("gateDamaged")}</option>
                </select>
              </label>
              <label>
                {t("gateTruck")}
                <input value={draft.truckPlate} onChange={set("truckPlate")} />
              </label>
              <label>
                {t("gateTrailer")}
                <input value={draft.trailerPlate} onChange={set("trailerPlate")} />
              </label>
              <label>
                {t("gateDriver")}
                <input value={draft.driverName} onChange={set("driverName")} />
              </label>
            </>
          )}
          <label className="grow">
            {t("reason")}
            <input value={reason} onChange={(e) => setReason(e.target.value)} required maxLength={500} />
          </label>
          <button type="submit" className={mode === "void" ? "danger" : "primary"}>
            {mode === "void" ? t("voidMove") : t("save")}
          </button>
        </form>
      )}
      {msg && (
        <p className="error" role="alert">
          {msg}
        </p>
      )}
    </section>
  );
}

export function VisitDetail({ id }: { id: string }) {
  const { t } = useT();
  const [v, setV] = useState<Detail | null>(null);
  const [failed, setFailed] = useState("");
  const load = useCallback(() => {
    ok(getVisit(id)).then(setV, (e) => setFailed(errText(e, t("networkError"))));
  }, [id]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(load, [load]);
  const { msg, act } = useAct(load);
  // The yard list carries the depot-local day count (the ageing report's rule); the visit itself does not.
  const [days, setDays] = useState<number | null>(null);
  useEffect(() => {
    if (!v) return;
    ok(listVisits({ q: v.number, status: v.status })).then(
      (r) => setDays(r.items.find((x) => x.id === v.id)?.daysInYard ?? null),
      () => setDays(null),
    );
  }, [v?.id, v?.status]); // eslint-disable-line react-hooks/exhaustive-deps
  const [pos, setPos] = useState({ block: "", row: "", tier: "" });
  const [reason, setReason] = useState("");
  useEffect(() => {
    if (v) setPos({ block: v.block, row: v.rowNo, tier: v.tier });
  }, [v?.block, v?.rowNo, v?.tier]); // eslint-disable-line react-hooks/exhaustive-deps

  if (failed) return <p className="error">{failed}</p>;
  if (!v) return <p className="boot">{t("loading")}</p>;
  const open = v.holds.filter((h) => !h.releasedAt);

  return (
    <div className="page">
      <a className="back" href="#/yard">
        {t("back")}
      </a>
      <div className="page-head">
        <h1>
          <ContainerNumber number={v.number} />
          <small>{v.typeCode}</small>
        </h1>
        {open.length > 0 && <span className="hold-sign">{t("onHold")}</span>}
        {v.status !== "in_yard" ? <span className="chip">{t("leftYard")}</span> : days !== null && <Age days={days} />}
      </div>
      <section className="card">
        <dl className="facts-grid">
          <div>
            <dt>{t("gateIn")}</dt>
            <dd>{dateTime(v.inAt)}</dd>
          </div>
          <div>
            <dt>{t("gateOut")}</dt>
            <dd>{dateTime(v.outAt)}</dd>
          </div>
          <div>
            <dt>{t("gateFullEmpty")}</dt>
            <dd>{v.fullEmpty === "F" ? t("gateFull") : t("gateEmpty")}</dd>
          </div>
          {v.imported && (
            <div>
              <dt>{t("source")}</dt>
              <dd>{t("importedStock")}</dd>
            </div>
          )}
          {v.closeReason && (
            <div>
              <dt>{t("closeReason")}</dt>
              <dd>{v.closeReason}</dd>
            </div>
          )}
        </dl>
        {v.status === "in_yard" && (
          <form
            className="admin-form"
            onSubmit={(e) => {
              e.preventDefault();
              void act(() => setVisitPosition(id, pos));
            }}
          >
            <label>
              {t("block")}
              <input value={pos.block} maxLength={64} onChange={(e) => setPos({ ...pos, block: e.target.value })} />
            </label>
            <label>
              {t("row")}
              <input value={pos.row} maxLength={64} onChange={(e) => setPos({ ...pos, row: e.target.value })} />
            </label>
            <label>
              {t("tier")}
              <input value={pos.tier} maxLength={64} onChange={(e) => setPos({ ...pos, tier: e.target.value })} />
            </label>
            <button type="submit">{t("savePosition")}</button>
            <label>
              {t("grade")}
              <select value={v.grade} onChange={(e) => void act(() => setVisitGrade(id, { grade: e.target.value }))}>
                {GRADES.map((g) => (
                  <option key={g} value={g}>
                    {g || "—"}
                  </option>
                ))}
              </select>
            </label>
          </form>
        )}
      </section>

      <section className="card">
        <h3>{t("holds")}</h3>
        {v.holds.length === 0 && <p className="muted">{t("noRows")}</p>}
        <ul>
          {v.holds.map((h) => (
            <li key={h.id} className={h.releasedAt ? "muted" : undefined}>
              <b>{h.reason}</b> · {dateTime(h.placedAt)}
              {h.releasedAt ? (
                ` · ${t("released")} ${dateTime(h.releasedAt)}`
              ) : (
                <>
                  {" "}
                  <button type="button" onClick={() => void act(() => releaseHold(h.id))}>
                    {t("releaseHold")}
                  </button>
                </>
              )}
            </li>
          ))}
        </ul>
        {v.status === "in_yard" && (
        <form
          className="admin-form"
          onSubmit={(e) => {
            e.preventDefault();
            void act(() => placeHold(id, { reason: reason.trim() }), () => setReason(""));
          }}
        >
          <label className="grow">
            {t("reason")}
            <input value={reason} maxLength={500} required onChange={(e) => setReason(e.target.value)} />
          </label>
          <button type="submit" className="danger">
            {t("placeHold")}
          </button>
        </form>
        )}
        {msg && (
          <p className="error" role="alert">
            {msg}
          </p>
        )}
      </section>

      {v.inForm && <MoveBlock key={`${v.inForm.id}:${v.inForm.corrections}:${v.inForm.voided}`} form={v.inForm} label={t("gateIn")} reload={load} />}
      {v.outForm && <MoveBlock key={`${v.outForm.id}:${v.outForm.corrections}:${v.outForm.voided}`} form={v.outForm} label={t("gateOut")} reload={load} />}
    </div>
  );
}
