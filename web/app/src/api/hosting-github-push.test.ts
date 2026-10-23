import { afterEach, expect, test, vi } from "vitest";
import { ApiError } from "./errors";
import { automaticDeploymentFromOptions, automaticDeploymentMatchesOptions, type AutomaticDeploymentOptions, type AutomaticDeploymentConfiguration, withGitHubPushDeadline, executeGitHubPushDecision, getGitHubPushPolicy, githubPushError, pushReason, listGitHubPushDeliveries, verificationCurrent, type GitHubPushPolicy, type PushDecision } from "./hosting-github-push";
const id = "11111111-1111-4111-8111-111111111111";
const policy: GitHubPushPolicy = { can_manage: true, id, resource_id: "resource/one", project_id: "project", generation: 2, resource_generation: 3, source_generation: 4, enabled: false, status: "verified", reason: "verified", repository_id: 1, repository: "owner/repo", branch: "main", verified_at: new Date().toISOString(), desired_commit: "", last_build_id: "", callback_url: "https://controller.test/hook", provider_read_configured: false };
const delivery = { id, policy_id: id, policy_generation: 2, state: "retry_wait", reason: "provider_unavailable", after_commit: "a", verified_commit: "", build_id: "", attempts: 1, received_at: "", expires_at: "" };
const json = (body: unknown) => new Response(JSON.stringify(body));
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });
test("all commands whitelist references and preserve exact CAS UUID bodies with CSRF/no-store", async () => {
 const fetch = vi.fn(async (_url: string, options: RequestInit) => json(JSON.parse(options.body as string).delivery_id ? delivery : policy)); vi.stubGlobal("fetch", fetch);
 const signal = new AbortController().signal;
 const commands: PushDecision[] = [
  { action: "configure", body: { expected_policy_generation: 2, expected_resource_generation: 3, expected_source_generation: 4, request_id: id, configuration: { automation_actor_id: "actor", webhook_secret: { id: "webhook", version: 1 }, provider_read_secret: { id: "provider", version: 2 } }, reset_branch_baseline: true } },
  { action: "verify", body: { expected_policy_generation: 2, request_id: id } },
  { action: "enabled", body: { expected_policy_generation: 2, request_id: id, enabled: true } },
  { action: "retry", body: { expected_policy_generation: 2, request_id: id, delivery_id: id } },
 ];
 for (const command of commands) { Object.assign(command.body, { token: "forbidden" }); await executeGitHubPushDecision("resource/one", command, { signal }); await executeGitHubPushDecision("resource/one", command, { signal }); }
 for (let i = 0; i < fetch.mock.calls.length; i += 2) { const [url, options] = fetch.mock.calls[i]!; expect(url).toMatch(/^\/api\/v1\/resources\/resource%2Fone\/github-push-policy/); expect(options).toMatchObject({ method: i === 0 ? "PUT" : "POST", cache: "no-store", credentials: "include", signal, headers: { "X-NeroCD-CSRF": "1" } }); expect(options.body).toBe(fetch.mock.calls[i + 1]![1].body); expect(options.body).not.toContain("forbidden"); }
});
test("policy and delivery metadata discard extra fields and hide owner references from viewers", async () => {
 vi.stubGlobal("fetch", vi.fn(async () => json({ ...policy, can_manage: false, owner_configuration: { automation_actor_id: "hidden", webhook_secret: { id: "secret", version: 1 } }, provider_response: "private diagnostic", value: "plaintext" })));
 expect(JSON.stringify(await getGitHubPushPolicy("resource/one"))).not.toMatch(/owner_configuration|plaintext|diagnostic|hidden/);
 const fetch = vi.fn(async () => json({ items: [{ ...delivery, raw_provider: "private" }], next_cursor: id, has_more: true })); vi.stubGlobal("fetch", fetch);
 expect(JSON.stringify(await listGitHubPushDeliveries("resource/one"))).not.toContain("private"); expect(fetch.mock.calls[0]![0]).toBe("/api/v1/resources/resource%2Fone/github-push-policy/deliveries?limit=20");
 await expect(listGitHubPushDeliveries("resource/one", id)).rejects.toThrow("Invalid push history");
 await expect(listGitHubPushDeliveries("resource/one", "arbitrary")).rejects.toThrow("cursor");
});
test("cross-resource policy and invalid enum responses fail closed", async () => {
 for (const change of [{ resource_id: "other" }, { status: "raw provider diagnostics" }, { reason: "unknown" }]) { vi.stubGlobal("fetch", vi.fn(async () => json({ ...policy, ...change }))); await expect(getGitHubPushPolicy("resource/one")).rejects.toThrow(); }
});
test("verification is current for at most60 seconds and errors never echo raw diagnostics", () => {
 const now = Date.now(); expect(verificationCurrent({ ...policy, verified_at: new Date(now - 60_000).toISOString() }, now)).toBe(true); expect(verificationCurrent({ ...policy, verified_at: new Date(now - 60_001).toISOString() }, now)).toBe(false); expect(verificationCurrent({ ...policy, status: "blocked" }, now)).toBe(false);
 for (const status of [400, 401, 403, 404, 409, 413, 503]) expect(githubPushError(new ApiError(status, "raw provider/token diagnostic", null))).not.toMatch(/raw provider|token diagnostic/);
 expect(githubPushError(new Error("raw provider/token diagnostic"))).toMatch(/identical request/);
 expect(pushReason.attempt_expired).toMatch(/current enabled policy.*authorized owner.*new bounded recovery window/); expect(pushReason.blocked_exhausted).toMatch(/current enabled policy.*authorized owner.*new bounded recovery window/);
});

