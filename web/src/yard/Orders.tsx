import { useList } from "@refinedev/core";
import { useEffect, useState, type FormEvent } from "react";
import { CsvButton, dateTime, errText } from "../components";
import { createOrder, listLines, OrderKind, OrderStatus, type Line, type Order, type OrderCreateItemsItem } from "../gen/api";
import { ok } from "../hubApi";
import { useT, type Key } from "../i18n";

const EMPTY_ITEM: OrderCreateItemsItem = { number: "", typeCode: "", fullEmpty: "E", quantity: 1 };

export function Orders() {
  const { t } = useT();
  const [f, setF] = useState({ q: "", kind: "", status: "" });
  const { result, query } = useList<Order>({
    resource: "orders",
    pagination: { mode: "off" },
    filters: [
      { field: "q", operator: "contains", value: f.q },
      { field: "kind", operator: "eq", value: f.kind },
      { field: "status", operator: "eq", value: f.status },
    ],
  });
  const rows = result.data ?? [];
  const [lines, setLines] = useState<Line[]>([]);
  useEffect(() => {
    ok(listLines({ perPage: 200 })).then(
      (r) => setLines(r.items),
      () => setLines([]),
    );
  }, []);
  const lineCode = (id: string | null) => lines.find((l) => l.id === id)?.code ?? "";

  const [draft, setDraft] = useState({ kind: OrderKind.release as OrderKind, reference: "", lineId: "", validUntil: "", note: "" });
  const [items, setItems] = useState<OrderCreateItemsItem[]>([{ ...EMPTY_ITEM }]);
  const [msg, setMsg] = useState("");
  const setItem = (i: number, patch: Partial<OrderCreateItemsItem>) => setItems(items.map((x, j) => (j === i ? { ...x, ...patch } : x)));

  async function add(e: FormEvent) {
    e.preventDefault();
    setMsg("");
    try {
      // An item names a container or a type with a quantity, never both.
      const clean = items.map((x) =>
        x.number?.trim()
          ? { number: x.number.trim().toUpperCase(), fullEmpty: x.fullEmpty }
          : { typeCode: x.typeCode?.trim().toUpperCase(), fullEmpty: x.fullEmpty, quantity: x.quantity ?? 1 },
      );
      const o = await ok(
        createOrder({
          kind: draft.kind,
          reference: draft.reference.trim(),
          lineId: draft.lineId || undefined,
          validUntil: draft.validUntil || undefined,
          note: draft.note.trim() || undefined,
          items: clean,
        }),
      );
      window.location.hash = `#/orders/${o.id}`;
    } catch (err) {
      setMsg(errText(err, t("networkError")));
    }
  }

  return (
    <div className="page">
      <div className="page-head">
        <h1>
          {t("navOrders")}
          <small>{rows.length}</small>
        </h1>
        <CsvButton name="orders" params={{ q: f.q, kind: f.kind, status: f.status }} />
      </div>
      <div className="filters">
        <label className="grow">
          {t("search")}
          <input type="search" value={f.q} onChange={(e) => setF({ ...f, q: e.target.value })} />
        </label>
        <label>
          {t("orderKind")}
          <select value={f.kind} onChange={(e) => setF({ ...f, kind: e.target.value })}>
            <option value="">{t("filterAll")}</option>
            {Object.values(OrderKind).map((k) => (
              <option key={k} value={k}>
                {t(`kind_${k}` as Key)}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t("colState")}
          <select value={f.status} onChange={(e) => setF({ ...f, status: e.target.value })}>
            <option value="">{t("filterAll")}</option>
            {Object.values(OrderStatus).map((s) => (
              <option key={s} value={s}>
                {t(`order_${s}` as Key)}
              </option>
            ))}
          </select>
        </label>
      </div>
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t("reference")}</th>
            <th>{t("orderKind")}</th>
            <th>{t("gateLine")}</th>
            <th>{t("validUntil")}</th>
            <th>{t("colState")}</th>
            <th>{t("created")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((o) => (
            <tr key={o.id} className={o.status === "open" ? undefined : "off"}>
              <td>
                <a href={`#/orders/${o.id}`}>{o.reference}</a>
              </td>
              <td>{t(`kind_${o.kind}` as Key)}</td>
              <td>{lineCode(o.lineId)}</td>
              <td>{o.validUntil ?? "—"}</td>
              <td>{t(`order_${o.status}` as Key)}</td>
              <td>{dateTime(o.createdAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 && !query.isLoading && <p className="muted">{t("noRows")}</p>}

      <form className="card" onSubmit={(e) => void add(e)}>
        <h3>{t("newOrder")}</h3>
        <div className="admin-form">
          <label>
            {t("orderKind")}
            <select value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value as OrderKind })}>
              {Object.values(OrderKind).map((k) => (
                <option key={k} value={k}>
                  {t(`kind_${k}` as Key)}
                </option>
              ))}
            </select>
          </label>
          <label>
            {t("reference")}
            <input value={draft.reference} onChange={(e) => setDraft({ ...draft, reference: e.target.value })} required maxLength={64} />
          </label>
          <label>
            {t("gateLine")}
            <select value={draft.lineId} onChange={(e) => setDraft({ ...draft, lineId: e.target.value })}>
              <option value="">—</option>
              {lines.map((l) => (
                <option key={l.id} value={l.id}>
                  {l.code}
                </option>
              ))}
            </select>
          </label>
          <label>
            {t("validUntil")}
            <input type="date" value={draft.validUntil} onChange={(e) => setDraft({ ...draft, validUntil: e.target.value })} />
          </label>
          <label className="grow">
            {t("note")}
            <input value={draft.note} onChange={(e) => setDraft({ ...draft, note: e.target.value })} maxLength={500} />
          </label>
        </div>
        <h4>{t("orderItems")}</h4>
        {items.map((x, i) => (
          <div className="admin-form" key={i}>
            <label>
              {t("number")}
              <input value={x.number ?? ""} onChange={(e) => setItem(i, { number: e.target.value })} placeholder={t("orSpecificNumber")} />
            </label>
            <label>
              {t("typeCode")}
              <input value={x.typeCode ?? ""} disabled={!!x.number?.trim()} onChange={(e) => setItem(i, { typeCode: e.target.value })} />
            </label>
            <label>
              {t("gateFullEmpty")}
              <select value={x.fullEmpty} onChange={(e) => setItem(i, { fullEmpty: e.target.value })}>
                <option value="E">{t("gateEmpty")}</option>
                <option value="F">{t("gateFull")}</option>
              </select>
            </label>
            <label>
              {t("quantity")}
              <input
                type="number"
                min={1}
                value={x.quantity ?? 1}
                disabled={!!x.number?.trim()}
                onChange={(e) => setItem(i, { quantity: Number(e.target.value) || 1 })}
              />
            </label>
            {items.length > 1 && (
              <button type="button" onClick={() => setItems(items.filter((_, j) => j !== i))}>
                {t("remove")}
              </button>
            )}
          </div>
        ))}
        <div className="row-actions">
          <button type="button" onClick={() => setItems([...items, { ...EMPTY_ITEM }])}>
            {t("addItem")}
          </button>
          <button type="submit" className="primary">
            {t("createOrder")}
          </button>
        </div>
        {msg && (
          <p className="error" role="alert">
            {msg}
          </p>
        )}
      </form>
    </div>
  );
}
