import { useEffect, useState, type ReactNode } from "react";
import { api, ApiError } from "./api";
import { useT } from "./i18n";
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

export function App() {
  const hash = useHash();
  const { t, lang, setLang } = useT();
  const [needLogin, setNeedLogin] = useState(false);

  useEffect(() => {
    api.listMoves("needs_review").catch((e: unknown) => {
      if (e instanceof ApiError && e.status === 401) setNeedLogin(true);
    });
  }, []);

  const [, route = "", id = ""] = hash.split("/");
  let page: ReactNode;
  if (needLogin) page = <Login onDone={() => setNeedLogin(false)} />;
  else if (route === "capture") page = <Capture />;
  else if (route === "queue") page = <Queue />;
  else if (route === "moves") page = <MoveDetail key={id} id={id} />;
  else if (route === "test") page = <TestRead />;
  else if (route === "camera") page = <Camera />;
  else page = <Home />;

  return (
    <div>
      <header>
        <a href="#/" className="brand">
          {t("appTitle")}
        </a>
        <nav>
          <a href="#/capture">{t("navCapture")}</a>
          <a href="#/queue">{t("navQueue")}</a>
          <a href="#/test">{t("navTest")}</a>
          <a href="#/camera">{t("navCamera")}</a>
        </nav>
        <button type="button" onClick={() => setLang(lang === "tr" ? "en" : "tr")}>
          {lang === "tr" ? "EN" : "TR"}
        </button>
      </header>
      <main>{page}</main>
    </div>
  );
}
