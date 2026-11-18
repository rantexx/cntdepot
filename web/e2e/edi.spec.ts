import { expect, test, type Page } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";

// Line EDI on the real hub: a COPARN upload becomes an order, and a gate-out sends a CODECO that lands
// as an .eml in the hub's mail-out folder. With SHOTS=1 the screenshots go to .impeccable/review/edi-*.jpg.
const SHOTS = path.resolve(import.meta.dirname, "../../.impeccable/review");
const shot = async (page: Page, name: string) => {
  if (!process.env.SHOTS) return;
  fs.mkdirSync(SHOTS, { recursive: true });
  await page.screenshot({ path: path.join(SHOTS, `edi-${name}.jpg`), type: "jpeg", quality: 80, fullPage: true });
};

const W = { "X-Depot-Client": "web" };
const NUMBER = "MSCU1234566"; // the container in the fixture's COPARN

test("COPARN upload becomes an order and a gate-out sends CODECO", async ({ page }) => {
  test.setTimeout(120_000);
  const api = page.request;
  await page.setViewportSize({ width: 1280, height: 900 });

  // 1. A line on EDI, our party id, and the depot's EDI location.
  expect((await api.put("/api/edi/company", { headers: W, data: { partyId: "DEPOTX" } })).ok()).toBeTruthy();
  expect((await api.put("/api/depots/own-depot/edi-location", { headers: W, data: { location: "TRMER" } })).ok()).toBeTruthy();
  const made = await api.post("/api/lines", { headers: W, data: { code: "E2E", name: "E2E Line" } });
  expect(made.ok()).toBeTruthy();
  const line = (await made.json()) as { id: string };
  const profile = {
    senderId: "E2ELINE", senderQual: "ZZZ", recipientId: "E2ELINE", recipientQual: "ZZZ", lineCode: "", sendTo: ["line@example.com"],
    allowedFrom: [], emptiesOnly: false, sendAperak: true, test: false, typeMap: [], reportTo: [], reportTime: "",
  };
  const set = await api.put(`/api/edi/lines/${line.id}`, { headers: W, data: { mode: "edi", profile } });
  expect(set.ok(), await set.text()).toBeTruthy();

  // 2. Upload the COPARN on the EDI screen.
  await page.goto("/#/edi");
  await page.locator('input[type="file"]').setInputFiles(path.resolve(import.meta.dirname, "fixtures/coparn_release.edi"));
  await page.getByRole("button", { name: "Upload" }).click();
  const coparn = page.locator("tbody tr", { hasText: "COPARN" }).first();
  await expect(coparn).toContainText("Done");
  await shot(page, "1-list");
  await coparn.click();
  await expect(page.getByText("COPARN:D:00B")).toBeVisible();
  await shot(page, "2-detail");

  // 3. The order is listed.
  await page.goto("/#/orders");
  await expect(page.getByText("RLS4471").first()).toBeVisible();

  // 4. Gate the container in and out through the API.
  const id = `E2E${Date.now()}`;
  expect((await api.post("/api/moves", { headers: W, data: { id, lane: "1" } })).ok()).toBeTruthy();
  const gin = await api.post(`/api/moves/${id}/gate`, { headers: W, data: { direction: "in", number: NUMBER, typeCode: "22G1", lineId: line.id } });
  expect(gin.ok(), await gin.text()).toBeTruthy();
  const out = `${id}o`;
  expect((await api.post("/api/moves", { headers: W, data: { id: out, lane: "1" } })).ok()).toBeTruthy();
  const gout = await api.post(`/api/moves/${out}/gate`, { headers: W, data: { direction: "out", number: NUMBER, typeCode: "22G1", lineId: line.id } });
  expect(gout.ok(), await gout.text()).toBeTruthy();

  // 5. A CODECO row is sent.
  await page.goto("/#/edi");
  const codeco = page.locator("tbody tr", { hasText: "CODECO" }).first();
  await expect(async () => {
    await page.getByRole("button", { name: "Refresh" }).click();
    await expect(codeco).toContainText("Sent", { timeout: 1500 });
  }).toPass({ timeout: 15_000 });

  // 6. The .eml in mail-out carries the CODECO (BGM 111, the SMDG gate-out code) as a base64 attachment.
  const dir = process.env.EDI_E2E_MAIL!;
  const text = fs.readdirSync(dir).filter((f) => f.endsWith(".eml")).map((f) => {
    const raw = fs.readFileSync(path.join(dir, f), "utf8");
    return raw + "\n" + [...raw.matchAll(/^([A-Za-z0-9+/=]{20,})$/gm)].map((m) => Buffer.from(m[1], "base64").toString("latin1")).join("");
  });
  expect(text.some((t) => t.includes("BGM+111"))).toBeTruthy();

  // 7. The Lines tab.
  await page.getByRole("button", { name: "Lines" }).click();
  await expect(page.getByLabel("Our EDI party id")).toHaveValue("DEPOTX");
  await page.getByRole("button", { name: /E2E/ }).first().click();
  await shot(page, "3-lines");

  // 8. The same screens in Turkish (SHOTS=1 only looks at them; the strings are checked by the type system).
  if (process.env.SHOTS) {
    await page.getByRole("button", { name: "TR", exact: true }).click();
    await shot(page, "3-lines-tr");
    await page.getByRole("button", { name: "Mesajlar" }).click();
    await shot(page, "1-list-tr");
    await page.locator("tbody tr", { hasText: "COPARN" }).first().click();
    await shot(page, "2-detail-tr");
    await page.getByRole("button", { name: "EN", exact: true }).click();
  }
});
