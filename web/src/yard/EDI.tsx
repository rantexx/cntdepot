import { useCallback, useEffect, useState } from "react";
import { errText } from "../components";
import {
  EDIDirection,
  EDIKind,
  EDIStatus,
  getEdiStatus,
  listDepots,
  listEdiMessages,
  listLineEdi,
  processEdiMessage,
  resendEdiMessage,
  uploadEdi,
  type Depot,
  type EDIMessage,
  type EDIStatusInfo,
  type LineEDI,
} from "../gen/api";
import { ok } from "../hubApi";
import { useT, type Key } from "../i18n";
import { EDILines } from "./EDILines";

const GOOD: string[] = [EDIStatus.sent, EDIStatus.accepted, EDIStatus.done];
const BAD: string[] = [EDIStatus.failed, EDIStatus.rejected, EDIStatus.error, EDIStatus.unmatched];

function Chip({ status }: { status: EDIStatus }) {
  const { t } = useT();
  const color = GOOD.includes(status) ? "var(--safe)" : BAD.includes(status) ? "var(--stop)" : undefined;
  return (
    <span className="chip" style={color ? { background: color, color: "#fff" } : undefined}>
      {t(`ediStatus_${status}` as Key)}
    </span>
  );
}

const fmt = (iso: string) => new Date(iso).toLocaleString([], { dateStyle: "short", timeStyle: "short" });

