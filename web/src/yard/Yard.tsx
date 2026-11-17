import { useList } from "@refinedev/core";
import { useEffect, useState } from "react";
import { Age, ContainerNumber, CsvButton, daysSince, dateTime } from "../components";
import { listLines, type Line, type VisitListItem } from "../gen/api";
import { ok } from "../hubApi";
import { useT } from "../i18n";

const PER_PAGE = 25;
const GRADES = ["", "A", "B", "C", "FOOD"];

// The containers in the yard (or gone): filters, ageing colour and the hold sign.
export function Yard() {
  const { t } = useT();
  const [f, setF] = useState({ q: "", status: "in_yard", lineId: "", type: "", grade: "", hold: false, minDays: "" });
  const [page, setPage] = useState(1);
  const [lines, setLines] = useState<Line[]>([]);
  useEffect(() => {
    ok(listLines({ perPage: 200 })).then(
      (r) => setLines(r.items),
      () => setLines([]),
    );
  }, []);
  const set = (patch: Partial<typeof f>) => {
    setF({ ...f, ...patch });
    setPage(1);
  };
  const filters = [
    { field: "q", operator: "contains" as const, value: f.q },
    { field: "status", operator: "eq" as const, value: f.status },
    { field: "lineId", operator: "eq" as const, value: f.lineId },
    { field: "type", operator: "eq" as const, value: f.type },
    { field: "grade", operator: "eq" as const, value: f.grade },
    { field: "hold", operator: "eq" as const, value: f.hold ? "true" : "" },
    { field: "minDays", operator: "gte" as const, value: f.minDays },
  ];
  const { result } = useList<VisitListItem>({ resource: "visits", pagination: { currentPage: page, pageSize: PER_PAGE }, filters });
  const rows = result.data ?? [];
  const total = result.total ?? 0;
  const code = (id: string | null) => lines.find((l) => l.id === id)?.code ?? "";
  const exportParams = Object.fromEntries(filters.map((x) => [x.field, String(x.value)]));

  return (
    <div className="page">
      <div className="page-head">
        <h1>
          {t("navYard")}
          <small>{total}</small>
        </h1>
        <CsvButton name="visits" params={exportParams} />
      </div>
      <div className="filters">
        <label className="grow">
          {t("search")}
          <input type="search" value={f.q} placeholder={t("number")} onChange={(e) => set({ q: e.target.value })} />
        </label>
        <label>
          {t("colState")}
          <select value={f.status} onChange={(e) => set({ status: e.target.value })}>
            <option value="in_yard">{t("inYard")}</option>
            <option value="out">{t("leftYard")}</option>
            <option value="">{t("filterAll")}</option>
          </select>
        </label>
        <label>
          {t("gateLine")}
          <select value={f.lineId} onChange={(e) => set({ lineId: e.target.value })}>
            <option value="">{t("filterAll")}</option>
            {lines.map((l) => (
              <option key={l.id} value={l.id}>
                {l.code}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t("typeCode")}
          <input value={f.type} maxLength={8} onChange={(e) => set({ type: e.target.value.toUpperCase() })} />
        </label>
        <label>
          {t("grade")}
          <select value={f.grade} onChange={(e) => set({ grade: e.target.value })}>
            {GRADES.map((g) => (
              <option key={g} value={g}>
                {g || t("filterAll")}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t("minDays")}
          <input type="number" min={0} value={f.minDays} onChange={(e) => set({ minDays: e.target.value })} />
        </label>
        <label className="check">
          <input type="checkbox" checked={f.hold} onChange={(e) => set({ hold: e.target.checked })} />
          {t("onHoldOnly")}
        </label>
      </div>
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t("colContainer")}</th>
            <th>{t("colType")}</th>
            <th>{t("gateLine")}</th>
            <th>{t("gateFullEmpty")}</th>
            <th>{t("grade")}</th>
            <th>{t("position")}</th>
            <th>{t("gateIn")}</th>
            <th>{t("daysInYard")}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {rows.map((v) => (
            <tr key={v.id} className={v.status === "out" ? "off" : undefined}>
              <td>
                <a href={`#/visits/${v.id}`}>
                  <ContainerNumber number={v.number} />
                </a>
              </td>
              <td>{v.typeCode}</td>
              <td>{code(v.lineId)}</td>
              <td>{v.fullEmpty === "F" ? t("gateFull") : t("gateEmpty")}</td>
              <td>{v.grade}</td>
              <td>{[v.block, v.rowNo, v.tier].filter(Boolean).join("-")}</td>
              <td>{dateTime(v.inAt)}</td>
              <td>{v.status === "in_yard" ? <Age days={daysSince(v.inAt)} /> : t("leftYard")}</td>
              <td>{v.onHold && <span className="hold-sign">{t("onHold")}</span>}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 && <p className="muted">{t("noRows")}</p>}
      <div className="pager">
        <button type="button" disabled={page <= 1} onClick={() => setPage(page - 1)}>
          {t("prev")}
        </button>
        <span>
          {page} / {Math.max(1, Math.ceil(total / PER_PAGE))}
        </span>
        <button type="button" disabled={page * PER_PAGE >= total} onClick={() => setPage(page + 1)}>
          {t("next")}
        </button>
      </div>
    </div>
  );
}
