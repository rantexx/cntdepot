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

let shownScope: string | undefined;
let scopeChanged: (() => void) | undefined;
/** The "<companyId>:<depotId>" this tab shows; the server refuses calls (409 E4097) once the session moved on. */
export function setShownScope(scope: string | undefined): void {
  shownScope = scope;
}
/** The app sets this to reload the session when the company or depot changed under the tab. */
export function onScopeChanged(fn: (() => void) | undefined): void {
  scopeChanged = fn;
}

// The hub rejects non-GET API requests without X-Depot-Client.
export async function http<T>(url: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  if (method !== "GET") headers.set("X-Depot-Client", "web");
  if (shownScope !== undefined) headers.set("X-Depot-Scope", shownScope);
  const res = await fetch(url, { ...init, headers, credentials: "include" });
  const text = await res.text();
  let body: unknown;
  try {
    body = text ? JSON.parse(text) : undefined;
  } catch {
    body = undefined;
  }
  if (!res.ok) {
    const e = (body as { error?: { code?: string; message?: string } } | undefined)?.error;
    if (res.status === 401) unauthorized?.();
    if (res.status === 409 && (e?.code === "E4097" || e?.code === "E4090")) scopeChanged?.();
    throw new ApiError(res.status, e?.code ?? "E0000", e?.message ?? res.statusText);
  }
  return { data: body, status: res.status, headers: res.headers } as T;
}
