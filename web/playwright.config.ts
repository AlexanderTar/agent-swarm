import { defineConfig, devices } from "@playwright/test";

const live = process.env.SWARM_E2E_LIVE === "1";

export default defineConfig({
  testDir: "e2e",
  workers: 1,
  use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 }, trace: "retain-on-failure" },
  projects: [
    { name: "mock", testMatch: /mock\.spec\.ts/, use: { baseURL: "http://127.0.0.1:5174" } },
    { name: "live", testMatch: /live\.spec\.ts/, use: { baseURL: process.env.SWARM_E2E_URL ?? "http://127.0.0.1:17777" } },
  ],
  webServer: live
    ? undefined
    : {
        command: "SWARM_MOCK=1 pnpm exec vite --host 127.0.0.1 --port 5174 --strictPort",
        url: "http://127.0.0.1:5174",
        reuseExistingServer: !process.env.CI,
      },
});
