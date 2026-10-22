import { useCallback, useEffect, useState, type ReactNode } from "react";
import { api, ApiError, type CameraState } from "./api";
import { useT, type Key } from "./i18n";
import { readLocal, writeLocal } from "./storage";
import { useMoveEvents } from "./live";
import { Camera } from "./pages/Camera";
import { Capture } from "./pages/Capture";
import { Home } from "./pages/Home";
import { Login } from "./pages/Login";
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
function useGateStatus(onAuth: () => void) {
  const [waiting, setWaiting] = useState(0);
  const [camera, setCamera] = useState<CameraState | null>(null);
  const [demo, setDemo] = useState(false);
  const [engineUp, setEngineUp] = useState<boolean | null>(null);
  const load = useCallback(async () => {
    try {
      setWaiting((await api.listMoves("needs_review")).moves.length);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) onAuth();
    }
    api.camera().then(
      (v) => {
        setCamera(v.available ? v.status.state : null);
        // shortcut: the simulated camera is recognised by its model name, until the hub reports demo mode itself.
        setDemo(v.status.info.model === "Fake camera");
      },
      () => setCamera(null),
    );
    api.health().then((h) => setEngineUp(h.ok), () => setEngineUp(false));
  }, [onAuth]);
  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 5000);
    return () => window.clearInterval(timer);
  }, [load]);
  useMoveEvents(
    () => void load(),
    () => void load(),
  );
  return { waiting, camera, demo, engineUp };
}

const NAV: { route: string; href: string; label: Key }[] = [
  { route: "", href: "#/", label: "navLiveGate" },
  { route: "queue", href: "#/queue", label: "navQueue" },
  { route: "camera", href: "#/camera", label: "navCamera" },
  { route: "capture", href: "#/capture", label: "navCapture" },
  { route: "test", href: "#/test", label: "navTest" },
];

export function App() {
  const hash = useHash();
  const { t, lang, setLang } = useT();
  const [needLogin, setNeedLogin] = useState(false);
  const onAuth = useCallback(() => setNeedLogin(true), []);
  const { waiting, camera, demo, engineUp } = useGateStatus(onAuth);
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

  const [, route = "", id = ""] = hash.split("/");
  let page: ReactNode;
  if (needLogin) page = <Login onDone={() => setNeedLogin(false)} />;
  else if (route === "capture") page = <Capture />;
  else if (route === "queue") page = <Queue />;
  else if (route === "moves") page = <MoveDetail key={id} id={id} />;
  else if (route === "test") page = <TestRead />;
  else if (route === "camera") page = <Camera />;
  else page = <Home />;
  // A truck record belongs to the review queue in the rail.
  const current = route === "moves" ? "queue" : route;

  return (
    <div className="shell">
      <aside className="rail">
        <a href="#/" className="brand">
          <span className="brand-mark" aria-hidden="true" />
          {t("appTitle")}
        </a>
        <nav>
          {NAV.map((n) => (
            <a key={n.route} href={n.href} aria-current={current === n.route ? "page" : undefined}>
              {t(n.label)}
              {n.route === "queue" && waiting > 0 && <span className="count">{waiting}</span>}
            </a>
          ))}
        </nav>
        <div className="rail-foot">
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
      <main className="work">{page}</main>
    </div>
  );
}
