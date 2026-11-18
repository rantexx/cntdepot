import { useState, type FormEvent } from "react";
import { errText } from "../components";
import {
  LineEDISettingsMode,
  setDepotEdiLocation,
  setEdiCompany,
  setLineEdi,
  testLineEdi,
  type Depot,
  type EDIStatusInfo,
  type LineEDI,
  type LineEDIProfile,
} from "../gen/api";
import { ok } from "../hubApi";
import { useT, type Key } from "../i18n";

// One depot's EDI location code, prefilled from the depot; it is sent only when changed, so a
// save never wipes a code this form did not show.
function DepotLocation({ depot, done }: { depot: Depot; done: () => void }) {
  const { t } = useT();
  const [was, setWas] = useState(depot.ediLocation);
  const [loc, setLoc] = useState(depot.ediLocation);
  const [msg, setMsg] = useState("");
  const [saved, setSaved] = useState(false);
  async function save(e: FormEvent) {
    e.preventDefault();
    setMsg("");
    setSaved(false);
    const next = loc.trim();
    try {
      if (next !== was && depot.id) await ok(setDepotEdiLocation(depot.id, { location: next }));
      setWas(next.toUpperCase());
      setLoc(next.toUpperCase());
      setSaved(true);
      done();
    } catch (err) {
      setMsg(errText(err, t("networkError")));
    }
  }
  return (
    <form className="admin-form" onSubmit={(e) => void save(e)}>
      <label>
        {depot.name} · {t("ediLocation")}
        <input value={loc} maxLength={25} onChange={(e) => setLoc(e.target.value)} />
      </label>
      <button type="submit">{t("save")}</button>
      {saved && (
        <span className="ok-text" role="status">
          {t("saved")}
        </span>
      )}
      {msg && (
        <span className="error" role="alert">
          {msg}
        </span>
      )}
    </form>
  );
}

const list = (s: string) => s.split(",").map((x) => x.trim()).filter(Boolean);
const typeMapText = (p: LineEDIProfile) => p.typeMap.map((x) => `${x.ours}=${x.theirs}`).join(", ");
const typeMapOf = (s: string) =>
  list(s).map((x) => {
    const [ours = "", theirs = ""] = x.split("=");
    return { ours: ours.trim(), theirs: theirs.trim() };
  });

// One line's EDI form. Lists are kept as text while typing and split on save.
function LineForm({ line, done }: { line: LineEDI; done: () => void }) {
  const { t } = useT();
  const p = line.profile;
  const [mode, setMode] = useState<LineEDISettingsMode>(line.mode);
  const [f, setF] = useState({
    senderId: p.senderId, senderQual: p.senderQual, recipientId: p.recipientId, recipientQual: p.recipientQual, lineCode: p.lineCode,
    sendTo: p.sendTo.join(", "), allowedFrom: p.allowedFrom.join(", "), reportTo: p.reportTo.join(", "), reportTime: p.reportTime,
    typeMap: typeMapText(p), emptiesOnly: p.emptiesOnly, sendAperak: p.sendAperak, test: p.test,
  });
  const [msg, setMsg] = useState("");
  const [note, setNote] = useState("");
  const text = (k: keyof typeof f, label: Key, hint?: string) => (
    <label>
      {t(label)}
      <input value={f[k] as string} placeholder={hint} onChange={(e) => setF({ ...f, [k]: e.target.value })} />
    </label>
  );
  const check = (k: "emptiesOnly" | "sendAperak" | "test", label: Key) => (
    <label className="check">
      <input type="checkbox" checked={f[k]} onChange={(e) => setF({ ...f, [k]: e.target.checked })} />
      {t(label)}
    </label>
  );

  async function save() {
    setMsg("");
    setNote("");
    try {
      await ok(
        setLineEdi(line.lineId, {
          mode,
          profile: {
            ...f,
            sendTo: list(f.sendTo), allowedFrom: list(f.allowedFrom), reportTo: list(f.reportTo), typeMap: typeMapOf(f.typeMap),
          },
        }),
      );
      setNote(t("saved"));
      done();
    } catch (e) {
      setMsg(errText(e, t("networkError")));
    }
  }
  async function test() {
    setMsg("");
    setNote("");
    try {
      await ok(testLineEdi(line.lineId));
      setNote(t("ediTestQueued"));
    } catch (e) {
      setMsg(errText(e, t("networkError")));
    }
  }

  return (
    <form
      className="card"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <fieldset className="radios">
        <legend>{t("ediMode")}</legend>
        {Object.values(LineEDISettingsMode).map((m) => (
          <label key={m} className="check">
            <input type="radio" name={`mode-${line.lineId}`} checked={mode === m} onChange={() => setMode(m)} />
            {t(`ediMode_${m}` as Key)}
          </label>
        ))}
      </fieldset>
      <div className="admin-form">
        {text("senderId", "ediSenderId")}
        {text("senderQual", "ediSenderQual", "ZZZ")}
        {text("recipientId", "ediRecipientId")}
        {text("recipientQual", "ediRecipientQual", "ZZZ")}
        {text("lineCode", "ediLineCode", line.code)}
      </div>
      <div className="admin-form">
        {text("sendTo", "ediSendTo", "edi@line.example")}
        {text("allowedFrom", "ediAllowedFrom", "edi@line.example")}
      </div>
      <div className="admin-form">
        {text("typeMap", "ediTypeMap", "22G1=20GP, 45G1=40HC")}
        {text("reportTo", "ediReportTo")}
        {text("reportTime", "ediReportTime", "06:00")}
      </div>
      <div className="admin-form">
        {check("emptiesOnly", "ediEmptiesOnly")}
        {check("sendAperak", "ediSendAperak")}
        {check("test", "ediTest")}
      </div>
      <div className="row-actions">
        <button type="submit" className="primary">
          {t("save")}
        </button>
        <button type="button" disabled={line.mode !== "edi"} onClick={() => void test()}>
          {t("ediSendTest")}
        </button>
      </div>
      {note && (
        <p className="ok-text" role="status">
          {note}
        </p>
      )}
      {msg && (
        <p className="error" role="alert">
          {msg}
        </p>
      )}
    </form>
  );
}

