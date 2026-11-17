// What the gate phone knows for working offline (spec 3.8): the depot's open orders, the block
// list, open holds and the containers in the yard, plus the lines and hauliers for the form.
// Kept in IndexedDB per scope, refreshed whenever the phone is online.
import { useCallback, useEffect, useState } from "react";
import {
  gateSnapshot,
  listHauliers,
  listLines,
  type Block,
  type GateSnapshot,
  type Haulier,
  type Hold,
  type Line,
  type Order,
  type OrderItem,
  type Visit,
} from "../gen/api";
import { ok } from "../hubApi";
import { run } from "./idb";

export interface Lists {
  snapshot: GateSnapshot;
  lines: Line[];
  hauliers: Haulier[];
}

const key = (scope: string) => `lists:${scope}`;

// shortcut: the first 200 lines and hauliers; page through them if a depot ever has more.
async function fetchLists(scope: string): Promise<Lists> {
  const [snapshot, lines, hauliers] = await Promise.all([
    ok(gateSnapshot()),
    ok(listLines({ perPage: 200 })),
    ok(listHauliers({ perPage: 200 })),
  ]);
  const lists = { snapshot, lines: lines.items.filter((l) => l.active), hauliers: hauliers.items.filter((h) => h.active) };
  await run("kv", "readwrite", (t) => {
    t.objectStore("kv").put(lists, key(scope));
  });
  return lists;
}

/** The lists for scope: the cached copy at once, then the server's whenever online (and every minute). */
export function useLists(scope: string) {
  const [lists, setLists] = useState<Lists | undefined>();
  const refresh = useCallback(() => {
    if (!navigator.onLine) return;
    fetchLists(scope).then(setLists, () => undefined); // keep the cached copy
  }, [scope]);
  useEffect(() => {
    let live = true;
    run<Lists | undefined>("kv", "readonly", (t) => t.objectStore("kv").get(key(scope))).then(
      (cached) => {
        if (live && cached) setLists((now) => now ?? cached);
      },
      () => undefined,
    );
    refresh();
    const timer = window.setInterval(refresh, 60_000);
    window.addEventListener("online", refresh);
    return () => {
      live = false;
      window.clearInterval(timer);
      window.removeEventListener("online", refresh);
    };
  }, [scope, refresh]);
  return { lists, refresh };
}

const norm = (s: string | null | undefined) => (s ?? "").replace(/[İı]/g, "I").toUpperCase().replace(/[^A-Z0-9]/g, "");

export interface OrderMatch {
  order: Order;
  item: OrderItem;
  expired: boolean;
}

export interface GateStatus {
  block?: Block;
  visit?: Visit;
  holds: Hold[];
  orders: OrderMatch[];
  /** the refusal codes the server will most likely answer, as in internal/yard/rules.go */
  refusals: string[];
}

/** What the lists say about a container at the gate. The server decides on save; this is the phone's best guess. */
export function gateStatus(
  { snapshot: s, lines }: Lists,
  f: { direction: "in" | "out"; number: string; typeCode: string; fullEmpty: string; lineId: string },
): GateStatus {
  const number = norm(f.number);
  const visit = s.inYard.find((v) => norm(v.number) === number);
  const line = f.lineId || (f.direction === "out" ? (visit?.lineId ?? "") : "");
  const lineCode = norm(lines.find((l) => l.id === line)?.code);
  const block = s.blocks.find((b) => b.active && (b.kind === "container" ? norm(b.value) === number : !!lineCode && norm(b.value) === lineCode));
  const holds = visit ? s.holds.filter((h) => h.visitId === visit.id && !h.releasedAt) : [];
  const kind = f.direction === "out" ? "release" : "redelivery";
  const type = norm(f.typeCode || visit?.typeCode);
  const today = new Date().toISOString().slice(0, 10);
  const orders: OrderMatch[] = [];
  for (const o of s.orders) {
    if (o.kind !== kind || o.status !== "open" || (o.lineId && line && o.lineId !== line)) continue;
    for (const item of o.items) {
      if (item.used >= item.quantity || (item.fullEmpty && norm(item.fullEmpty) !== norm(f.fullEmpty || "E"))) continue;
      const hit = item.number ? norm(item.number) === number : type.length >= 3 && norm(item.typeCode).slice(0, 3) === type.slice(0, 3);
      if (hit) orders.push({ order: o, item, expired: !!o.validUntil && o.validUntil.slice(0, 10) < today });
    }
  }
  const refusals: string[] = [];
  if (block) refusals.push("blocked");
  else {
    if (f.direction === "out" && holds.length > 0) refusals.push("hold_open");
    const live = orders.filter((m) => !m.expired);
    if (live.length === 0 && (f.direction === "out" || !s.settings.walkinsAllowed)) {
      refusals.push(orders.length > 0 ? "order_expired" : "no_order");
    }
  }
  return { block, visit, holds, orders, refusals };
}

/** The line the depot last used for the number's owner prefix (spec 3.8). */
export const suggestedLine = (s: GateSnapshot, number: string) =>
  s.linePrefixes.find((p) => p.prefix === norm(number).slice(0, 4))?.lineId ?? "";
