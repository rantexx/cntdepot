import { useCreate, useDelete, useList, useUpdate } from "@refinedev/core";
import { useEffect, useState, type FormEvent } from "react";
import { listDepots, Role, type Depot, type Invitation, type Member } from "../gen/api";
import { ok } from "../hubApi";
import { useT } from "../i18n";
import { useSession } from "../session";

const ROLES = Object.values(Role);

// DepotAccess chooses all depots (null) or a non-empty set of them.
function DepotAccess({
  depots,
  value,
  onChange,
}: {
  depots: Depot[];
  value: string[] | null;
  onChange: (v: string[] | null) => void;
}) {
  const { t } = useT();
  return (
    <fieldset className="depot-access">
      <legend className="sr-only">{t("depots")}</legend>
      <label className="check">
        <input type="checkbox" checked={value === null} onChange={(e) => onChange(e.target.checked ? null : depots.slice(0, 1).map((d) => d.id))} />
        {t("allDepots")}
      </label>
      {value !== null &&
        depots.map((d) => (
          <label key={d.id} className="check">
            <input
              type="checkbox"
              checked={value.includes(d.id)}
              onChange={(e) => onChange(e.target.checked ? [...value, d.id] : value.filter((x) => x !== d.id))}
            />
            {d.name}
          </label>
        ))}
    </fieldset>
  );
}

function depotLabel(ids: string[] | null, depots: Depot[], all: string): string {
  if (ids === null) return all;
  return ids.map((id) => depots.find((d) => d.id === id)?.name ?? id).join(", ");
}

export function Members() {
  const { t } = useT();
  const { session } = useSession();
  const [depots, setDepots] = useState<Depot[]>([]);
  const [message, setMessage] = useState("");
  const [editing, setEditing] = useState<{ id: string; role: Role; depotIds: string[] | null } | null>(null);
  const [invite, setInvite] = useState<{ email: string; role: Role; depotIds: string[] | null }>({
    email: "",
    role: Role.gate,
    depotIds: null,
  });

  useEffect(() => {
    ok(listDepots()).then(setDepots, () => setDepots([]));
  }, []);

  const members = useList<Member & { id: string }>({ resource: "members", pagination: { mode: "off" } });
  const invitations = useList<Invitation>({ resource: "invitations", pagination: { mode: "off" } });
  const update = useUpdate();
  const remove = useDelete();
  const create = useCreate();
  const fail = (e: { message?: string }) => setMessage(e.message ?? t("networkError"));

  function sendInvite(e: FormEvent) {
    e.preventDefault();
    setMessage("");
    create.mutate(
      { resource: "invitations", values: { email: invite.email.trim(), role: invite.role, depotIds: invite.depotIds } },
      { onSuccess: () => setInvite({ ...invite, email: "" }), onError: fail },
    );
  }

  const inviteReady = invite.email.includes("@") && (invite.depotIds === null || invite.depotIds.length > 0);
  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("navMembers")}</h1>
      </div>
      {message && <p className="error" role="alert">{message}</p>}
      <form className="card admin-form" onSubmit={sendInvite}>
        <label>
          {t("email")}
          <input type="email" value={invite.email} onChange={(e) => setInvite({ ...invite, email: e.target.value })} required />
        </label>
        <label>
          {t("role")}
          <select value={invite.role} onChange={(e) => setInvite({ ...invite, role: e.target.value as Role })}>
            {ROLES.map((r) => (
              <option key={r} value={r}>
                {t(`role_${r}`)}
              </option>
            ))}
          </select>
        </label>
        <DepotAccess depots={depots} value={invite.depotIds} onChange={(v) => setInvite({ ...invite, depotIds: v })} />
        <button type="submit" className="primary" disabled={!inviteReady}>
          {t("invite")}
        </button>
      </form>

      <h3>{t("navMembers")}</h3>
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t("email")}</th>
            <th>{t("role")}</th>
            <th>{t("depots")}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {members.result.data.map((m) =>
            editing?.id === m.id ? (
              <tr key={m.id}>
                <td>{m.email}</td>
                <td>
                  <select autoFocus aria-label={t("role")} value={editing.role} onChange={(e) => setEditing({ ...editing, role: e.target.value as Role })}>
                    {ROLES.map((r) => (
                      <option key={r} value={r}>
                        {t(`role_${r}`)}
                      </option>
                    ))}
                  </select>
                </td>
                <td>
                  <DepotAccess depots={depots} value={editing.depotIds} onChange={(v) => setEditing({ ...editing, depotIds: v })} />
                </td>
                <td className="actions">
                  <button
                    type="button"
                    className="primary"
                    disabled={editing.depotIds !== null && editing.depotIds.length === 0}
                    onClick={() =>
                      update.mutate(
                        { resource: "members", id: m.id, values: { role: editing.role, depotIds: editing.depotIds } },
                        { onSuccess: () => setEditing(null), onError: fail },
                      )
                    }
                  >
                    {t("save")}
                  </button>
                  <button type="button" onClick={() => setEditing(null)}>
                    {t("cancel")}
                  </button>
                </td>
              </tr>
            ) : (
              <tr key={m.id}>
                <td>{m.email}</td>
                <td>{t(`role_${m.role}`)}</td>
                <td>{depotLabel(m.depotIds, depots, t("allDepots"))}</td>
                <td className="actions">
                  <button type="button" onClick={() => setEditing({ id: m.id, role: m.role, depotIds: m.depotIds })}>
                    {t("edit")}
                  </button>
                  {m.userId !== session?.user.id && (
                    <button
                      type="button"
                      className="danger"
                      onClick={() => window.confirm(t("confirmRemove")) && remove.mutate({ resource: "members", id: m.id }, { onError: fail })}
                    >
                      {t("remove")}
                    </button>
                  )}
                </td>
              </tr>
            ),
          )}
        </tbody>
      </table>

      <h3>{t("invitations")}</h3>
      {invitations.result.data.length === 0 && <p className="muted">{t("noRows")}</p>}
      {invitations.result.data.length > 0 && (
        <table className="admin-table">
          <tbody>
            {invitations.result.data.map((i) => (
              <tr key={i.id}>
                <td>{i.email}</td>
                <td>{t(`role_${i.role}`)}</td>
                <td>{depotLabel(i.depotIds, depots, t("allDepots"))}</td>
                <td className="actions">
                  <button type="button" onClick={() => remove.mutate({ resource: "invitations", id: i.id }, { onError: fail })}>
                    {t("cancelInvite")}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
