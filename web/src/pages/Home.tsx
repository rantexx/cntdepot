import { useT } from "../i18n";

export function Home() {
  const { t } = useT();
  return (
    <div className="tiles">
      <a className="tile" href="#/capture">
        <b>{t("navCapture")}</b>
        <span>{t("homeCaptureHint")}</span>
      </a>
      <a className="tile" href="#/queue">
        <b>{t("navQueue")}</b>
        <span>{t("homeQueueHint")}</span>
      </a>
      <a className="tile" href="#/test">
        <b>{t("navTest")}</b>
        <span>{t("homeTestHint")}</span>
      </a>
    </div>
  );
}
