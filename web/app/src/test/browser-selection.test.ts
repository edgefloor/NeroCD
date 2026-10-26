// @vitest-environment node
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { basename, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const app = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const cli = resolve(app, "node_modules/@playwright/test/cli.js");
const guarded = ["hosting-builds.spec.ts", "hosting-deployments.spec.ts", "hosting-proxy.spec.ts"];

function listTests(files: string[] = [], fixture?: string) {
  const env = { ...process.env };
  delete env.NEROCD_HOSTING_UI_FIXTURE_DIR;
  delete env.PLAYWRIGHT_BASE_URL;
  delete env.NEROCD_BROWSER_SMOKE_PORT;
  if (fixture !== undefined) env.NEROCD_HOSTING_UI_FIXTURE_DIR = fixture;
  return spawnSync(process.execPath, [cli, "test", "--config", "playwright.config.ts", "--project", "chromium", "--no-deps", "--list", "--reporter=json", ...files], {
    cwd: app,
    env,
    encoding: "utf8",
    timeout: 20_000,
  });
}

type Suite = { suites?: Suite[]; specs?: { file: string }[] };
function selectedFiles(output: string): string[] {
  const report = JSON.parse(output) as { suites: Suite[] };
  const files = new Set<string>();
  const visit = (suite: Suite) => {
    for (const spec of suite.specs ?? []) files.add(basename(spec.file));
    for (const child of suite.suites ?? []) visit(child);
  };
  for (const suite of report.suites) visit(suite);
  return [...files].sort();
}

describe("browser workflow selection", () => {
  it("selects generic smoke coverage without prepared physical workflows", () => {
    const result = listTests();
    expect(result.error).toBeUndefined();
    expect(result.status, result.stderr).toBe(0);
    const files = selectedFiles(result.stdout);
    expect(files).toContain("operator-workflow.spec.ts");
    expect(files).toContain("database-backups.spec.ts");
    expect(files).toContain("hosting-git-resources.spec.ts");
    for (const file of guarded) expect(files).not.toContain(file);
  }, 30_000);

  for (const [wrapper, fixture, expected] of [
    ["hosting-builds-fixture.sh", "/tmp/nerocd-hosting-ui.selection", ["hosting-builds.spec.ts", "hosting-deployments.spec.ts", "hosting-git-resources.spec.ts"]],
    ["hosting-deployment-fixture.sh", "/tmp/nerocd-hosting-ui.selection", ["hosting-deployments.spec.ts"]],
    ["hosting-proxy-fixture.sh", "/tmp/nerocd-proxy-ui.selection", ["hosting-proxy.spec.ts"]],
  ] as const) {
    it(`preserves the exact prepared selection of ${wrapper}`, () => {
      const source = readFileSync(resolve(app, "scripts", wrapper), "utf8");
      const match = source.match(/os\.execvp\("bun", (\[[^\n]+\])\)/);
      expect(match).not.toBeNull();
      const command = JSON.parse(match![1]) as string[];
      expect(command.slice(0, 5)).toEqual(["bun", "run", "--cwd", "web/app", "smoke:browser"]);
      expect(command.slice(5)).toEqual(expected);
      const result = listTests(command.slice(5), fixture);
      expect(result.error).toBeUndefined();
      expect(result.status, result.stderr).toBe(0);
      expect(selectedFiles(result.stdout)).toEqual([...expected].sort());
    }, 30_000);
  }

  for (const file of guarded) {
    it(`rejects an explicit ${file} request without prepared setup`, () => {
      const result = listTests([file]);
      expect(result.error).toBeUndefined();
      expect(result.status).not.toBe(0);
      expect(result.stdout + result.stderr).toContain("No tests found");
    }, 30_000);
  }

  for (const fixture of ["", "/tmp/unrelated", "/tmp/nerocd-hosting-ui.selection/child"]) {
    it(`rejects invalid prepared setup ${JSON.stringify(fixture)}`, () => {
      const result = listTests([], fixture);
      expect(result.error).toBeUndefined();
      expect(result.status).not.toBe(0);
      expect(result.stdout + result.stderr).toContain("must identify a prepared hosting or proxy fixture");
    }, 30_000);
  }
});
