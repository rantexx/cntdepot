// Typed client for the hub API. Field names match the Go JSON tags.

export type Status = "capturing" | "reading" | "needs_review" | "accepted" | "refused";

export interface Photo {
  id: string;
  move_id: string;
  role: string;
  sha256: string;
  ai_sha256: string;
  media_type: string;
  ai_media_type: string;
  bytes: number;
  width: number;
  height: number;
  captured_at: string;
}

export interface Candidate {
  number: string;
  confidence: number;
  check_digit_valid: boolean;
  repaired_from?: string;
}

export interface Frame {
  role: string;
  candidates: Candidate[] | null;
  type_code: string;
  color: string;
  visible: string;
  notes: string;
  error?: string;
}

export interface Result {
  number: string;
  confidence: number;
  check_digit_valid: boolean;
  decision: string;
  reason: string;
  type_code: string;
  color: string;
  candidates: Candidate[] | null;
  frames: Frame[] | null;
}

export interface Move {
  id: string;
  lane: string;
  direction: string;
  status: Status;
  number: string;
  type_code: string;
  color: string;
  confidence: number;
  check_digit_valid: boolean;
  decision: string;
  reason: string;
  result: Partial<Result>;
  verified: boolean;
  corrected_from: string;
  resolved_by: string;
  note: string;
  is_test: boolean;
  created_at: string;
  updated_at: string;
  photos: Photo[] | null;
}

export interface Box {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
}

export interface Reading {
  candidates: { number: string; confidence: number }[] | null;
  type_code: string;
  color: string;
  visible: string;
  box: Box | null;
  notes: string;
}

export interface Attempt {
  id: string;
  move_id: string;
  photo_id: string;
  engine: string;
  model: string;
  prompt_version: string;
  reading: Partial<Reading>;
  raw: string;
  error: string;
  input_tokens: number;
  output_tokens: number;
  latency_ms: number;
  created_at: string;
}

export interface MoveDetail extends Move {
  attempts: Attempt[] | null;
}

export interface Step {
  char: string;
  value: number;
  weight: number;
  product: number;
}

export interface Validation {
  input: string;
  normalized: string;
  well_formed: boolean;
  check_digit_valid: boolean;
  expected_check_digit: number | null;
  valid: boolean;
  steps?: Step[];
  sum: number;
  remainder: number;
}

export interface TestReadResult {
  attempt: {
    engine: string;
    model: string;
    prompt_version: string;
    reading: Partial<Reading>;
    raw: string;
    usage: { input_tokens: number; output_tokens: number };
    latency_ms: number;
  };
  frame: Frame;
  error?: string;
}

export interface Health {
  ok: boolean;
  version: string;
  engine: string;
  model: string;
  auth_required: boolean;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

// The hub rejects non-GET API requests that do not carry the X-Depot-Client
// header, so every call sends it.
async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  headers.set("X-Depot-Client", "web");
  const res = await fetch(path, { credentials: "same-origin", ...init, headers });
  if (!res.ok) {
    let code = "E0000";
    let message = res.statusText;
    try {
      const body = (await res.json()) as { error?: { code?: string; message?: string } };
      code = body.error?.code ?? code;
      message = body.error?.message ?? message;
    } catch {
      // not a JSON error body
    }
    throw new ApiError(res.status, code, message);
  }
  return (await res.json()) as T;
}

// Uploads over a bad mobile link can hang for minutes; give up after this long
// so the shot is marked failed and the clerk can retry.
const UPLOAD_TIMEOUT_MS = 90_000;

async function callWithTimeout<T>(path: string, init: RequestInit, ms: number): Promise<T> {
  const controller = new AbortController();
  const timer = window.setTimeout(() => controller.abort(), ms);
  try {
    return await call<T>(path, { ...init, signal: controller.signal });
  } catch (e) {
    if (controller.signal.aborted) throw new ApiError(0, "E0000", "request timed out");
    throw e;
  } finally {
    window.clearTimeout(timer);
  }
}

function postJSON(body: unknown): RequestInit {
  return { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
}

export const blobUrl = (digest: string) => `/api/blobs/${digest}`;

// typedNumber normalizes a container number typed on a phone keyboard. A Turkish
// keyboard produces İ and ı, which are not in the A-Z alphabet of container
// numbers; both mean I here.
export function typedNumber(s: string): string {
  return s.replace(/[İı]/g, "I").toUpperCase();
}

export const api = {
  health: () => call<Health>("/api/health"),
  login: (code: string) => call<{ ok: boolean }>("/api/login", postJSON({ code })),
  createMove: (lane: string, isTest: boolean) => call<Move>("/api/moves", postJSON({ lane, is_test: isTest })),
  listMoves: (status: string) =>
    call<{ moves: Move[] }>(`/api/moves?status=${encodeURIComponent(status)}&limit=100`),
  getMove: (id: string) => call<MoveDetail>(`/api/moves/${encodeURIComponent(id)}`),
  uploadPhoto: (id: string, role: string, original: Blob, ai: Blob, width: number, height: number) => {
    const form = new FormData();
    form.append("role", role);
    form.append("width", String(width));
    form.append("height", String(height));
    form.append("original", original, "original");
    form.append("ai", ai, "ai.jpg");
    return callWithTimeout<Photo>(
      `/api/moves/${encodeURIComponent(id)}/photos`,
      { method: "POST", body: form },
      UPLOAD_TIMEOUT_MS,
    );
  },
  resolve: (
    id: string,
    body: { action: "accept" | "correct" | "refuse"; number?: string; by: string; note?: string },
  ) => call<Move>(`/api/moves/${encodeURIComponent(id)}/resolve`, postJSON(body)),
  reread: (id: string) => call<Move>(`/api/moves/${encodeURIComponent(id)}/reread`, { method: "POST" }),
  validate: (number: string) => call<Validation>(`/api/validate/${encodeURIComponent(number)}`),
  testRead: (ai: Blob) => {
    const form = new FormData();
    form.append("ai", ai, "ai.jpg");
    return callWithTimeout<TestReadResult>("/api/test-read", { method: "POST", body: form }, UPLOAD_TIMEOUT_MS);
  },
};
