// Plays go2rtc's live video with Media Source Extensions over a WebSocket.
// The browser lists the video codecs it can decode, go2rtc answers with the
// MIME type it will send, then it streams fragmented MP4, which is appended
// to a SourceBuffer. Only video codecs are offered, so go2rtc sends no audio.

const VIDEO_CODECS = ["avc1.640029", "avc1.64002A", "avc1.640033", "hvc1.1.6.L153.B0"];
const KEEP_SECONDS = 5; // buffer kept behind the live edge
const CATCH_UP_LAG = 0.5; // seconds behind the live edge before playing faster
const CATCH_UP_RATE = 1.1;
const JUMP_LAG = 2; // seconds behind the live edge before jumping forward
const JUMP_TARGET = 0.5; // a jump lands this far behind the live edge
const STALL_MS = 10_000; // no new frame for this long counts as a failure

// "blocked": the browser refused to start playback on its own (autoplay
// blocked), so the video shows its controls and waits for a tap.
// "unsupported": this browser cannot play the stream at all; reconnecting
// will not help. "error" is reported at most once per player and ends it.
export type LiveState = "connecting" | "playing" | "blocked" | "error" | "unsupported";

export interface LivePlayer {
  stop(): void;
}

export function liveUrl(): string {
  const scheme = window.location.protocol === "https:" ? "wss" : "ws";
  return `${scheme}://${window.location.host}/api/live/ws`;
}

// mediaSource prefers ManagedMediaSource, the only kind Safari on iPhone
// supports (iOS 17.1+).
function mediaSource(): { MS: typeof MediaSource; managed: boolean } | null {
  const w = window as unknown as { ManagedMediaSource?: typeof MediaSource };
  if (w.ManagedMediaSource) return { MS: w.ManagedMediaSource, managed: true };
  if (typeof window.MediaSource !== "undefined") return { MS: window.MediaSource, managed: false };
  return null;
}

const MEDIA_ERRORS: Record<number, string> = { 1: "aborted", 2: "network", 3: "decode", 4: "source not supported" };

