import { expect, test, type APIRequestContext, type Page } from "@playwright/test";

// ISO 6346 letter values skip 11, 22 and 33.
const LETTERS = "A10B12C13D14E15F16G17H18I19J20K21L23M24N25O26P27Q28R29S30T31U32V34W35X36Y37Z38";
const value = (c: string) => (/\d/.test(c) ? Number(c) : Number(LETTERS.slice(LETTERS.indexOf(c) + 1, LETTERS.indexOf(c) + 3)));

// A fresh, valid container number, so tests never meet each other's containers.
function containerNumber(): string {
  const first = "TSTU" + String(Math.floor(Math.random() * 1e6)).padStart(6, "0");
  const sum = [...first].reduce((s, c, i) => s + value(c) * 2 ** i, 0);
  return first + ((sum % 11) % 10);
}

const W = { "X-Depot-Client": "web" };

async function visitsOf(api: APIRequestContext, number: string) {
  const r = await api.get(`/api/visits?q=${number}`);
  expect(r.ok()).toBeTruthy();
  return ((await r.json()) as { items: { id: string; number: string; status: string }[] }).items.filter((v) => v.number === number);
}

async function movesOf(api: APIRequestContext, number: string) {
  const r = await api.get("/api/moves?limit=200");
  expect(r.ok()).toBeTruthy();
  const moves = ((await r.json()) as { moves: { id: string; number: string }[] }).moves.filter((m) => m.number === number);
  // The list leaves photos out; each move's own record has them.
  return Promise.all(moves.map(async (m) => (await (await api.get(`/api/moves/${m.id}`)).json()) as { id: string; photos: { role: string }[] | null }));
}

interface Form {
  id: string;
  voided: boolean;
  voidReason: string;
  flags: { code: string; detail: string }[];
}

async function formsOf(api: APIRequestContext, number: string): Promise<Form[]> {
  const moves = await movesOf(api, number);
  return Promise.all(moves.map(async (m) => (await (await api.get(`/api/moves/${m.id}/form`)).json()) as Form));
}

// A container already in the yard, with an open hold placed by the office.
async function heldInYard(api: APIRequestContext, number: string, reason: string) {
  const id = `E2E${Date.now()}${Math.floor(Math.random() * 1e6)}`;
  expect((await api.post("/api/moves", { headers: W, data: { id, lane: "1" } })).ok()).toBeTruthy();
  const saved = await api.post(`/api/moves/${id}/gate`, { headers: W, data: { direction: "in", number, typeCode: "22G1" } });
  expect(saved.ok()).toBeTruthy();
  const { visitId } = (await saved.json()) as { visitId: string };
  return () => api.post(`/api/visits/${visitId}/holds`, { headers: W, data: { reason } });
}

const outboxSize = (page: Page) =>
  page.evaluate(
    () =>
      new Promise<number>((resolve, reject) => {
        const r = indexedDB.open("depot-gate");
        r.onsuccess = () => {
          const q = r.result.transaction("outbox").objectStore("outbox").count();
          q.onsuccess = () => resolve(q.result);
          q.onerror = () => reject(q.error);
        };
        r.onerror = () => reject(r.error);
      }),
  );

async function sign(page: Page) {
  const pad = page.getByLabel("Driver's signature");
  await pad.scrollIntoViewIfNeeded();
  const box = (await pad.boundingBox())!;
  await page.mouse.move(box.x + 20, box.y + 20);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height - 20, { steps: 5 });
  await page.mouse.move(box.x + box.width - 20, box.y + 30, { steps: 5 });
  await page.mouse.up();
}

async function openGate(page: Page) {
  await page.goto("/#/gate");
  await expect(page.getByText(/Lists updated \d\d:\d\d/)).toBeVisible();
}

test("offline gate-in synced later", async ({ page, context }) => {
  const number = containerNumber();
  await openGate(page);
  // The service worker keeps the app shell: once it controls the page, a reload works offline.
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
  });
  await page.reload();
  await expect(page.getByText(/Lists updated/)).toBeVisible();
  await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);

  await context.setOffline(true);
  await page.reload();
  await expect(page.getByText("Offline", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Gate in" }).click();
  await page.getByLabel("Container number").fill(number);
  await expect(page.getByText("Check digit valid")).toBeVisible(); // worked out on the phone
  await page.getByLabel("Truck plate").fill("34 TST 01");
  await sign(page);
  await page.getByRole("button", { name: /^save$/i }).click();
  await expect(page.getByText("Saved on the phone")).toBeVisible();
  expect(await visitsOf(page.request, number)).toHaveLength(0); // still only on the phone

  await context.setOffline(false);
  await expect(page.getByRole("button", { name: "Share EIR" })).toBeVisible({ timeout: 30_000 });
  const visits = await visitsOf(page.request, number);
  expect(visits).toHaveLength(1);
  expect(visits[0].status).toBe("in_yard");
  const moves = await movesOf(page.request, number);
  expect(moves).toHaveLength(1);
  expect((moves[0].photos ?? []).map((p) => p.role)).toEqual(["signature"]);
});

