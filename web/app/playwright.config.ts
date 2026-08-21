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
        command: `cd ../.. && set -eu; runtime_dir="${bootstrapDir}"; umask 077; mkdir -p "$runtime_dir"; email="browser-$(od -An -N12 -tx1 /dev/urandom | tr -d '[:space:]')@example.invalid"; password=$(od -An -N32 -tx1 /dev/urandom | tr -d '[:space:]'); printf %s "$password" > "$runtime_dir/password"; printf '%s\n%s\n' "$email" "$password" > "$runtime_dir/credentials"; cleanup(){ rm -rf -- "$runtime_dir"; }; trap cleanup EXIT HUP INT TERM; NEROCD_COOKIE_SECURE=false NEROCD_PUBLIC_ORIGIN='${baseURL}' NEROCD_DEV_MEMORY=true NEROCD_DEV_BOOTSTRAP_EMAIL="$email" NEROCD_DEV_BOOTSTRAP_PASSWORD_FILE="$runtime_dir/password" GOCACHE=/private/tmp/nerocd-gocache go run ./cmd/nerocd server --addr 127.0.0.1:${port}`,
        url: baseURL,
        reuseExistingServer: false,
        timeout: 30_000,
      },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
