import { useCreate, useDelete, useList, useUpdate } from "@refinedev/core";
import { useEffect, useState, type FormEvent } from "react";
import { searchLiners, type Liner } from "../gen/api";
import { ok } from "../hubApi";
import { useT, type Key } from "../i18n";

export type ListResource = "lines" | "customers" | "hauliers";

interface Row {
  id: string;
  code: string;
  name: string;
  contact: string;
  active: boolean;
}

const TITLE: Record<ListResource, Key> = { lines: "navLines", customers: "navCustomers", hauliers: "navHauliers" };
const PER_PAGE = 25;
const EMPTY = { code: "", name: "", contact: "" };

// One list page for shipping lines, customers and hauliers: they share the same fields.
export function ListPage({ resource, canEdit }: { resource: ListResource; canEdit: boolean }) {
  const { t } = useT();
  const [q, setQ] = useState("");
  const [page, setPage] = useState(1);
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState(EMPTY);
  const [fresh, setFresh] = useState(EMPTY);
  const [liners, setLiners] = useState<Liner[]>([]);
  const [message, setMessage] = useState("");

  const { result, query } = useList<Row>({
    resource,
    pagination: { currentPage: page, pageSize: PER_PAGE },
    filters: [{ field: "q", operator: "contains", value: q }],
  });
  const create = useCreate();
  const update = useUpdate();
  const remove = useDelete();
  const rows = result.data ?? [];
  const total = result.total ?? 0;

  // Shipping lines: suggest from the SMDG liner list while the code is typed.
  useEffect(() => {
    if (resource !== "lines" || fresh.code.trim().length < 2) {
      setLiners([]);
      return;
    }
    let live = true;
    const timer = window.setTimeout(() => {
      ok(searchLiners({ q: fresh.code.trim() })).then(
        (l) => live && setLiners(l),
        () => live && setLiners([]),
      );
    }, 200);
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [resource, fresh.code]);

  const fail = (e: { message?: string }) => setMessage(e.message ?? t("networkError"));

  // A code that matches a liner fills the name, unless the person already typed one.
  function onCode(code: string) {
    const hit = liners.find((l) => l.code.toLowerCase() === code.trim().toLowerCase());
    setFresh((f) => ({ ...f, code, name: hit && f.name === "" ? hit.name : f.name }));
  }

  function add(e: FormEvent) {
    e.preventDefault();
    setMessage("");
    create.mutate(
      { resource, values: { code: fresh.code.trim(), name: fresh.name.trim(), contact: fresh.contact.trim() } },
      { onSuccess: () => setFresh(EMPTY), onError: fail },
    );
  }

  function save(id: string) {
    setMessage("");
    update.mutate({ resource, id, values: draft }, { onSuccess: () => setEditing(null), onError: fail });
  }

  const setActive = (r: Row, active: boolean) => {
    setMessage("");
    if (active) update.mutate({ resource, id: r.id, values: { active: true } }, { onError: fail });
    else remove.mutate({ resource, id: r.id }, { onError: fail });
  };

  return (
    <div className="page">
      <div className="page-head">
        <h1>
          {t(TITLE[resource])}
          <small>{total}</small>
        </h1>
        <input
          type="search"
          value={q}
          placeholder={t("search")}
          aria-label={t("search")}
          onChange={(e) => {
            setQ(e.target.value);
            setPage(1);
          }}
        />
      </div>
      {canEdit && (
        <form className="card admin-form" onSubmit={add}>
          <label>
            {t("colCode")}
            <input
              value={fresh.code}
              list={resource === "lines" ? "liners" : undefined}
              onChange={(e) => onCode(e.target.value)}
              required
              maxLength={32}
              autoComplete="off"
            />
          </label>
          {resource === "lines" && (
            <datalist id="liners">
              {liners.map((l) => (
                <option key={l.code} value={l.code}>
                  {l.name}
                </option>
              ))}
            </datalist>
          )}
          <label>
            {t("colName")}
            <input value={fresh.name} onChange={(e) => setFresh({ ...fresh, name: e.target.value })} required />
          </label>
          <label>
            {t("colContact")}
            <input value={fresh.contact} onChange={(e) => setFresh({ ...fresh, contact: e.target.value })} />
          </label>
          <button type="submit" className="primary" disabled={create.mutation.isPending}>
            {t("add")}
          </button>
        </form>
      )}
      {(message || query.error) && <p className="error" role="alert">{message || query.error?.message}</p>}
      {rows.length === 0 && !query.isLoading && <p className="muted">{t("noRows")}</p>}
      {rows.length > 0 && (
        <table className="admin-table">
          <thead>
            <tr>
              <th>{t("colCode")}</th>
              <th>{t("colName")}</th>
              <th>{t("colContact")}</th>
              <th>{t("colState")}</th>
              {canEdit && <th />}
            </tr>
          </thead>
          <tbody>
            {rows.map((r) =>
              editing === r.id ? (
                <tr key={r.id}>
                  <td>
                    <input autoFocus aria-label={t("colCode")} value={draft.code} onChange={(e) => setDraft({ ...draft, code: e.target.value })} />
                  </td>
                  <td>
                    <input aria-label={t("colName")} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
                  </td>
                  <td>
                    <input aria-label={t("colContact")} value={draft.contact} onChange={(e) => setDraft({ ...draft, contact: e.target.value })} />
                  </td>
                  <td />
                  <td className="actions">
                    <button type="button" className="primary" onClick={() => save(r.id)}>
                      {t("save")}
                    </button>
                    <button type="button" onClick={() => setEditing(null)}>
                      {t("cancel")}
                    </button>
                  </td>
                </tr>
              ) : (
                <tr key={r.id} className={r.active ? "" : "off"}>
                  <td className="mono">{r.code}</td>
                  <td>{r.name}</td>
                  <td>{r.contact}</td>
                  <td>
                    <span className={`sign ${r.active ? "safe" : "none"}`}>{t(r.active ? "active" : "inactive")}</span>
                  </td>
                  {canEdit && (
                    <td className="actions">
                      <button
                        type="button"
                        onClick={() => {
                          setEditing(r.id);
                          setDraft({ code: r.code, name: r.name, contact: r.contact });
                        }}
                      >
                        {t("edit")}
                      </button>
                      <button type="button" onClick={() => setActive(r, !r.active)}>
                        {t(r.active ? "deactivate" : "reactivate")}
                      </button>
                    </td>
                  )}
                </tr>
              ),
            )}
          </tbody>
        </table>
      )}
      {total > PER_PAGE && (
        <div className="row">
          <button type="button" disabled={page <= 1} onClick={() => setPage(page - 1)}>
            {t("prev")}
          </button>
          <span className="small muted">
            {page} / {Math.ceil(total / PER_PAGE)}
          </span>
          <button type="button" disabled={page * PER_PAGE >= total} onClick={() => setPage(page + 1)}>
            {t("next")}
          </button>
        </div>
      )}
    </div>
  );
}
