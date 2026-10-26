import { defineConfig, devices } from "@playwright/test";
import { fileURLToPath } from "node:url";
const app = fileURLToPath(new URL("../..", import.meta.url));
const port = process.env.NEROCD_NOTIFICATION_UI_PORT ?? "19273";
if (!/^\d{4,5}$/.test(port) || Number(port) > 65535)
  throw new Error("Invalid local UI test port");
export default defineConfig({
  testDir: ".",
  testMatch: "*.spec.ts",
  timeout: 30_000,
  workers: 1,
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    ...devices["Desktop Chrome"],
    trace: "off",
    screenshot: "off",
    video: "off",
  },
  webServer: {
    command: `bun run --cwd '${app}' preview --port ${port} --strictPort`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: false,
  },
});
