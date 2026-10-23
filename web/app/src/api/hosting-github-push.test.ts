import { afterEach, expect, test, vi } from "vitest";
import { ApiError } from "./errors";
import { withGitHubPushDeadline, executeGitHubPushDecision, getGitHubPushPolicy, githubPushError, pushReason, listGitHubPushDeliveries, verificationCurrent, type GitHubPushPolicy, type PushDecision } from "./hosting-github-push";
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
