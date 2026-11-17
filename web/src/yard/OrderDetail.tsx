import { useCallback, useEffect, useState } from "react";
import { dateTime, errText } from "../components";
import { cancelOrder, getOrder, type OrderDetail as Detail } from "../gen/api";
import { ok } from "../hubApi";
import { useT, type Key } from "../i18n";

export function OrderDetail({ id }: { id: string }) {
  const { t } = useT();
  const [o, setO] = useState<Detail | null>(null);
  const [msg, setMsg] = useState("");
  const load = useCallback(() => {
    ok(getOrder(id)).then(setO, (e) => setMsg(errText(e, t("networkError"))));
  }, [id]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(load, [load]);

  async function cancel() {
    if (!window.confirm(t("confirmCancelOrder"))) return;
    try {
      await cancelOrder(id);
      load();
    } catch (e) {
      setMsg(errText(e, t("networkError")));
    }
  }

  if (!o) return msg ? <p className="error">{msg}</p> : <p className="boot">{t("loading")}</p>;
  return (
    <div className="page">
      <a className="back" href="#/orders">
        {t("back")}
      </a>
      <div className="page-head">
        <h1>
          {o.reference}
          <small>{t(`kind_${o.kind}` as Key)}</small>
        </h1>
        <span className="chip">{t(`order_${o.status}` as Key)}</span>
        {o.status === "open" && (
          <button type="button" className="danger" onClick={() => void cancel()}>
            {t("cancelOrder")}
          </button>
        )}
      </div>
      <section className="card">
        <dl className="facts-grid">
          <div>
            <dt>{t("validUntil")}</dt>
            <dd>{o.validUntil ?? "—"}</dd>
          </div>
          <div>
            <dt>{t("created")}</dt>
            <dd>{dateTime(o.createdAt)}</dd>
          </div>
          <div>
            <dt>{t("note")}</dt>
            <dd>{o.note || "—"}</dd>
          </div>
        </dl>
      </section>
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t("colContainer")}</th>
            <th>{t("typeCode")}</th>
            <th>{t("gateFullEmpty")}</th>
            <th>{t("itemUsage")}</th>
          </tr>
        </thead>
        <tbody>
          {o.items.map((i) => (
            <tr key={i.id}>
              <td>{i.number ?? "—"}</td>
              <td>{i.typeCode ?? "—"}</td>
              <td>{i.fullEmpty === "F" ? t("gateFull") : t("gateEmpty")}</td>
              <td>
                <span className="progress" aria-hidden="true">
                  <span style={{ width: `${Math.min(100, (i.used / i.quantity) * 100)}%` }} />
                </span>
                {i.used} / {i.quantity}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {msg && (
        <p className="error" role="alert">
          {msg}
        </p>
      )}
    </div>
  );
}