// One message opened: problems, links, the file as readable segments and the actions that fit it.
function Detail({ m, lines, depots, done }: { m: EDIMessage; lines: LineEDI[]; depots: Depot[]; done: () => void }) {
  const { t } = useT();
  const [raw, setRaw] = useState("");
  const [lineId, setLineId] = useState(m.lineId ?? "");
  const [depotId, setDepotId] = useState(m.depotId ?? "");
  const [msg, setMsg] = useState("");
  useEffect(() => {
    void fetch(`/api/edi/messages/${m.id}/raw`).then((r) => r.text()).then(setRaw, () => setRaw(""));
  }, [m.id]);

  async function act(p: Promise<unknown>) {
    setMsg("");
    try {
      await p;
      done();
    } catch (e) {
      setMsg(errText(e, t("networkError")));
    }
  }
  const via = m.direction === "in" && m.via ? (m.via === "upload" ? t("ediViaUpload") : m.trusted ? t("ediViaMailOk") : t("ediViaMailNo")) : "";

  return (
    <tr>
      <td colSpan={6}>
        <div className="card">
          <p className="small muted">
            {m.fileName} · {t("ediFunction")} {m.function} · {m.sender}
            {via && ` · ${via}`}
          </p>
          {m.problems.length > 0 && (
            <>
              <h4>{t("ediProblems")}</h4>
              <ul>
                {m.problems.map((p, i) => (
                  <li key={i} className={p.code.startsWith("E") ? "error" : "warn"}>
                    <span className="mono">{p.code}</span> {p.text}
                  </li>
                ))}
              </ul>
            </>
          )}
          <div className="row-actions">
            {m.orderId && <a href={`#/orders/${m.orderId}`}>{t("ediOrder")}</a>}
            {m.moveId && <a href={`#/moves/${m.moveId}`}>{t("ediMove")}</a>}
            <a className="button" href={`/api/edi/messages/${m.id}/raw`} download={m.fileName || undefined}>
              {t("ediDownload")}
            </a>
            {m.direction === "out" && (
              <button type="button" onClick={() => void act(resendEdiMessage(m.id))}>
                {t("ediResend")}
              </button>
            )}
          </div>
          {m.direction === "in" && (m.status === "unmatched" || m.status === "error") && (
            <div className="admin-form">
              <label>
                {t("ediLine")}
                <select value={lineId} onChange={(e) => setLineId(e.target.value)}>
                  <option value="" />
                  {lines.map((l) => (
                    <option key={l.lineId} value={l.lineId}>
                      {l.code}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                {t("ediDepot")}
                <select value={depotId} onChange={(e) => setDepotId(e.target.value)}>
                  <option value="" />
                  {depots.map((d) => (
                    <option key={d.id} value={d.id}>
                      {d.name}
                    </option>
                  ))}
                </select>
              </label>
              <button type="button" className="primary" onClick={() => void act(processEdiMessage(m.id, { lineId: lineId || undefined, depotId: depotId || undefined }))}>
                {t("ediProcess")}
              </button>
            </div>
          )}
          {msg && (
            <p className="error small" role="alert">
              {msg}
            </p>
          )}
          <h4>{t("ediRaw")}</h4>
          <pre>{raw.split("'").map((s, i, a) => (i < a.length - 1 ? `${s.replace(/^\s+/, "")}'\n` : s.trim() ? s : "")).join("")}</pre>
        </div>
      </td>
    </tr>
  );
}

function Messages({ lines, depots }: { lines: LineEDI[]; depots: Depot[] }) {
  const { t } = useT();
  const [f, setF] = useState({ direction: "", kind: "", status: "", lineId: "", from: "", to: "" });
  const [rows, setRows] = useState<EDIMessage[]>([]);
  const [open, setOpen] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [msg, setMsg] = useState("");
  const [uploaded, setUploaded] = useState<number | null>(null);

  const load = useCallback(() => {
    // "to" is exclusive in the API: a picked day includes that whole day.
    const to = f.to ? new Date(new Date(f.to).getTime() + 86_400_000).toISOString() : undefined;
    ok(
      listEdiMessages({
        direction: (f.direction || undefined) as EDIDirection | undefined,
        kind: (f.kind || undefined) as EDIKind | undefined,
        status: (f.status || undefined) as EDIStatus | undefined,
        lineId: f.lineId || undefined,
        from: f.from ? new Date(f.from).toISOString() : undefined,
        to,
        limit: 200,
      }),
    ).then(
      (r) => setRows(r.messages),
      (e) => setMsg(errText(e, t("networkError"))),
    );
  }, [f]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(load, [load]);

  async function send() {
    setMsg("");
    setUploaded(null);
    try {
      const r = await ok(uploadEdi({ file: files }));
      setUploaded(r.messages.length);
      setFiles([]);
      load();
    } catch (e) {
      setMsg(errText(e, t("networkError")));
    }
  }
  const pick = (k: keyof typeof f, v: string) => setF({ ...f, [k]: v });
  const lineCode = (id: string | null) => lines.find((l) => l.lineId === id)?.code ?? "";

  return (
    <>
      <form
        className="card"
        onSubmit={(e) => {
          e.preventDefault();
          void send();
        }}
      >
        <div className="admin-form">
          <label className="grow">
            {t("ediUploadFiles")}
            <input type="file" multiple key={uploaded ?? "x"} onChange={(e) => setFiles([...(e.target.files ?? [])])} />
          </label>
          <button type="submit" className="primary" disabled={files.length === 0}>
            {t("upload")}
          </button>
        </div>
        {uploaded !== null && (
          <p className="ok-text" role="status">
            {t("ediUploaded")}: {uploaded}
          </p>
        )}
        {msg && (
          <p className="error" role="alert">
            {msg}
          </p>
        )}
      </form>
      <div className="admin-form">
        <label>
          {t("ediDirection")}
          <select value={f.direction} onChange={(e) => pick("direction", e.target.value)}>
            <option value="">{t("ediAll")}</option>
            {Object.values(EDIDirection).map((d) => (
              <option key={d} value={d}>
                {d === "in" ? t("ediDirIn") : t("ediDirOut")}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t("ediKind")}
          <select value={f.kind} onChange={(e) => pick("kind", e.target.value)}>
            <option value="">{t("ediAll")}</option>
            {Object.values(EDIKind).map((k) => (
              <option key={k} value={k}>
                {t(`ediKind_${k}` as Key)}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t("ediStatus")}
          <select value={f.status} onChange={(e) => pick("status", e.target.value)}>
            <option value="">{t("ediAll")}</option>
            {Object.values(EDIStatus).map((s) => (
              <option key={s} value={s}>
                {t(`ediStatus_${s}` as Key)}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t("ediLine")}
          <select value={f.lineId} onChange={(e) => pick("lineId", e.target.value)}>
            <option value="">{t("ediAll")}</option>
            {lines.map((l) => (
              <option key={l.lineId} value={l.lineId}>
                {l.code}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t("from")}
          <input type="date" value={f.from} onChange={(e) => pick("from", e.target.value)} />
        </label>
        <label>
          {t("to")}
          <input type="date" value={f.to} onChange={(e) => pick("to", e.target.value)} />
        </label>
        <button type="button" onClick={load}>
          {t("ediRefresh")}
        </button>
      </div>
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t("ediTime")}</th>
            <th aria-label={t("ediDirection")} />
            <th>{t("ediKind")}</th>
            <th>{t("ediLine")}</th>
            <th>{t("ediRef")}</th>
            <th>{t("ediStatus")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.flatMap((m) => [
            <tr key={m.id} onClick={() => setOpen(open === m.id ? "" : m.id)} style={{ cursor: "pointer" }}>
              <td>{fmt(m.createdAt)}</td>
              <td aria-label={m.direction === "in" ? t("ediDirIn") : t("ediDirOut")}>{m.direction === "in" ? "←" : "→"}</td>
              <td>{t(`ediKind_${m.kind}` as Key)}</td>
              <td>{lineCode(m.lineId)}</td>
              <td className="mono">{(m.direction === "in" ? m.orderRef : m.containerNumber) || m.fileName || m.messageRef || m.controlRef}</td>
              <td>
                <Chip status={m.status} />
              </td>
            </tr>,
            open === m.id && <Detail key={`${m.id}-d`} m={m} lines={lines} depots={depots} done={load} />,
          ])}
        </tbody>
      </table>
      {rows.length === 0 && <p className="muted">{t("ediNone")}</p>}
    </>
  );
}

export function EDI() {
  const { t } = useT();
  const [tab, setTab] = useState<"messages" | "lines">("messages");
  const [info, setInfo] = useState<EDIStatusInfo | null>(null);
  const [lines, setLines] = useState<LineEDI[]>([]);
  const [depots, setDepots] = useState<Depot[]>([]);
  const reload = useCallback(() => {
    ok(getEdiStatus()).then(setInfo, () => undefined);
    ok(listLineEdi()).then(setLines, () => undefined);
  }, []);
  useEffect(() => {
    reload();
    ok(listDepots()).then(setDepots, () => undefined);
  }, [reload]);

  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("ediTitle")}</h1>
      </div>
      {info?.mail === "file" && (
        <p className="warn" role="status">
          {t("ediBannerFile")}
        </p>
      )}
      {info?.inboundAddress && (
        <p className="muted">
          {t("ediInbound")}: <b className="mono">{info.inboundAddress}</b>
        </p>
      )}
      <div className="tabs" role="group">
        <button type="button" className={tab === "messages" ? "active" : ""} onClick={() => setTab("messages")}>
          {t("ediTabMessages")}
        </button>
        <button type="button" className={tab === "lines" ? "active" : ""} onClick={() => setTab("lines")}>
          {t("ediTabLines")}
        </button>
      </div>
      {tab === "messages" ? <Messages lines={lines} depots={depots} /> : <EDILines info={info} lines={lines} reload={reload} />}
    </div>
  );
}
