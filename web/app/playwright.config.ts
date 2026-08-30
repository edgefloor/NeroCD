import { defineConfig, devices } from "@playwright/test";
import { randomBytes } from "node:crypto";
const configuredPort = process.env.NEROCD_BROWSER_SMOKE_PORT ?? "18182";
if (!/^\d+$/.test(configuredPort)) throw new Error("NEROCD_BROWSER_SMOKE_PORT must be numeric");
const portNumber = Number(configuredPort);
if (!Number.isSafeInteger(portNumber) || portNumber < 1024 || portNumber > 65535) throw new Error("NEROCD_BROWSER_SMOKE_PORT must be between 1024 and 65535");
const port = String(portNumber);
const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? `http://127.0.0.1:${port}`;
const browserRunID = randomBytes(12).toString("hex");
process.env.NEROCD_BROWSER_RUN_ID = browserRunID;

export default defineConfig({
  testDir: "./tests/browser",
  timeout: 30_000,
  expect: {
    timeout: 10_000,
  },
  use: {
    baseURL,
    trace: "retain-on-failure",
    // Security regression tests must exercise the browser's actual CSP
    // enforcement rather than the Playwright bypass mode.
    bypassCSP: false,
  },
  webServer: process.env.PLAYWRIGHT_BASE_URL
    ? undefined
    : {
        command: `NEROCD_BROWSER_RUN_ID="${browserRunID}" sh scripts/browser-server.sh "${port}" "${baseURL}"`,
        url: baseURL,
        reuseExistingServer: false,
        timeout: 30_000,
        gracefulShutdown: { signal: "SIGTERM", timeout: 5_000 },
      },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
