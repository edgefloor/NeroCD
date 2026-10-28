import { defineConfig } from "@playwright/test";
import base from "./playwright.config";

const pin = process.env.NEROCD_NOTIFICATION_BROWSER_SPKI;
const origin = process.env.PLAYWRIGHT_BASE_URL;
const fixtureOrigin = /^https:\/\/127\.0\.0\.1:([1-9][0-9]{3,4})$/.exec(origin ?? "");
if (!pin || !/^[A-Za-z0-9+/]{43}=$/.test(pin) || Buffer.from(pin, "base64").toString("base64") !== pin || !fixtureOrigin || Number(fixtureOrigin[1]) > 65535) {
  throw new Error("exact notification fixture TLS identity required");
}

export default defineConfig({
  ...base,
  testMatch: "backup-notifications-live.spec.ts",
  use: {
    ...base.use,
    baseURL: origin,
    ignoreHTTPSErrors: false,
    launchOptions: { args: [`--ignore-certificate-errors-spki-list=${pin}`] },
    trace: "off",
    screenshot: "off",
    video: "off",
  },
  webServer: undefined,
});
