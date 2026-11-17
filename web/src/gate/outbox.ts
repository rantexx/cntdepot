// The gate phone's outbox (spec 3.3): every gate move goes here first, so a save never waits
// for the network. Entries are sent in the order they were saved, in the scope (company and
// depot) they were saved in. The server treats a resent id as the same move and a resent photo
// as the same photo, so sending again after a lost answer is harmless.
import { useSyncExternalStore } from "react";
import { addPhoto, createMove, saveGateForm, type GateForm, type GateRefusal, type GateResult } from "../gen/api";
import { ApiError, getShownScope } from "../http";
import { ok, UPLOAD_TIMEOUT_MS } from "../hubApi";
import { run } from "./idb";

export interface PhotoPayload {
  role: string;
  original: Blob;
  ai: Blob;
  width: number;
  height: number;
}

interface Base {
  seq?: number; // IndexedDB key: the order of saving
  id: string; // ULID
  moveId: string;
  /** "<companyId>:<depotId>" the entry was made in, sent as X-Depot-Scope so it cannot land in another depot. */
  scope: string;
  tries: number;
  state: "pending" | "refused" | "failed";
  /** a refused form: the server's reasons (409 E4098 or E4099) */
  refusal?: GateRefusal;
  error?: string;
  savedAt: string;
}

/** A form's damage and signature photo ids are the ids of this outbox's photo entries until sent. */
export type Entry = Base &
  ({ kind: "move"; payload: { lane: string } } | { kind: "photo"; payload: PhotoPayload } | { kind: "form"; payload: { form: GateForm } });

export type NewEntry = Omit<Entry, "seq" | "tries" | "state" | "savedAt" | "refusal" | "error">;

const CROCKFORD = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

/** A ULID: 48 bits of milliseconds and 80 random bits, in Crockford base 32 (crypto.randomUUID needs HTTPS). */
export function ulid(): string {
  let t = Date.now();
  let time = "";
  for (let i = 0; i < 10; i++) {
    time = CROCKFORD[t % 32] + time;
    t = Math.floor(t / 32);
  }
  return time + Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => CROCKFORD[b % 32]).join("");
}

let entries: Entry[] = [];
const listeners = new Set<() => void>();
const results = new Map<string, GateResult>(); // forms this tab sent, by move id