test.each(["timeout", "parent abort"])("deadline settles independently and retires before abort: %s", async (kind) => {
 vi.useFakeTimers();
 try {
  const parent = new AbortController(), order: string[] = []; let resolve!: (value: string) => void;
  const result = withGitHubPushDeadline(parent.signal, 5000, (signal) => { signal.addEventListener("abort", () => order.push("abort")); return new Promise<string>((done) => { resolve = done; }); }, () => order.push("retired")).catch((cause: unknown) => cause);
  if (kind === "timeout") await vi.advanceTimersByTimeAsync(5000); else parent.abort();
  expect(await result).toBeInstanceOf(Error); expect(order).toEqual(["retired", "abort"]); resolve("late"); await vi.advanceTimersByTimeAsync(0); expect(await result).toBeInstanceOf(Error); expect(vi.getTimerCount()).toBe(0);
 } finally { vi.useRealTimers(); }
});


const automatic: AutomaticDeploymentConfiguration = { mode: "automatic_unprotected", expected_healthy_operation_id: "hop_manual", expected_loaded_proxy_operation_id: "pop_loaded", expected_loaded_generation: 8, expected_runtime_configuration_generation: 2, container_port: 8080, readiness: { path: "/ready?full=1", timeout_seconds: 10 }, routes: [{ route_id: "route_b", expected_version: 3, expected_selected_release_operation_id: "hop_manual" }, { route_id: "route_a", expected_version: 2, expected_selected_release_operation_id: "hop_manual" }] };
const automaticOptions: AutomaticDeploymentOptions = { available: true, reason: "eligible", baseline: { healthy_operation_id: "hop_manual", loaded_proxy_operation_id: "pop_loaded", loaded_generation: 8, runtime_configuration_generation: 2, container_port: 8080, readiness: automatic.readiness, manual_epoch: 4, routes: automatic.routes.map((route) => ({ route_id: route.route_id, version: route.expected_version, selected_release_operation_id: route.expected_selected_release_operation_id, hostname: `${route.route_id.replace("_", "-")}.example.test`, path_prefix: "/app" })) } };
const automaticStatus = { mode: "automatic_unprotected", state: "waiting_switch", reason: "switch_pending", manual_epoch: 4, desired_commit: "a".repeat(40), build_id: "build_one", deployment_operation_id: "auto-deploy-one", switch_operation_id: "auto-switch-one", attempts: 3, observed_loaded_generation: 0 };

test("GET reconstructs owner automatic options and viewer-safe status without secret payloads", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => json({ ...policy, owner_configuration: { automation_actor_id: "actor", webhook_secret: { id: "webhook", version: 1 }, automatic_deployment: { ...automatic, raw_value: "never-pass" } }, owner_automatic_deployment_options: { ...automaticOptions, raw_value: "never-pass" }, automatic_deployment: { ...automaticStatus, raw_value: "never-pass" } })));
  const result = await getGitHubPushPolicy("resource/one");
  expect(result.owner_configuration?.automatic_deployment?.routes.map((route) => route.route_id)).toEqual(["route_a", "route_b"]);
  expect(result.owner_automatic_deployment_options?.baseline?.readiness).toEqual(automatic.readiness);
  expect(result.automatic_deployment).toEqual(automaticStatus);
  expect(JSON.stringify(result)).not.toContain("never-pass");
  vi.stubGlobal("fetch", vi.fn(async () => json({ ...policy, can_manage: false, owner_configuration: { automatic_deployment: automatic }, owner_automatic_deployment_options: automaticOptions, automatic_deployment: automaticStatus })));
  const viewer = await getGitHubPushPolicy("resource/one");
  expect(viewer.owner_configuration).toBeUndefined(); expect(viewer.owner_automatic_deployment_options).toBeUndefined(); expect(viewer.automatic_deployment).toEqual(automaticStatus);
});

test("automatic configure whitelists a sorted exact baseline and mutation receipts never supply fresh review options", async () => {
  const fetch = vi.fn(async () => json({ ...policy, owner_configuration: { automation_actor_id: "actor", webhook_secret: { id: "webhook", version: 1 }, automatic_deployment: automatic }, owner_automatic_deployment_options: automaticOptions })); vi.stubGlobal("fetch", fetch);
  const decision: PushDecision = { action: "configure", body: { expected_policy_generation: 2, expected_resource_generation: 3, expected_source_generation: 4, request_id: id, configuration: { automation_actor_id: "actor", webhook_secret: { id: "webhook", version: 1 }, automatic_deployment: automatic }, reset_branch_baseline: true } };
  const result = await executeGitHubPushDecision("resource/one", decision); await executeGitHubPushDecision("resource/one", decision);
  expect(fetch.mock.calls[0]![1].body).toBe(fetch.mock.calls[1]![1].body);
  const body = JSON.parse(fetch.mock.calls[0]![1].body as string);
  expect(body.configuration.automatic_deployment).toEqual({ ...automatic, routes: [...automatic.routes].reverse() });
  expect((result as GitHubPushPolicy).owner_automatic_deployment_options).toBeUndefined();
  expect((result as GitHubPushPolicy).owner_configuration?.automatic_deployment).toBeTruthy();
});