// The Lines tab: our own party id first (a line cannot use EDI before it is set), then one row per line.
export function EDILines({ info, lines, depots, reload }: { info: EDIStatusInfo | null; lines: LineEDI[]; depots: Depot[]; reload: () => void }) {
  const { t } = useT();
  const [party, setParty] = useState<string | null>(null);
  const [open, setOpen] = useState("");
  const [msg, setMsg] = useState("");
  const [saved, setSaved] = useState(false);
  const value = party ?? info?.partyId ?? "";

  async function saveParty() {
    setMsg("");
    setSaved(false);
    try {
      await ok(setEdiCompany({ partyId: value.trim() }));
      setSaved(true);
      reload();
    } catch (e) {
      setMsg(errText(e, t("networkError")));
    }
  }

  return (
    <>
      <form
        className="card"
        onSubmit={(e) => {
          e.preventDefault();
          void saveParty();
        }}
      >
        <div className="admin-form">
          <label>
            {t("ediPartyId")}
            <input value={value} maxLength={35} onChange={(e) => setParty(e.target.value)} />
          </label>
          <button type="submit" className="primary">
            {t("save")}
          </button>
        </div>
        <p className="muted small">{t("ediPartyHint")}</p>
        {saved && (
          <p className="ok-text" role="status">
            {t("saved")}
          </p>
        )}
        {msg && (
          <p className="error" role="alert">
            {msg}
          </p>
        )}
      </form>
      {depots.length > 0 && (
        <div className="card">
          <h4>{t("ediDepotLocations")}</h4>
          {depots.map((d) => (
            <DepotLocation key={d.id} depot={d} done={reload} />
          ))}
          <p className="muted small">{t("ediLocationHint")}</p>
        </div>
      )}
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t("ediLine")}</th>
            <th>{t("ediMode")}</th>
          </tr>
        </thead>
        <tbody>
          {lines.map((l) => (
            <tr key={l.lineId}>
              <td>
                <button type="button" aria-expanded={open === l.lineId} onClick={() => setOpen(open === l.lineId ? "" : l.lineId)}>
                  {l.code}
                </button>
              </td>
              <td>{t(`ediMode_${l.mode}` as Key)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {lines.filter((l) => l.lineId === open).map((l) => (
        <LineForm key={l.lineId} line={l} done={reload} />
      ))}
    </>
  );
}
