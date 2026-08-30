import { defineConfig, devices } from "@playwright/test";
import { tmpdir } from "node:os";
import { join } from "node:path";

const port = process.env.NEROCD_BROWSER_SMOKE_PORT ?? "18182";
const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? `http://127.0.0.1:${port}`;
const bootstrapDir = join(tmpdir(), `nerocd-browser-${port}`);

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
        command: `sh scripts/browser-server.sh "${port}" "${baseURL}" "${bootstrapDir}"`,
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