test.each([
  { mode: "automatic_protected" }, { expected_loaded_generation: 0 }, { expected_runtime_configuration_generation: -1 }, { container_port: 65536 }, { readiness: { path: "//external.invalid", timeout_seconds: 5 } }, { readiness: { path: "/%0a", timeout_seconds: 5 } }, { readiness: { path: "/", timeout_seconds: 121 } }, { routes: [] }, { routes: [automatic.routes[0], automatic.routes[0]] }, { routes: [{ ...automatic.routes[0], expected_version: 0 }] },
])("malformed automatic configuration cannot be sent or retained: %j", async (change) => {
  const fetch = vi.fn(async () => json({ ...policy, owner_configuration: { automation_actor_id: "actor", webhook_secret: { id: "webhook", version: 1 }, automatic_deployment: { ...automatic, ...change } } })); vi.stubGlobal("fetch", fetch);
  await expect(getGitHubPushPolicy("resource/one")).rejects.toThrow("malformed");
  const decision = { action: "configure", body: { expected_policy_generation: 2, expected_resource_generation: 3, expected_source_generation: 4, request_id: id, configuration: { automation_actor_id: "actor", webhook_secret: { id: "webhook", version: 1 }, automatic_deployment: { ...automatic, ...change } }, reset_branch_baseline: true } } as PushDecision;
  await expect(executeGitHubPushDecision("resource/one", decision)).rejects.toThrow("malformed");
  expect(fetch).toHaveBeenCalledTimes(1);
});

test.each([
  { available: true, reason: "unknown", baseline: automaticOptions.baseline }, { available: false, reason: "eligible" }, { available: true, reason: "eligible" }, { available: false, reason: "restore_hold", baseline: automaticOptions.baseline }, { ...automaticOptions, baseline: { ...automaticOptions.baseline, routes: [] } }, { ...automaticOptions, baseline: { ...automaticOptions.baseline, routes: [automaticOptions.baseline!.routes[0], automaticOptions.baseline!.routes[0]] } },
])("automatic owner options fail closed when availability or baseline is invalid: %j", async (options) => {
  vi.stubGlobal("fetch", vi.fn(async () => json({ ...policy, owner_automatic_deployment_options: options })));
  await expect(getGitHubPushPolicy("resource/one")).rejects.toThrow("malformed");
});

test.each([{ state: "healthy_now" }, { attempts: 33 }, { desired_commit: "unverified" }, { reason: "raw provider response" }, { switch_operation_id: "secret\nvalue" }, { expires_at: "yesterday" }])("invalid automatic summary fields fail closed: %j", async (change) => {
  vi.stubGlobal("fetch", vi.fn(async () => json({ ...policy, automatic_deployment: { ...automaticStatus, ...change } })));
  await expect(getGitHubPushPolicy("resource/one")).rejects.toThrow("malformed");
});

test("only unique existing routes produce a canonical current baseline and drift cannot be substituted", () => {
  const current = automaticDeploymentFromOptions(automaticOptions, ["route_b", "route_a"]);
  expect(current?.routes.map((route) => route.route_id)).toEqual(["route_a", "route_b"]);
  expect(automaticDeploymentMatchesOptions(automatic, automaticOptions)).toBe(true);
  expect(automaticDeploymentMatchesOptions(automatic, { ...automaticOptions, baseline: { ...automaticOptions.baseline!, loaded_generation: 9 } })).toBe(false);
  expect(automaticDeploymentFromOptions(automaticOptions, ["route_a", "route_a"])).toBeUndefined();
  expect(automaticDeploymentFromOptions(automaticOptions, ["foreign"])).toBeUndefined();
  expect(automaticDeploymentFromOptions({ available: false, reason: "restore_hold" }, ["route_a"])).toBeUndefined();
});


test.each([{ hostname: "bad..example.test" }, { hostname: "*.example.test" }, { path_prefix: "/a/../b" }, { path_prefix: "/a//b" }, { path_prefix: "/dashboard" }, { path_prefix: "/encoded%2fpath" }])("owner route labels require the canonical backend hostname and literal path contract: %j", async (change) => {
  const options = { ...automaticOptions, baseline: { ...automaticOptions.baseline!, routes: [{ ...automaticOptions.baseline!.routes[0], ...change }] } };
  vi.stubGlobal("fetch", vi.fn(async () => json({ ...policy, owner_automatic_deployment_options: options })));
  await expect(getGitHubPushPolicy("resource/one")).rejects.toThrow("malformed");
});