test("gate-out refused by hold", async ({ page }) => {
  const number = containerNumber();
  const api = page.request;
  // In the yard, then put on hold by the office.
  const id = `E2E${Date.now()}${Math.floor(Math.random() * 1e6)}`;
  expect((await api.post("/api/moves", { headers: W, data: { id, lane: "1" } })).ok()).toBeTruthy();
  const saved = await api.post(`/api/moves/${id}/gate`, { headers: W, data: { direction: "in", number, typeCode: "45G1" } });
  expect(saved.ok()).toBeTruthy();
  const { visitId } = (await saved.json()) as { visitId: string };
  expect((await api.post(`/api/visits/${visitId}/holds`, { headers: W, data: { reason: "Customs inspection" } })).ok()).toBeTruthy();

  await openGate(page);
  await page.getByRole("button", { name: "Gate out" }).click();
  await page.getByLabel("Container number").fill(number);
  await expect(page.getByText("Customs inspection")).toBeVisible(); // from the phone's lists, before saving
  await page.getByRole("button", { name: /^save$/i }).click();

  const refused = page.getByRole("region", { name: "Refused" });
  await expect(refused.getByText("Customs inspection")).toBeVisible();
  await refused.getByLabel("Reason to let it through").fill("Released by phone, paperwork follows");
  await refused.getByRole("button", { name: "Let it through" }).click();
  await expect(page.getByRole("button", { name: "Share EIR" })).toBeVisible();
  const visits = await visitsOf(api, number);
  expect(visits).toHaveLength(1);
  expect(visits[0].status).toBe("out");
});

test("a form saved offline is stored with the conflict flagged, never refused", async ({ page, context }) => {
  const number = containerNumber();
  const api = page.request;
  const hold = await heldInYard(api, number, "Damage survey");
  // A release order, so the phone's lists expect the gate-out to pass.
  const order = { kind: "release", reference: `R${number}`, items: [{ number }] };
  expect((await api.post("/api/orders", { headers: W, data: order })).ok()).toBeTruthy();

  await openGate(page);
  await context.setOffline(true);
  // The phone's lists predate the hold: offline, nothing warns the clerk, and the truck leaves.
  expect((await hold()).ok()).toBeTruthy();
  await page.getByRole("button", { name: "Gate out" }).click();
  await page.getByLabel("Container number").fill(number);
  await page.getByRole("button", { name: /^save$/i }).click();
  await expect(page.getByText("Saved on the phone")).toBeVisible();
  await context.setOffline(false);

  // The truth at the gate wins: the move is stored, the hold conflict goes to the office.
  await expect(page.getByRole("button", { name: "Share EIR" })).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText("Saved offline, conflicted later: On hold: Damage survey")).toBeVisible();
  await expect(page.getByRole("region", { name: "Refused" })).toHaveCount(0);
  expect((await visitsOf(api, number))[0].status).toBe("out");
  const [form] = await formsOf(api, number).then((fs) => fs.filter((f) => !f.voided && f.flags.some((x) => x.code.startsWith("offline:"))));
  expect(form.flags.map((f) => [f.code, f.detail])).toContainEqual(["offline:hold_open", "Damage survey"]);
  await expect.poll(() => outboxSize(page)).toBe(0);
});

test("a save with no answer is kept as an offline save", async ({ page }) => {
  const number = containerNumber();
  const api = page.request;
  const hold = await heldInYard(api, number, "Damage survey");
  const order = { kind: "release", reference: `R${number}`, items: [{ number }] };
  expect((await api.post("/api/orders", { headers: W, data: order })).ok()).toBeTruthy();

  await openGate(page);
  // The phone is online but the form's send gets no answer: the clerk sees it kept, and the truck leaves.
  await page.route("**/api/moves/*/gate", (r) => r.abort());
  await page.getByRole("button", { name: "Gate out" }).click();
  await page.getByLabel("Container number").fill(number);
  await page.getByRole("button", { name: /^save$/i }).click();
  await expect(page.getByText("Saved on the phone")).toBeVisible({ timeout: 15_000 });
  expect((await hold()).ok()).toBeTruthy();
  await page.unroute("**/api/moves/*/gate");

  // Stored with the conflict flagged, never refused, so "Refuse truck" can never delete it.
  await expect(page.getByRole("button", { name: "Share EIR" })).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole("region", { name: "Refused" })).toHaveCount(0);
  expect((await visitsOf(api, number))[0].status).toBe("out");
  const [form] = await formsOf(api, number).then((fs) => fs.filter((f) => !f.voided && f.flags.some((x) => x.code.startsWith("offline:"))));
  expect(form.flags.map((f) => [f.code, f.detail])).toContainEqual(["offline:hold_open", "Damage survey"]);
  await expect.poll(() => outboxSize(page)).toBe(0);
});

