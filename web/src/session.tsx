import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { getSession, listDepots, logout, setScope, type Depot, type Session, type SessionCurrent } from "./gen/api";
import { onUnauthorized } from "./http";
import { ok } from "./hubApi";
import { useT } from "./i18n";

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
  const [state, setState] = useState<State>({ status: "loading" });

  const reload = useCallback(async () => {
    try {
      setState({ status: "ready", session: await ok(getSession()) });
    } catch {
      // A 401 already flipped the state to "anon" through onUnauthorized.
      setState((s) => (s.status === "anon" ? s : { status: "down" }));
    }
  }, []);

  useEffect(() => {
    onUnauthorized(() => setState({ status: "anon" }));
    void reload();
    return () => onUnauthorized(undefined);
  }, [reload]);

  const choose = useCallback(async (companyId: string, depotId: string) => {
    setState({ status: "ready", session: await ok(setScope({ companyId, depotId })) });
  }, []);

  const signOut = useCallback(async () => {
    try {
      await logout();
    } finally {
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
  const [depots, setDepots] = useState<Depot[]>([]);
  const [error, setError] = useState("");
  const companyId = current?.companyId;

  useEffect(() => {
    if (!companyId) return;
    let live = true;
    ok(listDepots()).then(
      (d) => live && setDepots(d),
      () => live && setDepots([]),
    );
    return () => {
      live = false;
    };
  }, [companyId]);

  if (!session) return null;
  const pick = async (company: string, depot: string) => {
    setError("");
    try {
      await choose(company, depot);
    } catch {
      setError(t("scopeRefused"));
    }
  };
  return (
    <div className="switcher">
      <div className="who small">{session.user.email}</div>
      {session.companies.length > 0 && (
        <>
          <select
            aria-label={t("company")}
            value={companyId ?? ""}
            onChange={(e) => void pick(e.target.value, "")}
          >
            {!companyId && <option value="">{t("chooseCompany")}</option>}
            {session.companies.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
          {companyId && (
            <select
              aria-label={t("depot")}
              value={current?.depotId ?? ""}
              onChange={(e) => void pick(companyId, e.target.value)}
            >
              <option value="">{t("allDepots")}</option>
              {depots.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                </option>
              ))}
            </select>
          )}
        </>
      )}
      {error && <p className="error small">{error}</p>}
      <button type="button" onClick={() => void signOut()}>
        {t("signOut")}
      </button>
    </div>
  );
}
