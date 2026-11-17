import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { getSession, logout, setScope, type Session, type SessionCurrent } from "./gen/api";
import { forgetForSignOut, unsentCount } from "./gate/outbox";
import { ApiError, onScopeChanged, onUnauthorized, setShownScope } from "./http";
import { ok } from "./hubApi";
import { useT } from "./i18n";
import { readLocal, writeLocal } from "./storage";

// Written after a scope choice; the other tabs of the browser share the session, so they reload it.
const SCOPE_KEY = "scope-changed";
// The last session the server sent: a gate phone reloaded without network keeps working from it.
// The server still checks every call; signing out or a 401 clears it.
const SESSION_KEY = "last-session";
// A sign-out the server has not heard of yet (no network): finished before the session is read again,
// so a reload never brings back the person who signed out. Cleared when it reaches the server or on sign-in.
export const PENDING_LOGOUT = "pending-logout";

// Tells the server about a pending sign-out; false while it cannot be reached.
async function finishLogout(): Promise<boolean> {
  if (!readLocal(PENDING_LOGOUT)) return true;
  try {
    await logout();
  } catch (e) {
    if (!(e instanceof ApiError) || e.status >= 500) return false; // a 401 means it is already gone
  }
  writeLocal(PENDING_LOGOUT, "");
  return true;
}

// The scope this tab shows, sent with every call so a stale tab is refused instead of writing elsewhere.
function shown(session: Session): Session {
  setShownScope(`${session.current?.companyId ?? ""}:${session.current?.depotId ?? ""}`);
  writeLocal(SESSION_KEY, JSON.stringify(session));
  return session;
}

type State = { status: "loading" | "anon" | "down" } | { status: "ready"; session: Session };

interface Ctx {
  state: State;
  reload: () => Promise<void>;
  /** Chooses a company and depot ("" = all depots). Throws when the server refuses. */
  choose: (companyId: string, depotId: string) => Promise<void>;
  signOut: () => Promise<void>;
}

const SessionContext = createContext<Ctx | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const { t } = useT();
  const tRef = useRef(t);
  tRef.current = t;
  const [state, setState] = useState<State>({ status: "loading" });

  const reload = useCallback(async () => {
    if (!(await finishLogout())) {
      setState({ status: "anon" }); // signed out here; the server hears of it on reconnect
      return;
    }
    try {
      setState({ status: "ready", session: shown(await ok(getSession())) });
    } catch (e) {
      const last = e instanceof ApiError ? "" : readLocal(SESSION_KEY); // no answer at all: offline
      if (last) {
        setState({ status: "ready", session: shown(JSON.parse(last) as Session) });
        return;
      }
      // A 401 already flipped the state to "anon" through onUnauthorized.
      setState((s) => (s.status === "anon" ? s : { status: "down" }));
    }
  }, []);

  useEffect(() => {
    onUnauthorized(() => {
      writeLocal(SESSION_KEY, "");
      setState({ status: "anon" });
    });
    onScopeChanged(() => void reload());
    const onStorage = (e: StorageEvent) => {
      if (e.key === SCOPE_KEY) void reload();
    };
    const onOnline = () => {
      if (readLocal(PENDING_LOGOUT)) void finishLogout();
    };
    window.addEventListener("storage", onStorage);
    window.addEventListener("online", onOnline);
    void reload();
    return () => {
      onUnauthorized(undefined);
      onScopeChanged(undefined);
      window.removeEventListener("storage", onStorage);
      window.removeEventListener("online", onOnline);
    };
  }, [reload]);

  const choose = useCallback(async (companyId: string, depotId: string) => {
    setState({ status: "ready", session: shown(await ok(setScope({ companyId, depotId }))) });
    writeLocal(SCOPE_KEY, `${companyId}:${depotId}:${Date.now()}`); // a new value, so the event fires every time
  }, []);

  // A shared gate phone: the cached lists go with the person; unsent gate records stay (they are real trucks),
  // so the person is warned first.
  const signOut = useCallback(async () => {
    const unsent = await unsentCount().catch(() => 0);
    if (unsent > 0 && !window.confirm(tRef.current("gateUnsentSignOut"))) return;
    try {
      await logout();
    } catch (e) {
      if (!(e instanceof ApiError) || e.status >= 500) writeLocal(PENDING_LOGOUT, "1"); // finished on reconnect
    } finally {
      writeLocal(SESSION_KEY, "");
      await forgetForSignOut().catch(() => undefined);
      setState({ status: "anon" });
    }
  }, []);

  const value = useMemo(() => ({ state, reload, choose, signOut }), [state, reload, choose, signOut]);
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

function useCtx(): Ctx {
  const c = useContext(SessionContext);
  if (!c) throw new Error("useSession outside SessionProvider");
  return c;
}

/** The session actions; the state is read through useSession. */
export const useSessionActions = useCtx;

/** What the signed-in person may see: the scope the server resolved, never a guess. */
export function useSession() {
  const { state } = useCtx();
  const session = state.status === "ready" ? state.session : null;
  const current: SessionCurrent | undefined = session?.current;
  return {
    status: state.status,
    session,
    current,
    can: (perm: string) => !!current?.permissions.includes(perm),
    hasModule: (m: string) => !!current?.modules.includes(m),
    platformAdmin: !!session?.platformAdmin,
  };
}

// Switcher is the company and depot choice in the rail (and the picker when nothing is chosen yet).
export function Switcher() {
  const { t } = useT();
  const { session, current } = useSession();
  const { choose, signOut } = useCtx();
  const [error, setError] = useState("");

  if (!session) return null;
  const company = session.companies.find((c) => c.id === current?.companyId);
  // A new company: try all depots first (members who are not limited to some depots may use them);
  // a limited member is refused, so take the first depot the session says they may use.
  const pickCompany = async (id: string) => {
    setError("");
    try {
      await choose(id, "");
    } catch {
      const first = session.companies.find((c) => c.id === id)?.depots[0];
      try {
        if (!first) throw new Error("no depot");
        await choose(id, first.id);
      } catch {
        setError(t("scopeRefused"));
      }
    }
  };
  const pickDepot = async (companyId: string, depotId: string) => {
    setError("");
    try {
      await choose(companyId, depotId);
    } catch {
      setError(t("scopeRefused"));
    }
  };
  return (
    <div className="switcher">
      <div className="who small">{session.user.email}</div>
      {session.companies.length > 0 && (
        <>
          <select aria-label={t("company")} value={current?.companyId ?? ""} onChange={(e) => void pickCompany(e.target.value)}>
            {!current && (
              <option value="" disabled>
                {t("chooseCompany")}
              </option>
            )}
            {session.companies.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
          {current && company && (
            <select
              aria-label={t("depot")}
              value={current.depotId ?? ""}
              onChange={(e) => void pickDepot(current.companyId, e.target.value)}
            >
              <option value="">{t("allDepots")}</option>
              {company.depots.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                </option>
              ))}
            </select>
          )}
        </>
      )}
      {error && (
        <p className="error small" role="alert">
          {error}
        </p>
      )}
      <button type="button" onClick={() => void signOut()}>
        {t("signOut")}
      </button>
    </div>
  );
}
