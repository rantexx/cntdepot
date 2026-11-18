import { useEffect, useState, type FormEvent } from "react";
import { errText } from "../components";
import { useSession } from "../session";
import { getYardSettings, setDepotEdiLocation, setYardSettings, YardSettingsRefusalMode, type YardSettings as Settings } from "../gen/api";
import { ok } from "../hubApi";
import { useT, type Key } from "../i18n";

// The depot's EDI location code. The depot record does not return it, so the field starts empty and a save replaces it.
function EdiLocation() {
  const { t } = useT();
  const { current } = useSession();
  const [loc, setLoc] = useState("");
  const [msg, setMsg] = useState("");
  const [saved, setSaved] = useState(false);
  async function save(e: FormEvent) {
    e.preventDefault();
    setMsg("");
    setSaved(false);
    try {
      await ok(setDepotEdiLocation(current?.depotId ?? "", { location: loc.trim() }));
      setSaved(true);
    } catch (err) {
      setMsg(errText(err, t("networkError")));
    }
  }
  return (
    <form className="card" onSubmit={(e) => void save(e)}>
      <label>
        {t("ediLocation")}
        <input value={loc} maxLength={25} onChange={(e) => setLoc(e.target.value)} />
      </label>
      <p className="muted small">{t("ediLocationHint")}</p>
      <button type="submit" className="primary">
        {t("save")}
      </button>
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
  );
}

// The owner's yard rules: what the gate does with a refused truck, walk-ins and how long an EIR link lives.
export function YardSettings() {
  const { t } = useT();
  const [s, setS] = useState<Settings | null>(null);
  const [msg, setMsg] = useState("");
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    ok(getYardSettings()).then(setS, (e) => setMsg(errText(e, t("networkError"))));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  if (!s) return msg ? <p className="error">{msg}</p> : <p className="boot">{t("loading")}</p>;

  async function save(e: FormEvent) {
    e.preventDefault();
    setMsg("");
    setSaved(false);
    try {
      setS(await ok(setYardSettings(s!)));
      setSaved(true);
    } catch (err) {
      setMsg(errText(err, t("networkError")));
    }
  }

  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("navYardSettings")}</h1>
      </div>
      <form className="card" onSubmit={(e) => void save(e)}>
        <fieldset className="radios">
          <legend>{t("refusalMode")}</legend>
          {Object.values(YardSettingsRefusalMode).map((m) => (
            <label key={m} className="check">
              <input type="radio" name="mode" checked={s.refusalMode === m} onChange={() => setS({ ...s, refusalMode: m })} />
              <span>
                <b>{t(`mode_${m}` as Key)}</b>
                <span className="muted small"> {t(`modeHint_${m}` as Key)}</span>
              </span>
            </label>
          ))}
        </fieldset>
        <label className="check">
          <input type="checkbox" checked={s.walkinsAllowed} onChange={(e) => setS({ ...s, walkinsAllowed: e.target.checked })} />
          {t("walkinsAllowed")}
        </label>
        <label>
          {t("eirTtlDays")}
          <input
            type="number"
            min={1}
            value={s.eirTtlDays}
            onChange={(e) => setS({ ...s, eirTtlDays: Math.max(1, Number(e.target.value) || 1) })}
          />
        </label>
        <button type="submit" className="primary">
          {t("save")}
        </button>
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
      <EdiLocation />
    </div>
  );
}
