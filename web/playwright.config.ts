import { existsSync } from "node:fs";
import { chromium, defineConfig, devices } from "@playwright/test";

const live = process.env.SWARM_E2E_LIVE === "1";
// Parallel worktrees share the default port, so set SWARM_E2E_PORT to test your own tree.
const mockPort = process.env.SWARM_E2E_PORT ?? "5174";
const mockURL = `http://127.0.0.1:${mockPort}`;
// Use the installed Chrome when the bundled Chromium has not been downloaded (as on a dev Mac).
const channel = existsSync(chromium.executablePath()) ? undefined : "chrome";
// The phone and tablet descriptors default to WebKit; the board is checked in Chromium.
const emulate = (device: keyof typeof devices) => ({
  ...devices[device],
  browserName: "chromium" as const,
  defaultBrowserType: "chromium" as const,
  channel,
  baseURL: mockURL,
});

export default defineConfig({
  testDir: "e2e",
  workers: 1,
  use: {
    ...devices["Desktop Chrome"],
    channel,
    viewport: { width: 1440, height: 900 },
    trace: "retain-on-failure",
  },
  projects: [
    { name: "mock", testMatch: /(mock|login)\.spec\.ts/, use: { baseURL: mockURL } },
    { name: "mobile", testMatch: /(mock|login)\.spec\.ts/, use: emulate("iPhone 13") },
    { name: "tablet", testMatch: /mock\.spec\.ts/, use: emulate("iPad Mini") },
    {
      name: "live",
      testMatch: /live\.spec\.ts/,
      use: { baseURL: process.env.SWARM_E2E_URL ?? "http://127.0.0.1:17777" },
    },
  ],
  webServer: live
    ? undefined
    : {
        command: `SWARM_MOCK=1 pnpm exec vite --host 127.0.0.1 --port ${mockPort} --strictPort`,
        url: mockURL,
        reuseExistingServer: !process.env.CI,
      },
});
