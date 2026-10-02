// Plays go2rtc's live video with Media Source Extensions over a WebSocket.
// The browser lists the video codecs it can decode, go2rtc answers with the
// MIME type it will send, then it streams fragmented MP4, which is appended
// to a SourceBuffer. Only video codecs are offered, so go2rtc sends no audio.

const VIDEO_CODECS = ["avc1.640029", "avc1.64002A", "avc1.640033", "hvc1.1.6.L153.B0"];
const KEEP_SECONDS = 5; // buffer kept behind the live edge
const MAX_LAG = 1; // seconds behind the live edge before jumping forward

export type LiveState = "connecting" | "playing" | "error";

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

export function playLive(
  video: HTMLVideoElement,
  url: string,
  onState: (s: LiveState, detail?: string) => void,
): LivePlayer {
  const found = mediaSource();
  if (!found) {
    onState("error", "this browser cannot play live video");
    return { stop() {} };
  }
  const { MS, managed } = found;
  const ms = new MS();
  let objectUrl = "";
  if (managed) {
    video.disableRemotePlayback = true;
    video.srcObject = ms;
  } else {
    objectUrl = URL.createObjectURL(ms);
    video.src = objectUrl;
  }
  let ws: WebSocket | null = null;
  let sb: SourceBuffer | null = null;
  const queue: Uint8Array[] = [];
  let stopped = false;
  let playing = false;

  const fail = (detail: string) => {
    if (!stopped) onState("error", detail);
  };

  // pump appends queued data, trims the old buffer and keeps playback at the
  // live edge. It runs on every message and after every SourceBuffer update.
  const pump = () => {
    if (!sb || sb.updating || stopped) return;
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
    const ranges = sb.buffered;
    if (ranges.length === 0) return;
    const start = ranges.start(0);
    const end = ranges.end(ranges.length - 1);
    if (end - start > KEEP_SECONDS + 1) {
      try {
        sb.remove(start, end - KEEP_SECONDS);
      } catch {
        // removal fails while the media source is closing; nothing to do
      }
      return;
    }
    if (end - video.currentTime > MAX_LAG) video.currentTime = Math.max(start, end - 0.3);
    if (video.paused) void video.play().catch(() => undefined);
    if (!playing) {
      playing = true;
      onState("playing");
    }
  };

  ms.addEventListener(
    "sourceopen",
    () => {
      if (stopped) return;
      onState("connecting");
      const socket = new WebSocket(url);
      ws = socket;
      socket.binaryType = "arraybuffer";
      socket.onopen = () => {
        const codecs = VIDEO_CODECS.filter((c) => MS.isTypeSupported(`video/mp4; codecs="${c}"`)).join(",");
        socket.send(JSON.stringify({ type: "mse", value: codecs }));
      };
      socket.onmessage = (ev: MessageEvent) => {
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
            } catch {
              fail(`this browser cannot play ${msg.value}`);
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
      stopped = true;
      ws?.close();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
      video.removeAttribute("src");
      video.srcObject = null;
      video.load();
    },
  };
}
