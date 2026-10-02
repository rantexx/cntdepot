import { useEffect, useRef, useState } from "react";
import { useT } from "../i18n";
import { liveUrl, playLive, type LiveState } from "./mse";

// LiveVideo shows the camera's live video. When video cannot play (go2rtc not
// installed, the stream failing, an old browser), it shows a still refreshed
// every 2 seconds instead.
export function LiveVideo({ videoAvailable }: { videoAvailable: boolean }) {
  const { t } = useT();
  const videoRef = useRef<HTMLVideoElement>(null);
  const failures = useRef(0); // failures in a row; reset once video plays
  const [mode, setMode] = useState<"video" | "still">(videoAvailable ? "video" : "still");
  const [state, setState] = useState<LiveState>("connecting");
  const [detail, setDetail] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    const video = videoRef.current;
    if (mode !== "video" || !video) return;
    let failed = false;
    let timer = 0;
    const player = playLive(video, liveUrl(), (s, d) => {
      setState(s);
      if (s === "playing") failures.current = 0;
      if (s !== "error" || failed) return;
      failed = true;
      setDetail(d ?? "");
      failures.current += 1;
      // Reconnect twice, then fall back to stills.
      timer = window.setTimeout(() => {
        if (failures.current <= 2) setAttempt((a) => a + 1);
        else setMode("still");
      }, 2000);
    });
    return () => {
      window.clearTimeout(timer);
      player.stop();
    };
  }, [mode, attempt]);

  useEffect(() => {
    if (mode !== "still") return;
    const timer = window.setInterval(() => setTick((n) => n + 1), 2000);
    return () => window.clearInterval(timer);
  }, [mode]);

  let caption: string;
  if (mode === "still") caption = t("camLiveStill");
  else if (state === "playing") caption = t("camLivePlaying");
  else if (state === "connecting") caption = t("camLiveConnecting");
  else caption = `${t("error")}: ${detail}`;

  return (
    <div className="live">
      {mode === "video" ? (
        <video ref={videoRef} className="live-video" muted playsInline autoPlay />
      ) : (
        <img className="live-video" src={`/api/camera/still?w=1280&n=${tick}`} alt="" />
      )}
      <p className="small muted">
        {caption}{" "}
        {mode === "still" && videoAvailable && (
          <button
            type="button"
            onClick={() => {
              failures.current = 0;
              setAttempt((a) => a + 1);
              setMode("video");
            }}
          >
            {t("camLiveRetry")}
          </button>
        )}
      </p>
    </div>
  );
}
