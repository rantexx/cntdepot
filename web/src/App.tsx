import { Refine } from "@refinedev/core";
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Companies } from "./admin/Companies";
import { Company } from "./admin/Company";
import { dataProvider } from "./admin/dataProvider";
import { ListPage } from "./admin/ListPage";
import { Members } from "./admin/Members";
import { getHealth, listMoves } from "./gen/api";
import { camera as cameraApi, ok, type CameraState } from "./hubApi";
import { useT, type Key } from "./i18n";
import { readLocal, writeLocal } from "./storage";
import { useMoveEvents } from "./live";
import { SessionProvider, Switcher, useSession } from "./session";
import { Camera } from "./pages/Camera";
import { Capture } from "./pages/Capture";
import { Home } from "./pages/Home";
import { Login, NoMembership } from "./pages/Login";
import { MoveDetail } from "./pages/MoveDetail";
import { Queue } from "./pages/Queue";
import { TestRead } from "./pages/TestRead";

function useHash(): string {
  const [hash, setHash] = useState(() => window.location.hash || "#/");
  useEffect(() => {
    const onChange = () => setHash(window.location.hash || "#/");
    window.addEventListener("hashchange", onChange);
    return () => window.removeEventListener("hashchange", onChange);
  }, []);
  return hash;
}

// useGateStatus keeps the rail's review count, camera state and engine state fresh.
// The review count needs the review queue and the camera state needs a manager;
// a clerk's rail asks for neither.
function useGateStatus(queue: boolean, manage: boolean) {
  const [waiting, setWaiting] = useState(0);
  const [camera, setCamera] = useState<CameraState | null>(null);
  const [demo, setDemo] = useState(false);
  const [engineUp, setEngineUp] = useState<boolean | null>(null);
  const load = useCallback(() => {
    if (queue) {
      ok(listMoves({ status: "needs_review", limit: 100 })).then(
        (r) => setWaiting(r.moves.length),
        () => undefined,
      );
    }
    if (manage) {
      cameraApi.get().then(
        (v) => {
          setCamera(v.available ? v.status.state : null);
          // shortcut: the simulated camera is recognised by its model name, until the hub reports demo mode itself.
          setDemo(v.available && v.status.info.model === "Fake camera");
        },
        () => setCamera(null),
      );
    }
    ok(getHealth()).then(
      (h) => setEngineUp(h.ok),
      () => setEngineUp(false),
    );
  }, [queue, manage]);
  useEffect(() => {
    load();
    const timer = window.setInterval(load, 5000);
    return () => window.clearInterval(timer);
  }, [load]);
  useMoveEvents(load, load);
  return { waiting, camera, demo, engineUp };
}

interface NavItem {
  route: string;
  href: string;
  label: Key;
  perm: string; // the lowest permission that may open it
  ocr?: boolean; // only with the ocr module on
}

const NAV: NavItem[] = [
  { route: "", href: "#/", label: "navLiveGate", perm: "gate", ocr: true },
  { route: "queue", href: "#/queue", label: "navQueue", perm: "office", ocr: true },
  { route: "camera", href: "#/camera", label: "navCamera", perm: "manage" },
  { route: "capture", href: "#/capture", label: "navCapture", perm: "gate" },
  { route: "test", href: "#/test", label: "navTest", perm: "office", ocr: true },
];

const ADMIN: NavItem[] = [
  { route: "admin/lines", href: "#/admin/lines", label: "navLines", perm: "office" },
  { route: "admin/customers", href: "#/admin/customers", label: "navCustomers", perm: "office" },
  { route: "admin/hauliers", href: "#/admin/hauliers", label: "navHauliers", perm: "office" },
  { route: "admin/members", href: "#/admin/members", label: "navMembers", perm: "owner" },
  { route: "admin/company", href: "#/admin/company", label: "navCompany", perm: "owner" },
];

export function App() {
  return (
    <SessionProvider>
      <Gate />
    </SessionProvider>
  );
}

// Gate decides what the person sees before the shell: sign in, a message when
// nobody has added them yet, or the shell (with a company picker when none is chosen).
function Gate() {
  const { t } = useT();
  const { status, session, current } = useSession();
  if (status === "loading") return <p className="boot">{t("loading")}</p>;
  if (status === "down") return <p className="boot error">{t("networkError")}</p>;
  if (status === "anon" || !session) return <Login />;
  if (!current && session.companies.length === 0 && !session.platformAdmin) return <NoMembership />;
  return <Shell key={`${current?.companyId ?? ""}:${current?.depotId ?? ""}`} />;
}