test("offline, a refusal the lists predict holds the truck", async ({ page, context }) => {
  const number = containerNumber();
  const api = page.request;
  expect((await (await heldInYard(api, number, "Customs seal check"))()).ok()).toBeTruthy();
  await openGate(page); // the lists know the hold
  await context.setOffline(true);
  await page.getByRole("button", { name: "Gate out" }).click();
  await page.getByLabel("Container number").fill(number);
  await page.getByRole("button", { name: /^save$/i }).click();
  const hold = page.getByRole("region", { name: /Hold the truck/ });
  await expect(hold.getByText("Customs seal check")).toBeVisible();
  await expect(page.getByText("Saved on the phone")).toHaveCount(0); // nothing looks saved
  await hold.getByRole("button", { name: "Hold the truck" }).click();
  await expect(page.getByText("The truck waits")).toBeVisible();

  // clerk_override: a written reason lets it through, even offline.
  await page.getByRole("region", { name: "What the lists say" }).getByLabel("Reason to let it through").fill("Seal checked at the gate");
  await page.getByRole("button", { name: /^save$/i }).click();
  await expect(page.getByText("Saved on the phone")).toBeVisible();
  await context.setOffline(false);
  await expect(page.getByRole("button", { name: "Share EIR" })).toBeVisible({ timeout: 30_000 });
  expect((await visitsOf(api, number))[0].status).toBe("out");
});

test("a late failure never brings back an entry removed meanwhile", async ({ page }) => {
  const number = containerNumber();
  let release = () => {};
  const answered = new Promise<void>((r) => (release = r));
  // The form's send hangs, then fails; meanwhile another tab takes the entry off the phone.
  await page.route("**/api/moves/*/gate", async (route) => {
    await answered;
    await route.abort("connectionreset");
  });
  await openGate(page);
  await page.getByRole("button", { name: "Gate in" }).click();
  await page.getByLabel("Container number").fill(number);
  await page.getByRole("button", { name: /^save$/i }).click();
  await expect.poll(() => outboxSize(page)).toBe(1); // the move is sent; the form is in flight
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        const r = indexedDB.open("depot-gate");
        r.onsuccess = () => {
          const t = r.result.transaction("outbox", "readwrite");
          t.objectStore("outbox").clear();
          t.oncomplete = () => resolve();
        };
      }),
  );
  release();
  await page.waitForTimeout(2500); // the failure is recorded and the retry timer runs
  expect(await outboxSize(page)).toBe(0);
  await page.unrouteAll({ behavior: "ignoreErrors" });
});

test("replayed outbox makes no duplicates", async ({ page }) => {
  const number = containerNumber();
  // Each first send reaches the server, but its answer is lost: the outbox must send again.
  const lost = new Set<string>();
  const loseFirstAnswer = (name: string) => async (route: import("@playwright/test").Route) => {
    if (route.request().method() !== "POST" || lost.has(name)) return route.continue();
    lost.add(name);
    await route.fetch();
    await route.abort("connectionreset");
  };
  await page.route("**/api/moves", loseFirstAnswer("move"));
  await page.route("**/api/moves/*/photos", loseFirstAnswer("photo"));
  await page.route("**/api/moves/*/gate", loseFirstAnswer("form"));

  await openGate(page);
  await page.getByRole("button", { name: "Gate in" }).click();
  await page.getByLabel("Container number").fill(number);
  await sign(page);
  await page.getByRole("button", { name: /^save$/i }).click();
  await expect(page.getByRole("button", { name: "Share EIR" })).toBeVisible({ timeout: 45_000 });
  expect([...lost].sort()).toEqual(["form", "move", "photo"]);

  expect(await visitsOf(page.request, number)).toHaveLength(1);
  const moves = await movesOf(page.request, number);
  expect(moves).toHaveLength(1);
  expect((moves[0].photos ?? []).map((p) => p.role)).toEqual(["signature"]);
});
