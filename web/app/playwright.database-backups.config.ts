import { defineConfig, devices } from "@playwright/test";
const port = Number(process.env.NEROCD_BACKUP_BROWSER_PORT ?? "27857");
if (
  !Number.isSafeInteger(port) ||
  port < 1024 ||
  port > 65535 ||
  [18081, 33085].includes(port)
)
  throw new Error("invalid owned backup browser port");
export default defineConfig({
  testDir: "./tests/browser",
  testMatch: "database-backups.spec.ts",
  timeout: 60000,
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: "retain-on-failure",
    ...devices["Desktop Chrome"],
  },
  webServer: {
    command: `bunx vite preview --host 127.0.0.1 --port ${port} --strictPort`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: false,
    timeout: 15000,
    gracefulShutdown: { signal: "SIGTERM", timeout: 5000 },
  },
});
