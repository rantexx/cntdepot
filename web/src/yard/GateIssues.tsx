import { useList } from "@refinedev/core";
import { useEffect, useState } from "react";
import { errText, ReasonText, useReasonDetail } from "../components";
import { getMoveForm, resolveGateFlag, type GateFlag, type MoveForm } from "../gen/api";
import { ok } from "../hubApi";
import { useMoveEvents } from "../live";
import { useT } from "../i18n";

// One open flag: the container it is about, and a note field to resolve it.
function Issue({ flag, done }: { flag: GateFlag; done: () => void }) {
  const { t } = useT();
  const detail = useReasonDetail();
  const [form, setForm] = useState<MoveForm | null>(null);
  const [note, setNote] = useState("");
  const [msg, setMsg] = useState("");
  useEffect(() => {
    ok(getMoveForm(flag.moveId)).then(setForm, () => undefined);
  }, [flag.moveId]);

  async function resolve() {
    setMsg("");
    try {
      await resolveGateFlag(flag.id, { note: note.trim() });
      done();
    } catch (e) {
      setMsg(errText(e, t("networkError")));
    }
  }

  return (
    <tr>
      <td>
        <ReasonText code={flag.code} />
        {flag.detail && <div className="small muted">{detail(flag.detail)}</div>}
      </td>
      <td className="mono">
        {form?.visitId ? <a href={`#/visits/${form.visitId}`}>{form.number}</a> : (form?.number ?? "")}
        {form && <div className="small muted">{form.direction === "in" ? t("gateIn") : t("gateOut")}</div>}
      </td>
      <td>
        <div className="row-actions">
          <input value={note} onChange={(e) => setNote(e.target.value)} placeholder={t("resolveNote")} aria-label={t("resolveNote")} maxLength={500} />
          <button type="button" onClick={() => void resolve()}>
            {t("resolve")}
          </button>
        </div>
        {msg && (
          <p className="error small" role="alert">
            {msg}
          </p>
        )}
      </td>
    </tr>
  );
}

export function GateIssues() {
  const { t } = useT();
  const { result, query } = useList<GateFlag>({
    resource: "gate-flags",
    pagination: { mode: "off" },
    filters: [{ field: "open", operator: "eq", value: "true" }],
  });
  const refetch = () => void query.refetch();
  useMoveEvents(refetch, refetch); // a saved gate form may bring new flags
  const rows = result.data ?? [];
  return (
    <div className="page">
      <div className="page-head">
        <h1>
          {t("navIssues")}
          <small>{rows.length}</small>
        </h1>
      </div>
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t("issue")}</th>
            <th>{t("colContainer")}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {rows.map((f) => (
            <Issue key={f.id} flag={f} done={refetch} />
          ))}
        </tbody>
      </table>
      {rows.length === 0 && !query.isLoading && <p className="muted">{t("noIssues")}</p>}
    </div>
  );
}
