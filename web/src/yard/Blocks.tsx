import { useList } from "@refinedev/core";
import { useState, type FormEvent } from "react";
import { dateTime, errText } from "../components";
import { BlockKind, createBlock, setBlockActive, type Block } from "../gen/api";
import { useT, type Key } from "../i18n";

// The company's block list: a container number or a whole shipping line that may not pass the gate.
export function Blocks() {
  const { t } = useT();
  const { result, query } = useList<Block>({ resource: "blocks", pagination: { mode: "off" } });
  const rows = result.data ?? [];
  const [fresh, setFresh] = useState({ kind: BlockKind.container as BlockKind, value: "", reason: "" });
  const [msg, setMsg] = useState("");

  async function run(fn: () => Promise<unknown>, after?: () => void) {
    setMsg("");
    try {
      await fn();
      after?.();
      void query.refetch();
    } catch (e) {
      setMsg(errText(e, t("networkError")));
    }
  }
  const add = (e: FormEvent) => {
    e.preventDefault();
    void run(
      () => createBlock({ kind: fresh.kind, value: fresh.value.trim().toUpperCase(), reason: fresh.reason.trim() }),
      () => setFresh({ ...fresh, value: "", reason: "" }),
    );
  };

  return (
    <div className="page">
      <div className="page-head">
        <h1>
          {t("navBlocks")}
          <small>{rows.length}</small>
        </h1>
      </div>
      <form className="card admin-form" onSubmit={add}>
        <label>
          {t("blockKind")}
          <select value={fresh.kind} onChange={(e) => setFresh({ ...fresh, kind: e.target.value as BlockKind })}>
            {Object.values(BlockKind).map((k) => (
              <option key={k} value={k}>
                {t(`blockKind_${k}` as Key)}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t("value")}
          <input value={fresh.value} onChange={(e) => setFresh({ ...fresh, value: e.target.value })} required maxLength={64} />
        </label>
        <label className="grow">
          {t("reason")}
          <input value={fresh.reason} onChange={(e) => setFresh({ ...fresh, reason: e.target.value })} required maxLength={500} />
        </label>
        <button type="submit" className="primary">
          {t("add")}
        </button>
      </form>
      {msg && (
        <p className="error" role="alert">
          {msg}
        </p>
      )}
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t("blockKind")}</th>
            <th>{t("value")}</th>
            <th>{t("reason")}</th>
            <th>{t("created")}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {rows.map((b) => (
            <tr key={b.id} className={b.active ? undefined : "off"}>
              <td>{t(`blockKind_${b.kind}` as Key)}</td>
              <td className="mono">{b.value}</td>
              <td>{b.reason}</td>
              <td>{dateTime(b.createdAt)}</td>
              <td className="actions">
                <button type="button" onClick={() => void run(() => setBlockActive(b.id, { active: !b.active }))}>
                  {b.active ? t("deactivate") : t("reactivate")}
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 && !query.isLoading && <p className="muted">{t("noRows")}</p>}
    </div>
  );
}
