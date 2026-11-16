import { useCallback, useEffect, useState, type FormEvent } from "react";
import { createCompany, listCompanies, type Company } from "../gen/api";
import { ok } from "../hubApi";
import { useT } from "../i18n";

// Platform admins: every company on the platform, and a form to start a new one.
export function Companies() {
  const { t } = useT();
  const [rows, setRows] = useState<Company[]>([]);
  const [fresh, setFresh] = useState({ name: "", country: "", ownerEmail: "", ocr: true });
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      setRows(await ok(listCompanies()));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  async function add(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      await ok(
        createCompany({
          name: fresh.name.trim(),
          country: fresh.country.trim(),
          ownerEmail: fresh.ownerEmail.trim(),
          modules: fresh.ocr ? ["ocr"] : [],
        }),
      );
      setFresh({ name: "", country: "", ownerEmail: "", ocr: true });
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("navCompanies")}</h1>
      </div>
      <form className="card admin-form" onSubmit={(e) => void add(e)}>
        <label>
          {t("colName")}
          <input value={fresh.name} onChange={(e) => setFresh({ ...fresh, name: e.target.value })} required />
        </label>
        <label>
          {t("country")}
          <input value={fresh.country} onChange={(e) => setFresh({ ...fresh, country: e.target.value })} placeholder="TR" required />
        </label>
        <label>
          {t("ownerEmail")}
          <input type="email" value={fresh.ownerEmail} onChange={(e) => setFresh({ ...fresh, ownerEmail: e.target.value })} required />
        </label>
        <label className="check">
          <input type="checkbox" checked={fresh.ocr} onChange={(e) => setFresh({ ...fresh, ocr: e.target.checked })} />
          {t("module_ocr")}
        </label>
        <button type="submit" className="primary">
          {t("add")}
        </button>
      </form>
      {error && <p className="error" role="alert">{error}</p>}
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t("colName")}</th>
            <th>{t("country")}</th>
            <th>{t("modules")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((c) => (
            <tr key={c.id}>
              <td>{c.name}</td>
              <td>{c.country}</td>
              <td>{c.modules.join(", ") || "–"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
