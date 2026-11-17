import { useState, type FormEvent } from "react";
import { ApiError } from "../hubApi";
import { errText } from "../components";
import { importOrders, importStock, type ImportRejected, type ImportRowError } from "../gen/api";
import { useT, type Key } from "../i18n";

type Kind = "stock" | "orders";

// One import: the template to start from, a file, and either the count imported or every bad row.
function ImportCard({ kind }: { kind: Kind }) {
  const { t } = useT();
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<number | null>(null);
  const [rows, setRows] = useState<ImportRowError[]>([]);
  const [msg, setMsg] = useState("");

  async function send(e: FormEvent) {
    e.preventDefault();
    if (!file) return;
    setBusy(true);
    setDone(null);
    setRows([]);
    setMsg("");
    try {
      const r = await (kind === "stock" ? importStock({ file }) : importOrders({ file }));
      setDone((r.data as { imported: number }).imported);
    } catch (err) {
      // 422: nothing was imported and the body lists the bad rows.
      if (err instanceof ApiError && err.status === 422) {
        const body = err.body as ImportRejected;
        setRows(body.rows ?? []);
        setMsg(body.error.message);
      } else setMsg(errText(err, t("networkError")));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="card" onSubmit={(e) => void send(e)}>
      <h3>{t(`import_${kind}` as Key)}</h3>
      <p className="muted">{t("importHint")}</p>
      <div className="admin-form">
        <a className="button" href={`/api/import/templates/${kind}.csv`} download>
          {t("downloadTemplate")}
        </a>
        <label className="grow">
          {t("csvFile")}
          <input type="file" accept=".csv,text/csv" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
        </label>
        <button type="submit" className="primary" disabled={!file || busy}>
          {t("upload")}
        </button>
      </div>
      {done !== null && (
        <p className="ok-text" role="status">
          {t("imported")}: {done}
        </p>
      )}
      {msg && (
        <p className="error" role="alert">
          {msg}
        </p>
      )}
      {rows.length > 0 && (
        <table className="admin-table">
          <thead>
            <tr>
              <th>{t("row")}</th>
              <th>{t("field")}</th>
              <th>{t("error")}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={i}>
                <td>{r.row || t("header")}</td>
                <td>{r.field}</td>
                <td>{r.message}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </form>
  );
}

export function Import() {
  const { t } = useT();
  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("navImport")}</h1>
      </div>
      <ImportCard kind="stock" />
      <ImportCard kind="orders" />
    </div>
  );
}
