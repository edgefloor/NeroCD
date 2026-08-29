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
        command: `cd ../.. && set -eu; runtime_dir="${bootstrapDir}"; database="nerocd_browser_${port}"; container="nerocd-browser-postgres-${port}"; umask 077; mkdir -p "$runtime_dir"; email="browser-$(od -An -N12 -tx1 /dev/urandom | tr -d '[:space:]')@example.invalid"; password=$(od -An -N32 -tx1 /dev/urandom | tr -d '[:space:]'); printf %s "$password" > "$runtime_dir/password"; printf '%s\n%s\n' "$email" "$password" > "$runtime_dir/credentials"; cleanup(){ docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf -- "$runtime_dir"; }; trap cleanup EXIT HUP INT TERM; docker run -d --rm --name "$container" -e POSTGRES_DB="$database" -e POSTGRES_USER=nerocd -e POSTGRES_PASSWORD=nerocd_browser -p 127.0.0.1::5432 postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94 >/dev/null; for _ in $(seq 1 30); do docker exec "$container" pg_isready -U nerocd -d "$database" >/dev/null 2>&1 && break; sleep 1; done; docker exec "$container" pg_isready -U nerocd -d "$database" >/dev/null; database_port=$(docker port "$container" 5432/tcp | tail -1 | sed 's/.*://'); database_url="postgres://nerocd:nerocd_browser@127.0.0.1:\${database_port}/\${database}?sslmode=disable"; printf %s "$database_url" > "$runtime_dir/database-url"; NEROCD_DATABASE_URL="$database_url" GOCACHE=/private/tmp/nerocd-gocache go run ./cmd/nerocd migrate; NEROCD_DATABASE_URL="$database_url" NEROCD_COOKIE_SECURE=false NEROCD_PUBLIC_ORIGIN='${baseURL}' GOCACHE=/private/tmp/nerocd-gocache go run ./cmd/nerocd server --addr 127.0.0.1:${port}`,
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