export function playLive(
  video: HTMLVideoElement,
  url: string,
  onState: (s: LiveState, detail?: string) => void,
): LivePlayer {
  const noop: LivePlayer = { stop() {} };
  const found = mediaSource();
  if (!found) {
    onState("unsupported", "unsupported: this browser has no Media Source Extensions");
    return noop;
  }
  const { MS, managed } = found;
  const codecs = VIDEO_CODECS.filter((c) => MS.isTypeSupported(`video/mp4; codecs="${c}"`)).join(",");
  if (!codecs) {
    onState("unsupported", "unsupported: this browser cannot decode H.264 or H.265 video");
    return noop;
  }

  let ms: MediaSource;
  let objectUrl = "";
  try {
    ms = new MS();
    if (managed) {
      video.disableRemotePlayback = true;
      video.srcObject = ms;
    } else {
      objectUrl = URL.createObjectURL(ms);
      video.src = objectUrl;
    }
  } catch (e) {
    if (objectUrl) URL.revokeObjectURL(objectUrl);
    onState("error", `cannot start live video: ${String(e)}`);
    return noop;
  }

  let ws: WebSocket | null = null;
  let sb: SourceBuffer | null = null;
  const queue: Uint8Array[] = [];
  let done = false; // failed or stopped: report nothing more
  let reported: LiveState | "" = "";
  let starting = false; // a play() call is pending
  let lastTime = video.currentTime; // playback position at the last sign of progress
  let lastProgress = Date.now();

  const report = (s: LiveState) => {
    if (done || reported === s) return;
    reported = s;
    onState(s);
  };

  const removeListeners = () => {
    video.removeEventListener("error", onVideoError);
    video.removeEventListener("playing", onPlaying);
    video.removeEventListener("timeupdate", onTimeUpdate);
    video.removeEventListener("play", onPlay);
  };

  const fail = (detail: string) => {
    if (done) return;
    done = true;
    window.clearInterval(watchdog);
    removeListeners();
    ws?.close();
    onState("error", detail);
  };

  // waitingForTap: playback was blocked and the person has not pressed play.
  const waitingForTap = () => video.controls && video.paused;

  // progressed notes whether playback moved past the last known position.
  // Jumps to the live edge move that position too, so they do not count.
  const progressed = () => {
    if (video.seeking || video.currentTime <= lastTime) return false;
    lastTime = video.currentTime;
    lastProgress = Date.now();
    return true;
  };

  const jump = (to: number) => {
    video.currentTime = to;
    lastTime = Math.max(lastTime, to);
  };

  // The watchdog fails the player when no new frame has played for
  // STALL_MS, whether it is still connecting or was playing. A hidden page
  // and a video waiting for a tap are not stalls.
  const watchdog = window.setInterval(() => {
    if (done) return;
    if (document.hidden || waitingForTap() || progressed()) {
      lastProgress = Date.now();
      return;
    }
    if (Date.now() - lastProgress > STALL_MS) fail(`no video for ${STALL_MS / 1000} s`);
  }, 1000);

  function onVideoError() {
    const err = video.error;
    const kind = err ? (MEDIA_ERRORS[err.code] ?? `code ${err.code}`) : "unknown";
    fail(`video error (${kind})${err?.message ? `: ${err.message}` : ""}`);
  }
  function onPlaying() {
    report("playing");
  }
  function onTimeUpdate() {
    if (progressed() && !video.paused) report("playing");
  }
  function onPlay() {
    // the person pressed play after autoplay was blocked
    if (reported === "blocked") report("connecting");
  }
  video.addEventListener("error", onVideoError);
  video.addEventListener("playing", onPlaying);
  video.addEventListener("timeupdate", onTimeUpdate);
  video.addEventListener("play", onPlay);

  const startPlayback = () => {
    if (!video.paused || starting || video.controls) return;
    starting = true;
    video.play().then(
      () => {
        starting = false;
      },
      (e: unknown) => {
        starting = false;
        if (done) return;
        const name = e instanceof DOMException ? e.name : "";
        if (name === "AbortError") return; // interrupted by a new load
        if (name === "NotAllowedError") {
          // Autoplay blocked (for example iPhone low power mode): let the
          // person start it with the video's own play button.
          video.controls = true;
          report("blocked");
          return;
        }
        fail(`cannot play live video: ${String(e)}`);
      },
    );
  };

  // pump appends queued data, trims the old buffer and keeps playback at the
  // live edge. It runs on every message and after every SourceBuffer update.
  const pump = () => {
    if (!sb || sb.updating || done) return;
    if (queue.length > 0) {
      const size = queue.reduce((n, b) => n + b.byteLength, 0);
      const data = new Uint8Array(size);
      let offset = 0;
      for (const b of queue) {
        data.set(b, offset);
        offset += b.byteLength;
      }
      queue.length = 0;
      try {
        sb.appendBuffer(data);
      } catch (e) {
        fail(String(e));
      }
      return;
    }
    let start: number;
    let end: number;
    try {
      const ranges = sb.buffered;
      if (ranges.length === 0) return;
      start = ranges.start(0);
      end = ranges.end(ranges.length - 1);
    } catch (e) {
      // the SourceBuffer was removed from a closing MediaSource
      fail(String(e));
      return;
    }
    if (end - start > KEEP_SECONDS + 1) {
      try {
        sb.remove(start, end - KEEP_SECONDS);
      } catch {
        // removal fails while the media source is closing; nothing to do
      }
      return;
    }
    // Like go2rtc's player: play slightly faster to catch up a small lag, and
    // jump only when far behind or before the start of the buffer.
    const lag = end - video.currentTime;
    if (!video.seeking && (video.currentTime < start || lag > JUMP_LAG)) {
      jump(Math.max(start, end - JUMP_TARGET));
    }
    const rate = lag > CATCH_UP_LAG ? CATCH_UP_RATE : 1;
    if (video.playbackRate !== rate) video.playbackRate = rate;
    startPlayback();
  };

  ms.addEventListener(
    "sourceopen",
    () => {
      if (done) return;
      report("connecting");
      let socket: WebSocket;
      try {
        socket = new WebSocket(url);
      } catch (e) {
        fail(`cannot connect: ${String(e)}`);
        return;
      }
      ws = socket;
      socket.binaryType = "arraybuffer";
      socket.onopen = () => {
        socket.send(JSON.stringify({ type: "mse", value: codecs }));
      };
      socket.onmessage = (ev: MessageEvent) => {
        if (done) return;
        if (typeof ev.data === "string") {
          let msg: { type?: string; value?: string };
          try {
            msg = JSON.parse(ev.data) as { type?: string; value?: string };
          } catch {
            return;
          }
          if (msg.type === "mse" && msg.value && !sb) {
            try {
              sb = ms.addSourceBuffer(msg.value);
              sb.mode = "segments";
              sb.addEventListener("updateend", pump);
            } catch (e) {
              fail(`this browser cannot play ${msg.value}: ${String(e)}`);
            }
          } else if (msg.type === "error") {
            fail(msg.value ?? "live video error");
          }
          return;
        }
        queue.push(new Uint8Array(ev.data as ArrayBuffer));
        pump();
      };
      socket.onerror = () => fail("connection failed");
      socket.onclose = () => fail("connection closed");
    },
    { once: true },
  );

  return {
    stop() {
      done = true;
      window.clearInterval(watchdog);
      removeListeners();
      ws?.close();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
      video.controls = false;
      video.playbackRate = 1;
      video.removeAttribute("src");
      video.srcObject = null;
      video.load();
    },
  };
}
