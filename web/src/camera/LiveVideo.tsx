import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { CameraState } from "../hubApi";
import { useT, type Key } from "../i18n";
import { liveUrl, playLive, type LiveState } from "./mse";

const RECONNECTS = 2; // reconnects after a failure before falling back to stills
const RECONNECT_MS = 2000;
const STILL_MS = 2000; // pause between the end of one still and the next
const AUTO_RETRY_MS = 60_000; // how often still mode tries video again

function subscribeVisibility(onChange: () => void) {
  document.addEventListener("visibilitychange", onChange);
  return () => document.removeEventListener("visibilitychange", onChange);
}

// LiveVideo shows the camera's live video. When video cannot play (go2rtc not
// installed, the stream failing, an old browser), it shows a still refreshed
// every 2 seconds instead, says why, and tries video again every minute.
export function LiveVideo({ videoAvailable }: { videoAvailable: boolean }) {
  const { t } = useT();
  const videoRef = useRef<HTMLVideoElement>(null);
  const failures = useRef(0); // failures in a row; reset once video plays
  const [mode, setMode] = useState<"video" | "still">(videoAvailable ? "video" : "still");
  const [state, setState] = useState<LiveState>("connecting");
  const [detail, setDetail] = useState(""); // the last error
  const [unsupported, setUnsupported] = useState(false); // this browser cannot play video at all
  const [attempt, setAttempt] = useState(0);
  const [tick, setTick] = useState(0); // the still being shown
  const [stillDone, setStillDone] = useState(-1); // the last still that finished loading
  const stillDoneAt = useRef(0);
  const hidden = useSyncExternalStore(subscribeVisibility, () => document.hidden);
  const wasAvailable = useRef(videoAvailable);

  // startVideo switches to video. With RECONNECTS failures already counted,
  // one failed try goes straight back to stills.
  const startVideo = (failuresSoFar: number) => {
    failures.current = failuresSoFar;
    setState("connecting");
    setMode("video");
    setAttempt((a) => a + 1);
  };

  useEffect(() => {
    const video = videoRef.current;
    if (mode !== "video" || !video) return;
    let timer = 0;
    const player = playLive(video, liveUrl(), (s, d) => {
      setState(s);
      if (s === "playing") failures.current = 0;
      if (s === "unsupported") {
        // Reconnecting cannot help; show stills right away.
        setDetail(d ?? "");
        setUnsupported(true);
        setMode("still");
        return;
      }
      if (s !== "error") return; // playLive reports an error at most once
      setDetail(d ?? "");
      failures.current += 1;
      timer = window.setTimeout(() => {
        if (failures.current <= RECONNECTS) {
          setState("connecting");
          setAttempt((a) => a + 1);
        } else setMode("still");
      }, RECONNECT_MS);
    });
    return () => {
      window.clearTimeout(timer);
      player.stop();
    };
  }, [mode, attempt]);

  // Follow the hub: video that becomes available is tried at once, and video
  // that stops being available is not waited for.
  useEffect(() => {
    if (videoAvailable === wasAvailable.current) return;
    wasAvailable.current = videoAvailable;
    if (videoAvailable && mode === "still" && !unsupported) startVideo(0);
    else if (!videoAvailable && mode === "video") {
      setDetail("");
      setMode("still");
    }
  }, [videoAvailable, mode, unsupported]);

  // In still mode after failures, try one video connection every minute.
  useEffect(() => {
    if (mode !== "still" || !videoAvailable || unsupported) return;
    const timer = window.setTimeout(() => startVideo(RECONNECTS), AUTO_RETRY_MS);
    return () => window.clearTimeout(timer);
  }, [mode, videoAvailable, unsupported]);

  // Load the next still STILL_MS after the previous one finished, and none
  // while the page is hidden. Coming back after a while loads one at once.
  useEffect(() => {
    if (mode !== "still" || hidden || stillDone !== tick) return;
    const wait = Math.max(0, STILL_MS - (Date.now() - stillDoneAt.current));
    const timer = window.setTimeout(() => setTick((n) => n + 1), wait);
    return () => window.clearTimeout(timer);
  }, [mode, hidden, stillDone, tick]);

  const stillFinished = () => {
    stillDoneAt.current = Date.now();
    setStillDone(tick);
  };

  let caption: string;
  if (mode === "still") caption = detail ? `${t("camLiveStill")} · ${detail}` : t("camLiveStill");
  else if (state === "playing") caption = t("camLivePlaying");
  else if (state === "blocked") caption = t("camLiveTap");
  else if (state === "error") caption = `${t("error")}: ${detail}`;
  else caption = t("camLiveConnecting");

  return (
    <div className="live">
      {mode === "video" ? (
        <video ref={videoRef} className="live-video" muted playsInline autoPlay />
      ) : (
        <img
          className="live-video"
          src={`/api/camera/still?w=1280&n=${tick}`}
          alt=""
          onLoad={stillFinished}
          onError={stillFinished}
        />
      )}
      <p className="small muted">
        {caption}{" "}
        {mode === "still" && videoAvailable && !unsupported && (
          <button type="button" onClick={() => startVideo(0)}>
            {t("camLiveRetry")}
          </button>
        )}
      </p>
    </div>
  );
}

// CameraOffline stands in for the live view while the hub is not logged in
// to the camera, so the page does not ask the camera for video or stills.
export function CameraOffline({ state }: { state: CameraState }) {
  const { t } = useT();
  return (
    <p className="muted">
      {t("camNotConnected")} · {t(`camState_${state}` as Key)}
    </p>
  );
}
