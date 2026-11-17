import { useList } from "@refinedev/core";
import { useState } from "react";
import { ContainerNumber, dateTime, errText, MovePhotos, ReasonText } from "../components";
import { decideApproval, type Approval } from "../gen/api";
import { useMoveEvents } from "../live";
import { useT } from "../i18n";

// The managers' queue of refused trucks the clerk asked to pass. It follows the hub's live stream
// ("approval" events) and reloads after every reconnect, so no polling is needed.
export function Approvals() {
  const { t } = useT();
  const { result, query } = useList<Approval>({
    resource: "approvals",
    pagination: { mode: "off" },
    filters: [{ field: "status", operator: "eq", value: "pending" }],
  });
  const refetch = () => void query.refetch();
  useMoveEvents(refetch, refetch, "approval");
  const rows = result.data ?? [];
  const [msg, setMsg] = useState("");

  async function decide(id: string, approve: boolean) {
    setMsg("");
    try {
      await decideApproval(id, { approve });
    } catch (e) {
      setMsg(errText(e, t("networkError"))); // e.g. another manager decided first
    }
    refetch();
  }

  return (
    <div className="page">
      <div className="page-head">
        <h1>
          {t("navApprovals")}
          <small>{rows.length}</small>
        </h1>
      </div>
      {msg && (
        <p className="error" role="alert">
          {msg}
        </p>
      )}
      {rows.length === 0 && !query.isLoading && <p className="muted">{t("noApprovals")}</p>}
      {rows.map((a) => (
        <section key={a.id} className="card approval">
          <div className="page-head">
            <h3>
              <ContainerNumber number={a.number} />
            </h3>
            <span className="muted">{dateTime(a.requestedAt)}</span>
          </div>
          <p>
            {a.reasons.map((r) => (
              <span key={r.code} className="chip" title={r.detail}>
                <ReasonText code={r.code} />
              </span>
            ))}
          </p>
          <MovePhotos moveId={a.moveId} />
          <div className="row-actions">
            <button type="button" className="primary" onClick={() => void decide(a.id, true)}>
              {t("approve")}
            </button>
            <button type="button" className="danger" onClick={() => void decide(a.id, false)}>
              {t("deny")}
            </button>
          </div>
        </section>
      ))}
    </div>
  );
}
