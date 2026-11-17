import { defineConfig, devices } from "@playwright/test";

// The tests drive a real hub (fake OCR engine) that e2e/hub.ts starts and stops.
export default defineConfig({
  testDir: "e2e",
  testMatch: "*.spec.ts",
  globalSetup: "./e2e/hub.ts",
  workers: 1, // one hub, one shared session
  timeout: 90_000,
  expect: { timeout: 15_000 },
  reporter: "list",
  use: {
    ...devices["Pixel 7"],
    baseURL: "http://127.0.0.1:18093",
    storageState: "e2e/.auth.json",
  },
});
