import { useCallback, useEffect, useState, type FormEvent } from "react";
import { createDepot, getCompany, listDepots, setModules, type Company as CompanyT, type Depot } from "../gen/api";
import { ok } from "../hubApi";
import { useT } from "../i18n";
import { useSessionActions } from "../session";

// Every module a company can switch on. The server refuses unknown names.
const MODULES = ["ocr"] as const;

export function Company() {
  const { t } = useT();
  const { reload } = useSessionActions();
  const [company, setCompany] = useState<CompanyT | null>(null);
  const [depots, setDepots] = useState<Depot[]>([]);
  const [fresh, setFresh] = useState({ name: "", country: "", timezone: "" });
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      setCompany(await ok(getCompany()));
      setDepots(await ok(listDepots()));
    } catch (e) {
      setError(String(e));
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  async function toggle(m: string, on: boolean) {
    if (!company) return;
    setError("");
    try {
      const mods = on ? [...company.modules, m] : company.modules.filter((x) => x !== m);
      setCompany(await ok(setModules({ modules: mods })));
      await reload(); // the rail follows the modules
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function addDepot(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      await ok(createDepot(fresh));
      setFresh({ name: "", country: "", timezone: "" });
      await load();
      await reload(); // the switcher lists the new depot
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  if (!company) return <p role={error ? "alert" : undefined}>{error || t("loading")}</p>;
  return (
    <div className="page">
      <div className="page-head">
        <h1>
          {company.name}
          <small>{company.country}</small>
        </h1>
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="card">
        <h3>{t("modules")}</h3>
        {MODULES.map((m) => (
          <label key={m} className="check">
            <input type="checkbox" checked={company.modules.includes(m)} onChange={(e) => void toggle(m, e.target.checked)} />
            {t(`module_${m}`)}
          </label>
        ))}
        <p className="small muted">{t("ocrOffHint")}</p>
      </div>
      <h3>{t("depots")}</h3>
      <table className="admin-table">
        <tbody>
          {depots.map((d) => (
            <tr key={d.id}>
              <td>{d.name}</td>
              <td>{d.country}</td>
              <td>{d.timezone}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <form className="card admin-form" onSubmit={(e) => void addDepot(e)}>
        <label>
          {t("colName")}
          <input value={fresh.name} onChange={(e) => setFresh({ ...fresh, name: e.target.value })} required />
        </label>
        <label>
          {t("country")}
          <input value={fresh.country} onChange={(e) => setFresh({ ...fresh, country: e.target.value })} placeholder="TR" required />
        </label>
        <label>
          {t("timezone")}
          <input value={fresh.timezone} onChange={(e) => setFresh({ ...fresh, timezone: e.target.value })} placeholder="Europe/Istanbul" required />
        </label>
        <button type="submit" className="primary">
          {t("add")}
        </button>
      </form>
    </div>
  );
}
