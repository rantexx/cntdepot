// Playwright global setup: starts `depotd hub -engine fake` on a fresh data folder,
// signs in with the dev login, picks the depot, and stops the hub (by PID) afterwards.
// Run `npm run build` first: the hub serves the web app it was compiled with.
import { request } from "@playwright/test";
import { spawn, spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const BASE = "http://127.0.0.1:18093";
const REPO = path.resolve(import.meta.dirname, "../..");

async function up(): Promise<boolean> {
  try {
    return (await fetch(`${BASE}/api/health`)).ok;
  } catch {
    return false;
  }
}

export default async function setup() {
  if (await up()) throw new Error(`something already listens on ${BASE}; stop it first`);
  const data = fs.mkdtempSync(path.join(os.tmpdir(), "depot-e2e-"));
  const goBin = process.platform === "win32" ? "C:\\Program Files\\Go\\bin" : "";
  process.env.EDI_E2E_MAIL = path.join(data, "mail-out"); // the specs read the .eml files here
  const log = fs.openSync(path.join(data, "hub.log"), "w");
  const hub = spawn("go", ["run", "./cmd/depotd", "hub", "-data", data, "-addr", "127.0.0.1:18093", "-engine", "fake"], {
    cwd: REPO,
    env: { ...process.env, EDI_TICK: "1s", MAIL_DIR: path.join(data, "mail-out"), PATH: [process.env.PATH, goBin].filter(Boolean).join(path.delimiter) },
    stdio: ["ignore", log, log],
    detached: process.platform !== "win32", // its own process group, so the group can be stopped
  });
  const stop = () => {
    if (hub.pid !== undefined && hub.exitCode === null) {
      // go run starts the compiled hub as its child: stop the tree.
      if (process.platform === "win32") spawnSync("taskkill", ["/PID", String(hub.pid), "/T", "/F"]);
      else process.kill(-hub.pid, "SIGKILL");
    }
    // The hub's PostgreSQL runs detached (pg_ctl), so a killed hub leaves it running: stop it too.
    const pgCtl = path.join(data, "pg-run", "bin", process.platform === "win32" ? "pg_ctl.exe" : "pg_ctl");
    if (fs.existsSync(pgCtl)) spawnSync(pgCtl, ["stop", "-D", path.join(data, "pg"), "-m", "fast"]);
  };
  try {
    const deadline = Date.now() + 240_000; // first run compiles and unpacks PostgreSQL
    while (!(await up())) {
      if (hub.exitCode !== null) throw new Error(`hub exited; see ${path.join(data, "hub.log")}`);
      if (Date.now() > deadline) throw new Error(`hub did not start; see ${path.join(data, "hub.log")}`);
      await new Promise((r) => setTimeout(r, 1000));
    }
    const api = await request.newContext({ baseURL: BASE });
    const login = await api.get("/auth/login"); // follows to the dev sign-in form
    const state = new URL(login.url()).searchParams.get("state") ?? "";
    await api.post("/auth/dev", { form: { email: "owner@local", state } });
    const scope = await api.post("/api/session/scope", {
      headers: { "X-Depot-Client": "web" },
      data: { companyId: "local", depotId: "own-depot" },
    });
    if (!scope.ok()) throw new Error(`choose depot: ${scope.status()} ${await scope.text()}`);
    await api.storageState({ path: path.join(import.meta.dirname, ".auth.json") });
    await api.dispose();
  } catch (e) {
    stop();
    throw e;
  }
  return () => {
    stop();
    fs.closeSync(log);
    fs.rmSync(data, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 });
  };
}
