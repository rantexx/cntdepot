// The calls that stay hand-written: the camera, stills and small helpers around the generated client.
import type { ErrorResponse, TestReadResult } from "./gen/api";
import { http } from "./http";

export { ApiError } from "./http";

/** Unwraps the body of a generated call (the mutator throws on every error status). */
export async function ok<R extends { status: number; data: unknown }>(
  p: Promise<R>,
): Promise<Exclude<Extract<R, { status: 200 | 201 | 202 }>["data"], ErrorResponse>> {
  return (await p).data as never;
}

// Uploads over a bad mobile link can hang for minutes; give up after this long
// so the shot is marked failed and the clerk can retry.
export const UPLOAD_TIMEOUT_MS = 90_000;
export const timeout = (ms: number): RequestInit => ({ signal: AbortSignal.timeout(ms) });

// blobUrl links a stored photo. With a width the hub sends a small JPEG
// preview instead, so phones do not download 4K camera stills.
export type PreviewWidth = 160 | 320 | 640 | 1280;
export const blobUrl = (digest: string, w?: PreviewWidth) => (w ? `/api/blobs/${digest}?w=${w}` : `/api/blobs/${digest}`);
// "Read this frame" stills are not gate photos; the hub serves them from its own route.
export const stillUrl = (digest: string, w?: PreviewWidth) =>
  w ? `/api/camera/stills/${digest}?w=${w}` : `/api/camera/stills/${digest}`;

// typedNumber normalizes a container number typed on a phone keyboard. A Turkish
// keyboard produces İ and ı, which are not in the A-Z alphabet of container
// numbers; both mean I here.
export function typedNumber(s: string): string {
  return s.replace(/[İı]/g, "I").toUpperCase();
}

export interface CameraInfo {
  model: string;
  firmware: string;
  width: number;
  height: number;
  rtsp: boolean;
  detection: string;
}

export type CameraState = "unconfigured" | "connecting" | "online" | "offline" | "auth";

export interface CameraStatus {
  state: CameraState;
  since: string;
  error?: string;
  vehicle: boolean;
  detection: string;
  info: CameraInfo;
  auto: boolean;
  visit_move?: string;
}

export interface LiveStatus {
  state: "off" | "missing" | "running" | "failing";
  reason?: string;
}

export interface CameraOn {
  available: true;
  settings: { host: string; user: string; password_set: boolean; auto: boolean };
  status: CameraStatus;
  live: LiveStatus;
}

/** Outside hub mode the camera calls answer this, e.g. reason "needs_onsite_box". */
export interface CameraOff {
  available: false;
  reason?: string;
}

export type CameraView = CameraOn | CameraOff;

export type CameraStepName = "reach" | "login" | "info" | "ports" | "detect" | "snapshot";

export interface CameraReport {
  steps: { name: CameraStepName; ok: boolean; detail?: string }[] | null;
  info: CameraInfo;
  raw: string;
}

export interface CameraRead {
  still: string;
  width: number;
  height: number;
  result: TestReadResult;
  letter_px: number;
  verdict: "" | "good" | "borderline" | "too_small";
}

export interface CameraUpdate {
  host?: string;
  user?: string;
  password?: string;
  auto?: boolean;
}

const json = (method: string, body?: unknown): RequestInit => ({
  method,
  headers: { "Content-Type": "application/json" },
  body: body === undefined ? undefined : JSON.stringify(body),
});

export const camera = {
  get: async () => (await http<{ data: CameraView }>("/api/camera")).data,
  save: async (u: CameraUpdate) => (await http<{ data: CameraView }>("/api/camera", json("PUT", u))).data,
  test: async () => (await http<{ data: CameraReport }>("/api/camera/test", { method: "POST", ...timeout(45_000) })).data,
  read: async () => (await http<{ data: CameraRead }>("/api/camera/read", { method: "POST", ...timeout(UPLOAD_TIMEOUT_MS) })).data,
  capture: async () => (await http<{ data: { move_id: string } }>("/api/camera/capture", { method: "POST" })).data,
};
