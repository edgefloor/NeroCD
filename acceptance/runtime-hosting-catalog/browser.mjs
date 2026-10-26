// Connected browser against the candidate Go-served UI; no route interception.
import { createRequire } from "node:module";
import { resolve } from "node:path";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
const require = createRequire(resolve("web/app/package.json"));
const { chromium, expect, request } = require("@playwright/test");
const origin = process.env.NEROCD_CATALOG_BROWSER_ORIGIN;
const key = process.env.NEROCD_CATALOG_BROWSER_KEY;
const evidence = process.env.NEROCD_CATALOG_HOST_EVIDENCE;
if (
  !/^https:\/\/127\.0\.0\.1:[1-9][0-9]{3,4}$/.test(origin ?? "") ||
  !/^[a-f0-9]{64}$/.test(key ?? "") ||
  !evidence?.startsWith("/")
)
  throw Error("exact private browser profile required");
let api,
  browser,
  context,
  viewerContext,
  page,
  stopped = false;
const closing = new Map(),
  failures = new Set();
async function closeOwned() {
  for (const [name, value, method] of [
    ["viewer", viewerContext, "close"],
    ["context", context, "close"],
    ["browser", browser, "close"],
    ["api", api, "dispose"],
  ]) {
    if (!value) continue;
    if (!closing.has(name))
      closing.set(
        name,
        Promise.resolve().then(() => value[method]()),
      );
    const [r] = await Promise.allSettled([closing.get(name)]);
    if (r.status === "rejected") failures.add(name);
  }
  return failures.size === 0;
}
function stop(code) {
  if (stopped) return;
  stopped = true;
  process.exitCode = code;
  void closeOwned();
}
const interrupt = () => stop(130),
  terminate = () => stop(143);
process.once("SIGINT", interrupt);
process.once("SIGTERM", terminate);
const deadline = Date.now() + 20 * 60 * 1000,
  timer = setTimeout(() => stop(1), 20 * 60 * 1000);
timer.unref();
const safe = { status: "failed", intercepted_requests: false };
let phase = "startup";
async function state(expected) {
  phase = expected;
  while (Date.now() < deadline && !stopped) {
    try {
      const r = await api.get("/__catalog_fixture/state");
      if (r.status() === 200) {
        const v = await r.json();
        if (v.phase === expected) return v;
      }
    } catch {}
    await new Promise((r) => setTimeout(r, 250));
  }
  throw Error("fixture deadline");
}
async function report(action, id) {
  const r = await api.post("/__catalog_fixture/report", {
    data: { action, id },
  });
  if (r.status() !== 204) throw Error("fixture report rejected");
}
async function signIn(p, email, password) {
  await p.goto("/sign-in");
  await p.getByLabel("Email", { exact: true }).fill(email);
  await p.getByLabel("Password", { exact: true }).fill(password);
  await p.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    p.getByRole("button", { name: "Account", exact: true }),
  ).toBeVisible();
}
async function hosting(p) {
  await p.goto("/projects");
  const h = p.getByRole("region", { name: "Project hosting", exact: true });
  await h
    .getByLabel("Hosting project", { exact: true })
    .selectOption("proj_platform");
  await h
    .getByRole("button", { name: "storage runtime fixture", exact: true })
    .click();
  return h;
}
const response = (p, path) =>
  p.waitForResponse(
    (r) =>
      r.request().method() === "POST" && new URL(r.url()).pathname === path,
  );
