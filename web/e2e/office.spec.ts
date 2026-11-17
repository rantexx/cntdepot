import { expect, test, type Page } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";

// The office walk-through of Stage 1 on the real hub: import, order, gate-out on a phone, hold,
// manager approval, EIR link, CSV export. Screenshots go to .impeccable/review/stage1-*.jpg.
const SHOTS = path.resolve(import.meta.dirname, "../../.impeccable/review");
fs.mkdirSync(SHOTS, { recursive: true });
const shot = (page: Page, name: string) => page.screenshot({ path: path.join(SHOTS, `stage1-${name}.jpg`), type: "jpeg", quality: 80, fullPage: true });

// ISO 6346 letter values skip 11, 22 and 33.
const LETTERS = "A10B12C13D14E15F16G17H18I19J20K21L23M24N25O26P27Q28R29S30T31U32V34W35X36Y37Z38";
const value = (c: string) => (/\d/.test(c) ? Number(c) : Number(LETTERS.slice(LETTERS.indexOf(c) + 1, LETTERS.indexOf(c) + 3)));
function containerNumber(): string {
  const first = "TSTU" + String(Math.floor(Math.random() * 1e6)).padStart(6, "0");
  const sum = [...first].reduce((s, c, i) => s + value(c) * 2 ** i, 0);
  return first + ((sum % 11) % 10);
}

