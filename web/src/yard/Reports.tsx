import { useEffect, useState } from "react";
import { CsvButton, dateTime, errText } from "../components";
import { gateLog, reportAgeing, reportDashboard, type AgeingRow, type Dashboard, type GateLogRow } from "../gen/api";
import { ok } from "../hubApi";
import { useT } from "../i18n";

const BUCKETS = ["0-7", "8-30", "31-90", "90+"];
const today = () => new Date().toLocaleDateString("sv"); // YYYY-MM-DD in the computer's own day

export function Reports() {
  const { t } = useT();
  const [dash, setDash] = useState<Dashboard | null>(null);
  const [ageing, setAgeing] = useState<AgeingRow[]>([]);
  const [log, setLog] = useState<GateLogRow[]>([]);
  const [range, setRange] = useState({ from: today(), to: today() });
  const [msg, setMsg] = useState("");

  useEffect(() => {
    ok(reportDashboard()).then(setDash, (e) => setMsg(errText(e, t("networkError"))));
    ok(reportAgeing()).then(setAgeing, () => undefined);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    ok(gateLog({ from: range.from || undefined, to: range.to || undefined })).then(setLog, (e) => setMsg(errText(e, t("networkError"))));
  }, [range]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("navReports")}</h1>
      </div>
      {msg && (
        <p className="error" role="alert">
          {msg}
        </p>
      )}
      {dash && (
        <div className="tiles">
          <div>
            <b>{dash.inToday}</b>
            <span>{t("inToday")}</span>
          </div>
          <div>
            <b>{dash.outToday}</b>
            <span>{t("outToday")}</span>
          </div>
          <div>
            <b>{dash.inYardByLine.reduce((n, l) => n + l.count, 0)}</b>
            <span>{t("inYard")}</span>
          </div>
          <div className={dash.openHolds > 0 ? "bad" : undefined}>
            <b>{dash.openHolds}</b>
            <span>{t("openHolds")}</span>
          </div>
          <div className={dash.openFlags > 0 ? "bad" : undefined}>
            <b>{dash.openFlags}</b>
            <span>{t("openFlags")}</span>
          </div>
        </div>
      )}
      {dash && dash.inYardByLine.length > 0 && (
        <p>
          {dash.inYardByLine.map((l) => (
            <span key={l.lineId} className="chip">
              {l.lineCode || "—"} {l.count}
            </span>
          ))}
        </p>
      )}

      <div className="page-head">
        <h3>{t("ageing")}</h3>
        <CsvButton name="ageing" />
      </div>
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t("gateLine")}</th>
            {BUCKETS.map((b) => (
              <th key={b}>
                {b} {t("daysShort")}
              </th>
            ))}
            <th>{t("total")}</th>
          </tr>
        </thead>
        <tbody>
          {ageing.map((r) => (
            <tr key={r.lineId}>
              <td>{r.lineCode || "—"}</td>
              {BUCKETS.map((b) => (
                <td key={b}>{r.buckets[b] ?? 0}</td>
              ))}
              <td className="strong">{r.total}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {ageing.length === 0 && <p className="muted">{t("noRows")}</p>}

      <div className="page-head">
        <h3>{t("gateLog")}</h3>
        <CsvButton name="gate-log" params={range} />
      </div>
      <div className="filters">
        <label>
          {t("from")}
          <input type="date" value={range.from} onChange={(e) => setRange({ ...range, from: e.target.value })} />
        </label>
        <label>
          {t("to")}
          <input type="date" value={range.to} onChange={(e) => setRange({ ...range, to: e.target.value })} />
        </label>
      </div>
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t("colTime")}</th>
            <th>{t("gateIn")} / {t("gateOut")}</th>
            <th>{t("colContainer")}</th>
            <th>{t("gateTruck")}</th>
            <th>{t("gateDriver")}</th>
            <th>{t("gateLine")}</th>
            <th>{t("openFlags")}</th>
          </tr>
        </thead>
        <tbody>
          {log.map((r) => (
            <tr key={r.id} className={r.voided ? "off" : undefined}>
              <td>{dateTime(r.at)}</td>
              <td>{r.direction === "in" ? t("gateIn") : t("gateOut")}</td>
              <td className="mono">{r.number}</td>
              <td>{r.truckPlate}</td>
              <td>{r.driverName}</td>
              <td>{r.lineCode}</td>
              <td>{r.flags > 0 ? r.flags : ""}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {log.length === 0 && <p className="muted">{t("noRows")}</p>}
    </div>
  );
}