async function reload() {
  entries = await run<Entry[]>("outbox", "readonly", (t) => t.objectStore("outbox").getAll());
  listeners.forEach((l) => l());
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

/** The outbox as it is now, oldest first; the component re-renders when it changes. */
export function useOutbox(): Entry[] {
  return useSyncExternalStore(subscribe, () => entries);
}

/** The server's answer to a form this tab sent. */
export const resultOf = (moveId: string) => results.get(moveId);

/** Adds a saved truck's entries in one transaction (never half a truck) and starts sending. */
export async function enqueue(list: NewEntry[]) {
  const savedAt = new Date().toISOString();
  await run("outbox", "readwrite", (t) => {
    const store = t.objectStore("outbox");
    for (const e of list) store.add({ ...e, tries: 0, state: "pending", savedAt });
  });
  await reload();
  void flush();
}

/** Sends a refused or failed entry again, a form with the clerk's change (override reason, chosen order item, approval). */
export async function retry(e: Entry, change?: Partial<GateForm>) {
  const next = { ...e, state: "pending", refusal: undefined, error: undefined } as Entry;
  if (change && next.kind === "form") next.payload = { form: { ...next.payload.form, ...change } };
  await run("outbox", "readwrite", (t) => {
    t.objectStore("outbox").put(next);
  });
  await reload();
  failures = 0;
  void flush();
}

/** The truck was turned away: its unsent entries go. What was already sent stays on the server for the office. */
export async function drop(moveId: string) {
  await run("outbox", "readwrite", (t) => {
    const store = t.objectStore("outbox");
    for (const e of entries) if (e.moveId === moveId && e.seq !== undefined) store.delete(e.seq);
  });
  await reload();
}

async function settle(e: Entry, change: Partial<Base>) {
  await run("outbox", "readwrite", (t) => {
    t.objectStore("outbox").put({ ...e, ...change, tries: e.tries + 1 });
  });
}

// The form with its photo ids swapped for the server's; undefined when a photo was never sent.
async function serverIds(form: GateForm): Promise<GateForm | undefined> {
  const refs = [...(form.damages ?? []).flatMap((d) => d.photoIds), ...(form.signaturePhotoId ? [form.signaturePhotoId] : [])];
  const map = new Map<string, string | undefined>();
  await run("photoIds", "readonly", (t) => {
    const store = t.objectStore("photoIds");
    for (const r of refs) {
      const req = store.get(r) as IDBRequest<string | undefined>;
      req.onsuccess = () => map.set(r, req.result);
    }
  });
  if ([...map.values()].some((v) => !v)) return undefined;
  return {
    ...form,
    signaturePhotoId: form.signaturePhotoId ? map.get(form.signaturePhotoId) : undefined,
    damages: form.damages?.map((d) => ({ ...d, photoIds: d.photoIds.map((p) => map.get(p)!) })),
  };
}

type Outcome = "done" | "held" | "retry";

async function send(e: Entry): Promise<Outcome> {
  const opts = (ms: number) => ({ headers: { "X-Depot-Scope": e.scope }, signal: AbortSignal.timeout(ms) });
  const remove = (t: IDBTransaction) => t.objectStore("outbox").delete(e.seq!);
  try {
    if (e.kind === "move") {
      await ok(createMove({ id: e.moveId, lane: e.payload.lane }, opts(30_000)));
      await run("outbox", "readwrite", remove);
    } else if (e.kind === "photo") {
      const p = e.payload;
      const photo = await ok(
        addPhoto(e.moveId, { role: p.role, width: p.width, height: p.height, original: p.original, ai: p.ai }, opts(UPLOAD_TIMEOUT_MS)),
      );
      await run(["outbox", "photoIds"], "readwrite", (t) => {
        remove(t);
        t.objectStore("photoIds").put(photo.id, e.id);
      });
    } else {
      const form = await serverIds(e.payload.form);
      if (!form) {
        await settle(e, { state: "failed", error: "a photo of this truck was not sent" });
        return "held";
      }
      results.set(e.moveId, await ok(saveGateForm(e.moveId, form, opts(30_000))));
      await run(["outbox", "photoIds"], "readwrite", (t) => {
        remove(t);
        const ids = t.objectStore("photoIds");
        for (const d of e.payload.form.damages ?? []) d.photoIds.forEach((p) => ids.delete(p));
        if (e.payload.form.signaturePhotoId) ids.delete(e.payload.form.signaturePhotoId);
      });
    }
    return "done";
  } catch (err) {
    // No answer, a server fault, a signed-out or moved session: the same entry goes again later.
    if (!(err instanceof ApiError) || err.status >= 500 || err.status === 401 || err.status === 429 || err.code === "E4097") {
      await settle(e, {});
      return "retry";
    }
    if (e.kind === "form" && err.status === 409 && (err.code === "E4098" || err.code === "E4099")) {
      await settle(e, { state: "refused", refusal: err.body as GateRefusal });
    } else {
      await settle(e, { state: "failed", error: err.message });
    }
    return "held";
  }
}

// One pass in order. A truck whose entry is refused or failed holds its later entries back;
// other trucks go on. No answer at all stops the pass, so the order is kept.
async function drain(): Promise<"idle" | "retry"> {
  const scope = getShownScope();
  const held = new Set<string>();
  for (const e of [...entries]) {
    if (!navigator.onLine) return "idle"; // the online event starts the next pass
    if (e.scope !== scope) continue; // another depot's work waits until the phone shows that depot
    if (e.state !== "pending" || held.has(e.moveId)) {
      held.add(e.moveId);
      continue;
    }
    const outcome = await send(e);
    await reload();
    if (outcome === "retry") return "retry";
    if (outcome === "held") held.add(e.moveId);
  }
  return "idle";
}

let running: Promise<void> | null = null;
let again = false;
let failures = 0;
let timer: number | undefined;

/** Sends what can be sent now; called on save, when the network returns, and by the retry timer. */
export function flush(): Promise<void> {
  if (running) {
    again = true;
    return running;
  }
  running = (async () => {
    try {
      do {
        again = false;
        window.clearTimeout(timer);
        await reload();
        const r = await drain().catch(() => "retry" as const);
        if (r === "retry") {
          failures++;
          timer = window.setTimeout(() => void flush(), Math.min(30_000, 1000 * 2 ** (failures - 1))); // 1 s, 2 s, 4 s … 30 s
          return;
        }
        failures = 0;
      } while (again);
    } finally {
      running = null;
    }
  })();
  return running;
}

/** Starts sending for this tab: now, and whenever the network returns. */
export function startOutbox(): () => void {
  const online = () => {
    failures = 0;
    void flush();
  };
  window.addEventListener("online", online);
  void flush();
  return () => {
    window.removeEventListener("online", online);
    window.clearTimeout(timer);
  };
}