test("office walk-through", async ({ page: phone, browser }) => {
  test.setTimeout(420_000);
  const [a, b, old] = [containerNumber(), containerNumber(), containerNumber()];
  const ctx = await browser.newContext({ baseURL: "http://127.0.0.1:18093", storageState: "e2e/.auth.json", viewport: { width: 1280, height: 900 }, acceptDownloads: true });
  const desk = await ctx.newPage();

  // 1. Import a sample stock CSV (one old container for the ageing colour).
  await desk.goto("/#/import");
  const csv =
    "number,type_code,line_code,customer_code,full_empty,in_date,block,row,tier,grade\n" +
    `${a},45G1,,,E,2026-11-10,A,1,1,A\n${b},45G1,,,E,2026-11-10,A,1,2,B\n${old},22G1,,,E,2026-07-01,B,2,1,C\n`;
  await desk.locator('input[type="file"]').first().setInputFiles({ name: "stock.csv", mimeType: "text/csv", buffer: Buffer.from(csv) });
  await desk.getByRole("button", { name: "Upload" }).first().click();
  await expect(desk.getByRole("status")).toContainText("Imported: 3");
  await shot(desk, "1-import");
  await desk.goto("/#/yard");
  await expect(desk.getByText(a.slice(0, 4))).toHaveCount(3);
  await shot(desk, "1-yard");

  // 2. A release order for 2 x 45G1.
  await desk.goto("/#/orders");
  await desk.getByLabel("Reference").fill("REL-2026-001");
  await desk.locator("form.card").getByLabel("ISO type code").fill("45G1");
  await desk.locator("form.card").getByLabel("Quantity").fill("2");
  await desk.getByRole("button", { name: "Create order" }).click();
  await expect(desk.getByRole("heading", { name: /REL-2026-001/ })).toBeVisible();
  await expect(desk.getByText("0 / 2")).toBeVisible();
  await shot(desk, "2-order");

  // 3. Gate out container A on a phone.
  await phone.goto("/#/gate");
  await phone.reload(); // the same hash does not start a new truck by itself
  await expect(phone.getByText(/Lists updated \d\d:\d\d/)).toBeVisible();
  await phone.getByRole("button", { name: "Gate out" }).click();
  await phone.getByLabel("Container number").fill(a);
  await phone.getByRole("button", { name: /^save$/i }).click();
  await expect(phone.getByRole("button", { name: "Share EIR" })).toBeVisible();
  await shot(phone, "3-gate-out-phone");
  await desk.reload();
  await expect(desk.getByText("1 / 2")).toBeVisible();

  // 4. Hold B, then the gate-out is refused.
  await desk.goto("/#/yard");
  await desk.getByPlaceholder("Container no").fill(b);
  await desk.getByRole("link", { name: new RegExp(b.slice(4, 10)) }).click();
  await desk.getByLabel("Reason").first().fill("Customs inspection");
  await desk.getByRole("button", { name: "Place hold" }).click();
  await expect(desk.getByText("On hold").first()).toBeVisible();
  await shot(desk, "4-visit-hold");
  await phone.goto("/#/gate");
  await phone.reload(); // the same hash does not start a new truck by itself
  await expect(phone.getByText(/Lists updated \d\d:\d\d/)).toBeVisible();
  await phone.getByRole("button", { name: "Gate out" }).click();
  await phone.getByLabel("Container number").fill(b);
  await phone.getByRole("button", { name: /^save$/i }).click();
  const refused = phone.getByRole("region", { name: "Refused" });
  await expect(refused.getByText("Customs inspection")).toBeVisible();
  await shot(phone, "4-gate-out-refused");
  await refused.getByRole("button", { name: "Refuse the truck" }).click();
  // The turned-away truck is an open gate issue; the office resolves it with a note.
  await desk.goto("/#/issues");
  await expect(desk.getByText("Turned away")).toBeVisible();
  await shot(desk, "4-issues");
  await desk.getByLabel("What was done? (note)").first().fill("Driver sent back until customs clear it");
  await desk.getByRole("button", { name: "Resolve" }).first().click();
  await expect(desk.getByText("No open gate issues.")).toBeVisible();

  // 5. Switch to manager approval, retry, and approve from the Approvals screen.
  await desk.goto("/#/settings");
  await desk.getByLabel(/Manager approval/).check();
  await desk.getByRole("button", { name: "Save", exact: true }).click();
  await expect(desk.getByText("Saved", { exact: true })).toBeVisible();
  await shot(desk, "5-settings");
  await desk.goto("/#/approvals");
  await phone.goto("/#/gate");
  await phone.reload(); // the same hash does not start a new truck by itself
  await expect(phone.getByText(/Lists updated \d\d:\d\d/)).toBeVisible();
  await phone.getByRole("button", { name: "Gate out" }).click();
  await phone.getByLabel("Container number").fill(b);
  await phone.getByRole("button", { name: /^save$/i }).click();
  await expect(phone.getByText("Waiting for the manager…")).toBeVisible();
  await expect(desk.getByRole("button", { name: "Approve" })).toBeVisible({ timeout: 20_000 }); // live, no reload
  await shot(desk, "5-approvals");
  await desk.getByRole("button", { name: "Approve" }).click();
  await expect(desk.getByText("No approvals waiting.")).toBeVisible();
  await expect(phone.getByRole("button", { name: "Share EIR" })).toBeVisible({ timeout: 30_000 });
  await shot(phone, "5-approved-phone");

  // 6. The EIR link of container A opens.
  await desk.goto("/#/yard");
  await desk.getByLabel("State").selectOption(""); // A has left the yard
  await desk.getByPlaceholder("Container no").fill(a);
  await desk.getByRole("link", { name: new RegExp(a.slice(4, 10)) }).click();
  const [eir] = await Promise.all([ctx.waitForEvent("page"), desk.getByRole("button", { name: "Open EIR" }).first().click()]);
  await eir.waitForLoadState();
  await expect(eir.getByText(a.slice(0, 4))).toBeVisible();
  await shot(eir, "6-eir");
  await shot(desk, "6-visit");

  // 7. Export the stock CSV.
  await desk.goto("/#/yard");
  await desk.getByLabel("State").selectOption("");
  const [dl] = await Promise.all([desk.waitForEvent("download"), desk.getByRole("link", { name: "CSV" }).click()]);
  const text = fs.readFileSync(await dl.path(), "utf8");
  expect(text).toContain(old);
  expect(text.charCodeAt(0)).toBe(0xfeff);
  await shot(desk, "7-yard-export");
  await ctx.close();
});
