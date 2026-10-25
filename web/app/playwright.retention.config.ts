import { defineConfig, devices } from "@playwright/test";
export default defineConfig({
  testDir: "./tests/retention",
  outputDir: "./node_modules/.cache/retention-browser-results",
  fullyParallel: false,
  workers: 1,
  use: {
    ...devices["Desktop Chrome"],
    baseURL: "http://127.0.0.1:18375",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "bunx vite preview --config retention-preview.vite.config.ts",
    url: "http://127.0.0.1:18375/tests/retention/index.html",
    reuseExistingServer: false,
    timeout: 15000,
    gracefulShutdown: { signal: "SIGTERM", timeout: 3000 },
  },
});