function Shell() {
  const hash = useHash();
  const { t, lang, setLang } = useT();
  const { current, can, hasModule, platformAdmin } = useSession();
  const ocr = hasModule("ocr");
  const visible = (n: NavItem) => can(n.perm) && (!n.ocr || ocr);
  const { waiting, camera, demo, engineUp } = useGateStatus(!!current && ocr && can("office"), can("manage"));
  // Day or night is the person's choice on a shared depot PC; unset follows the computer.
  const [theme, setTheme] = useState(() => readLocal("theme") ?? "");
  useEffect(() => {
    if (theme) document.documentElement.dataset.theme = theme;
    else delete document.documentElement.dataset.theme;
  }, [theme]);
  const night = theme ? theme === "night" : window.matchMedia("(prefers-color-scheme: dark)").matches;
  const pickTheme = (t: "day" | "night") => {
    writeLocal("theme", t);
    setTheme(t);
  };

  const [, first = "", second = ""] = hash.split("/");
  const route = first === "admin" || first === "platform" ? `${first}/${second}` : first;
  const id = second;
  const open = (r: string) => NAV.concat(ADMIN).some((n) => n.route === r && visible(n));
  let page: ReactNode;
  if (route === "platform/companies" && platformAdmin) page = <Companies />;
  else if (!current) page = <Picker />; // no company chosen yet
  else if (route === "admin/lines" && open(route)) page = <ListPage resource="lines" canEdit={can("office")} />;
  else if (route === "admin/customers" && open(route)) page = <ListPage resource="customers" canEdit={can("office")} />;
  else if (route === "admin/hauliers" && open(route)) page = <ListPage resource="hauliers" canEdit={can("office")} />;
  else if (route === "admin/members" && open(route)) page = <Members />;
  else if (route === "admin/company" && open(route)) page = <Company />;
  else if (route === "capture") page = <Capture />;
  else if (route === "queue" && open(route)) page = <Queue />;
  else if (route === "moves" && can("gate")) page = <MoveDetail key={id} id={id} />;
  else if (route === "test" && open(route)) page = <TestRead />;
  else if (route === "camera" && open(route)) page = <Camera />;
  else if (route === "" && ocr) page = <Home />;
  else page = <Capture />; // without OCR the gate's home is phone capture
  // A truck record belongs to the review queue in the rail.
  const active = route === "moves" ? "queue" : route === "" && !ocr ? "capture" : route;
  const link = (n: NavItem) => (
    <a key={n.route} href={n.href} aria-current={active === n.route ? "page" : undefined}>
      {t(n.label)}
      {n.route === "queue" && waiting > 0 && <span className="count">{waiting}</span>}
    </a>
  );
  const admin = current ? ADMIN.filter(visible) : [];
  const adminRoute = route.startsWith("admin/") || route.startsWith("platform/");

  return (
    <div className="shell">
      <aside className="rail">
        <a href="#/" className="brand">
          <span className="brand-mark" aria-hidden="true" />
          {t("appTitle")}
        </a>
        <nav>
          {current && NAV.filter(visible).map(link)}
          {(admin.length > 0 || platformAdmin) && <span className="nav-group">{t("navAdmin")}</span>}
          {admin.map(link)}
          {platformAdmin && (
            <a href="#/platform/companies" aria-current={route === "platform/companies" ? "page" : undefined}>
              {t("navCompanies")}
            </a>
          )}
        </nav>
        <div className="rail-foot">
          {current && <Switcher />}
          <div className="lang" role="group" aria-label="Language">
            <button type="button" aria-pressed={lang === "en"} onClick={() => setLang("en")}>
              EN
            </button>
            <button type="button" aria-pressed={lang === "tr"} onClick={() => setLang("tr")}>
              TR
            </button>
          </div>
          <div className="lang" role="group" aria-label="Theme">
            <button type="button" aria-pressed={!night} onClick={() => pickTheme("day")}>
              {t("themeDay")}
            </button>
            <button type="button" aria-pressed={night} onClick={() => pickTheme("night")}>
              {t("themeNight")}
            </button>
          </div>
          {camera && (
            <div className="stat">
              <i className={camera === "online" && !demo ? "on" : camera === "online" || camera === "unconfigured" ? "" : "bad"} />
              {t("navCamera")}: {camera === "online" && demo ? t("cameraDemo") : t(`camState_${camera}` as Key)}
            </div>
          )}
          {engineUp !== null && (
            <div className="stat">
              <i className={engineUp ? "on" : "bad"} />
              {engineUp ? t("engineOnline") : t("engineDown")}
            </div>
          )}
        </div>
      </aside>
      <main className="work">
        {adminRoute ? (
          <Refine dataProvider={dataProvider} options={{ disableTelemetry: true }}>
            {page}
          </Refine>
        ) : (
          page
        )}
      </main>
    </div>
  );
}

// Picker: nothing chosen yet. It is the same control as the one in the rail.
function Picker() {
  const { t } = useT();
  return (
    <section className="card narrow">
      <h2>{t("chooseCompany")}</h2>
      <Switcher />
    </section>
  );
}
