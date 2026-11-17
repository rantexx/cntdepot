import type { DataProvider } from "@refinedev/core";
import { http } from "../http";

// Lines, customers and hauliers are paged lists; members and invitations are short plain arrays (the owner's own team).
const call = async <T>(url: string, method = "GET", body?: unknown): Promise<T> =>
  (
    await http<{ data: T }>(url, {
      method,
      headers: body === undefined ? undefined : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  ).data;

// Members have no id of their own; the user id is the key.
const withId = (row: Record<string, unknown>) => ({ ...row, id: row.id ?? row.userId });

export const dataProvider: DataProvider = {
  getApiUrl: () => "/api",
  getList: async ({ resource, pagination, filters }) => {
    // Every filter goes to the server as a query parameter of the same name.
    const q = new URLSearchParams();
    for (const f of filters ?? []) {
      if ("field" in f && f.value !== undefined && f.value !== null && f.value !== "") q.set(f.field, String(f.value));
    }
    if (pagination?.mode !== "off") {
      q.set("page", String(pagination?.currentPage ?? 1));
      q.set("perPage", String(pagination?.pageSize ?? 50));
    }
    // A list answers a plain array (members, orders, blocks, ...) or { items, total } (paged lists).
    const r = await call<Record<string, unknown>[] | { items: Record<string, unknown>[]; total: number }>(
      `/api/${resource}?${q.toString()}`,
    );
    if (Array.isArray(r)) return { data: r.map(withId) as never, total: r.length };
    return { data: r.items as never, total: r.total };
  },
  getOne: async ({ resource, id }) => {
    const rows = await call<{ items: Record<string, unknown>[] }>(`/api/${resource}?perPage=200`);
    return { data: rows.items.find((r) => r.id === id) as never };
  },
  create: async ({ resource, variables }) => ({ data: withId(await call(`/api/${resource}`, "POST", variables)) as never }),
  // A member is changed by user id and always sends role and depotIds together.
  update: async ({ resource, id, variables }) => ({
    data: withId(await call(`/api/${resource}/${encodeURIComponent(String(id))}`, "PATCH", variables)) as never,
  }),
  // DELETE on a list item deactivates it; on a member or invitation it removes it.
  deleteOne: async ({ resource, id }) => {
    await call(`/api/${resource}/${encodeURIComponent(String(id))}`, "DELETE");
    return { data: { id } as never };
  },
};
