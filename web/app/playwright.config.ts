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

// These workflows require the owned images, runner endpoint, and other
// prerequisites prepared by their existing hosting-*-fixture.sh wrappers.
const fixtureDirectory = process.env.NEROCD_HOSTING_UI_FIXTURE_DIR;
if (fixtureDirectory !== undefined && !/^\/tmp\/nerocd-(?:hosting|proxy)-ui\.[A-Za-z0-9]+$/.test(fixtureDirectory)) {
  throw new Error("NEROCD_HOSTING_UI_FIXTURE_DIR must identify a prepared hosting or proxy fixture");
}
const preparedFixtureTests = [
  "hosting-builds.spec.ts",
  "hosting-deployments.spec.ts",
  "hosting-proxy.spec.ts",
];

export default defineConfig({
  testDir: "./tests/browser",
  metadata: {
    NEROCD_BROWSER_RUN_ID: browserRunID,
  },
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
        // Startup includes PostgreSQL, OIDC setup, and a fresh Go binary build.
        timeout: 120_000,
        gracefulShutdown: { signal: "SIGTERM", timeout: 5_000 },
      },
  projects: [
    {
      name: "oidc-setup",
      testMatch: "oidc.spec.ts",
      use: { ...devices["Desktop Chrome"] },
    },
    {
      name: "chromium",
      dependencies: ["oidc-setup"],
      testIgnore: ["oidc.spec.ts", ...(fixtureDirectory === undefined ? preparedFixtureTests : [])],
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