try {
  api = await request.newContext({
    baseURL: origin,
    ignoreHTTPSErrors: true,
    extraHTTPHeaders: { "X-NeroCD-Fixture-Key": key },
    timeout: 10000,
  });
  browser = await chromium.launch({ headless: true });
  context = await browser.newContext({
    baseURL: origin,
    ignoreHTTPSErrors: true,
    viewport: { width: 1280, height: 900 },
  });
  page = await context.newPage();
  page.setDefaultTimeout(30000);
  const initial = await state("install");
  const manifest = JSON.parse(
    await readFile(resolve(evidence, "browser-ui-manifest.json"), "utf8"),
  );
  if (
    manifest.schema !== "nerocd.ui-assets/v1" ||
    !manifest.files["index.html"]
  )
    throw Error("candidate manifest absent");
  for (const [path, sha] of Object.entries(manifest.files)) {
    if (path === ".gitkeep") continue;
    const r = await api.get("/" + path);
    if (
      r.status() !== 200 ||
      createHash("sha256")
        .update(await r.body())
        .digest("hex") !== sha
    )
      throw Error("candidate asset mismatch");
  }
  safe.served_assets_match_manifest = true;
  await signIn(page, "admin@example.local", "admin");
  let h = await hosting(page);
  const catalog = h.getByRole("region", {
    name: "Hosting catalog",
    exact: true,
  });
  await expect(catalog).toBeVisible();
  await catalog
    .getByLabel("Catalog application", { exact: true })
    .selectOption("linkding");
  await catalog
    .getByLabel("Installation name", { exact: true })
    .fill("Linkding acceptance");
  await catalog
    .getByLabel("Catalog destination", { exact: true })
    .selectOption(initial.id);
  for (const width of [1280, 320, 390, 430]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(
      catalog.getByRole("button", { name: "Install application", exact: true }),
    ).toBeVisible();
    if (
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      )
    )
      throw Error("responsive overflow");
  }
  safe.responsive_widths = [1280, 320, 390, 430];
  await page.setViewportSize({ width: 1280, height: 900 });
  const firstWait = response(page, "/api/v1/hosting-catalog/installations");
  await catalog
    .getByRole("button", { name: "Install application", exact: true })
    .click();
  const first = await firstWait;
  if (first.status() !== 503) throw Error("committed response fault missing");
  const original = first.request().postData();
  await expect(catalog.getByRole("alert")).toContainText(
    "Installation unconfirmed",
  );
  await expect(
    catalog.getByLabel("Installation name", { exact: true }),
  ).toBeDisabled();
  const retryWait = response(page, "/api/v1/hosting-catalog/installations");
  await catalog
    .getByRole("button", { name: "Retry same installation", exact: true })
    .click();
  const retry = await retryWait;
  if (retry.status() !== 201 || retry.request().postData() !== original)
    throw Error("retry changed request");
  const installed = await retry.json();
  await expect(catalog).toContainText("Installed Linkding acceptance");
  await expect(catalog.locator("output")).toHaveCount(0);
  const revealWait = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      /\/managed-secrets\/[^/]+\/reveal$/.test(new URL(r.url()).pathname),
  );
  await catalog
    .getByRole("button", { name: "Reveal login credentials", exact: true })
    .click();
  if ((await revealWait).status() !== 200)
    throw Error("explicit reveal failed");
  await expect(catalog.locator("output")).toHaveCount(2);
  await catalog
    .getByRole("button", { name: "Hide credentials", exact: true })
    .click();
  await expect(catalog.locator("output")).toHaveCount(0);
  safe.catalog_install_identical_retry = true;
  safe.explicit_reveal_and_hide = true;
  await report("installed", installed.resource.id);
  await state("deploy");
  const application = h.getByRole("article", {
    name: "Linkding acceptance",
    exact: true,
  });
  const review = application.getByRole("region", {
    name: "Persistent upload deployment review",
    exact: true,
  });
  await expect(
    review.getByLabel("Container port", { exact: true }),
  ).toHaveValue("9090");
  await expect(
    review.getByLabel("Readiness path", { exact: true }),
  ).toHaveValue("/health");
  await review
    .getByLabel("Runner-local host port", { exact: true })
    .fill("20101");
  await review
    .getByRole("button", {
      name: "Review current storage deployment",
      exact: true,
    })
    .click();
  await review.getByRole("checkbox").check();
  const deployWait = response(
    page,
    `/api/v1/resources/${installed.resource.id}/deployments`,
  );
  await review
    .getByRole("button", {
      name: "Deploy reviewed image with uploads",
      exact: true,
    })
    .click();
  const deployed = await deployWait;
  if (deployed.status() !== 202) throw Error("browser deployment rejected");
  const operation = await deployed.json();
  await report("deployed", operation.id);
  safe.browser_deployment = true;
  await state("healthy");
  const deployments = application.getByRole("region", {
    name: "Image deployments for Linkding acceptance",
    exact: true,
  });
  await deployments
    .getByRole("button", { name: "Refresh deployment history", exact: true })
    .click();
  await deployments
    .getByRole("button", {
      name: `View operation ${operation.id}`,
      exact: true,
    })
    .click();
  const detail = deployments.getByRole("region", {
    name: "Deployment operation detail",
    exact: true,
  });
  await expect(detail).toContainText("Succeeded");
  await expect(
    detail.getByRole("region", {
      name: "Recorded deployment result",
      exact: true,
    }),
  ).toContainText("Readiness recorded at completion: passed");
  safe.healthy_resource_result = true;
  viewerContext = await browser.newContext({
    baseURL: origin,
    ignoreHTTPSErrors: true,
    viewport: { width: 390, height: 844 },
  });
  const viewer = await viewerContext.newPage();
  await signIn(viewer, "viewer@example.local", "viewer");
  const vh = await hosting(viewer);
  await expect(vh).toContainText(
    "Project owner access is required to install catalog applications.",
  );
  await expect(
    vh.getByRole("button", { name: "Install application", exact: true }),
  ).toHaveCount(0);
  await expect(
    vh.getByRole("article", { name: "Linkding acceptance", exact: true }),
  ).toBeVisible();
  const denial = await viewerContext.request.post(
    "/api/v1/hosting-catalog/installations",
    {
      headers: { "X-NeroCD-CSRF": "1", Origin: origin },
      data: JSON.parse(original),
    },
  );
  if (denial.status() !== 403) throw Error("viewer replay not denied");
  safe.current_viewer_denied_replay = true;
  await report("healthy", operation.id);
  await state("route");
  const routes = application.getByRole("region", {
    name: "Routes for Linkding acceptance",
    exact: true,
  });
  await routes
    .getByRole("button", { name: "Refresh resource routes", exact: true })
    .click();
  const routeForm = routes.getByRole("form", {
    name: "Configure route for Linkding acceptance",
    exact: true,
  });
  await routeForm
    .getByLabel("Route hostname", { exact: true })
    .fill("catalog-runtime.example.test");
  await routeForm
    .getByLabel("Expected settled healthy deployment", { exact: true })
    .selectOption(operation.id);
  const routeWait = response(
    page,
    `/api/v1/resources/${installed.resource.id}/routes`,
  );
  await routeForm
    .getByRole("button", { name: "Save desired route", exact: true })
    .click();
  const routed = await routeWait;
  if (routed.status() !== 202) throw Error("route request rejected");
  const route = await routed.json();
  safe.browser_route_selection = true;
  await report("routed", route.operation_id);
  await state("complete");
  await report("done", operation.id);
  safe.status = "passed";
} catch (error) {
  safe.failure = stopped ? "cancelled" : "browser_workflow_failed";
  safe.failed_phase = phase;
  safe.error_class = ["TimeoutError", "AssertionError", "TypeError"].includes(
    error?.name,
  )
    ? error.name
    : "Error";
  if (!process.exitCode) process.exitCode = 1;
} finally {
  clearTimeout(timer);
  if (!(await closeOwned())) {
    safe.status = "failed";
    safe.cleanup_failures = [...failures].sort();
    process.exitCode ||= 1;
  }
  if (stopped) safe.status = "failed";
  try {
    await writeFile(
      resolve(evidence, "catalog-browser-safe.json"),
      JSON.stringify(safe, null, 2) + "\n",
      { mode: 0o600 },
    );
  } catch {
    process.exitCode ||= 1;
  }
  process.removeListener("SIGINT", interrupt);
  process.removeListener("SIGTERM", terminate);
}
