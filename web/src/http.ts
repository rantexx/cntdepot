// Orval mutator: every generated client call goes through here.

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

let unauthorized: (() => void) | undefined;
/** The app sets this to send the user to sign in when a call answers 401. */
export function onUnauthorized(fn: (() => void) | undefined): void {
  unauthorized = fn;
}

// The hub rejects non-GET API requests without X-Depot-Client.
export async function http<T>(url: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  if (method !== "GET") headers.set("X-Depot-Client", "web");
  const res = await fetch(url, { ...init, headers, credentials: "include" });
  const text = await res.text();
  let body: unknown;
  try {
    body = text ? JSON.parse(text) : undefined;
  } catch {
    body = undefined;
  }
  if (!res.ok) {
    if (res.status === 401) unauthorized?.();
    const e = (body as { error?: { code?: string; message?: string } } | undefined)?.error;
    throw new ApiError(res.status, e?.code ?? "E0000", e?.message ?? res.statusText);
  }
  return { data: body, status: res.status, headers: res.headers } as T;
}
