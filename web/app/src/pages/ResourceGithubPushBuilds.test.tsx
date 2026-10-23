import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import type { Resource } from "@/api/hosting";
import type { AutomaticDeploymentOptions, AutomaticDeploymentConfiguration, GitHubPushPolicy } from "@/api/hosting-github-push";
import { ResourceGithubPushBuilds } from "./ResourceGithubPushBuilds";
const resource: Resource = { id: "resource", project_id: "project", project_environment_id: "environment", name: "Web", kind: "application", generation: 2, created_by: "user", created_at: "", updated_at: "", source: { kind: "git", git: { source_id: "source", generation: 3, repository_url: "https://github.com/owner/repo.git", requested_ref: "main", credential_kind: "public", settings: { strategy: "static" } } } };
const policy: GitHubPushPolicy = { id: "policy", can_manage: true, resource_id: "resource", project_id: "project", generation: 1, resource_generation: 2, source_generation: 3, enabled: false, status: "unverified", reason: "unverified", repository_id: 1, repository: "owner/repo", branch: "main", desired_commit: "", last_build_id: "", callback_url: "https://control.test/fixed-hook", provider_read_configured: false };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const page = (items: unknown[]) => ({ items, count: items.length, total: items.length, limit: 20, offset: 0 });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });
function setup(p = policy, r = resource, handler?: (url: string, options: RequestInit) => Response | Promise<Response> | undefined) {
 const fetch = vi.fn(async (url: string, options: RequestInit) => await handler?.(url, options) ?? (url.endsWith("/github-push-policy") ? json(p) : url.includes("/deliveries?") ? json({ items: [], next_cursor: "", has_more: false }) : url.includes("/service-accounts?") ? json(page([{ id: "actor", project_id: "project", name: "Build actor", status: "active", role: "maintainer" }, { id: "viewer", project_id: "project", name: "Viewer actor", status: "active", role: "viewer" }])) : url.includes("/managed-secrets?") ? json(page([{ id: "webhook", project_id: "project", name: "Signing secret", status: "active" }, { id: "provider", project_id: "project", name: "Provider secret", status: "active" }])) : url.includes("/versions?") ? json(page([{ secret_id: url.includes("webhook") ? "webhook" : "provider", version: 1 }])) : url.includes("/source-grants?") ? json(page([{ id: "grant", resource_id: "resource", source_id: "source", source_generation: 3, version: 1, kind: "https_basic", revoked: false }])) : json({}))); vi.stubGlobal("fetch", fetch);
 client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
 const view = render(<QueryClientProvider client={client}><ResourceGithubPushBuilds actor="actor-identity" resource={r} blocked={false} /></QueryClientProvider>); return { fetch, ...view };
}
async function references(provider = false) {
 await screen.findByRole("option", { name: /Build actor/ }); fireEvent.change(screen.getByLabelText("Automation service account"), { target: { value: "actor" } }); fireEvent.change(screen.getByLabelText("Webhook signing managed secret"), { target: { value: "webhook" } }); await waitFor(() => expect(screen.getByLabelText<HTMLSelectElement>("Webhook signing immutable version").disabled).toBe(false)); fireEvent.change(screen.getByLabelText("Webhook signing immutable version"), { target: { value: "1" } });
 if (provider) { fireEvent.change(screen.getByLabelText("GitHub provider read managed secret"), { target: { value: "provider" } }); await waitFor(() => expect(screen.getByLabelText<HTMLSelectElement>("GitHub provider read immutable version").disabled).toBe(false)); fireEvent.change(screen.getByLabelText("GitHub provider read immutable version"), { target: { value: "1" } }); }
 fireEvent.click(screen.getByLabelText(/I reviewed these exact references/));
}
test("viewer reads policy and history with no owner selectors or secret/account reads", async () => {
 const { fetch } = setup({ ...policy, can_manage: false }); await screen.findByText(/Maintainer build access does not grant policy management/); expect(screen.queryByRole("form")).toBeNull(); expect(fetch.mock.calls.some(([url]) => /managed-secrets|service-accounts|source-grants/.test(url))).toBe(false); expect(screen.getByText(/Automatic retries stop after 32 attempts or 24 hours and use their existing bounded deadline/)).toBeTruthy(); expect(screen.getByText(/Deployments remain manual/)).toBeTruthy();
});
test("configuration requires explicit reset acknowledgment and exact metadata references only", async () => {
 const { fetch } = setup(); await references(); const submit = screen.getByRole<HTMLButtonElement>("button", { name: "Save disabled push configuration" }); await waitFor(() => expect(submit.disabled).toBe(false)); expect(screen.queryByRole("option", { name: /Viewer actor/ })).toBeNull(); fireEvent.click(submit); await waitFor(() => expect(fetch.mock.calls.some(([, o]) => o.method === "PUT")).toBe(true)); const body = JSON.parse(fetch.mock.calls.find(([, o]) => o.method === "PUT")![1].body as string); expect(body).toMatchObject({ expected_policy_generation: 1, expected_resource_generation: 2, expected_source_generation: 3, reset_branch_baseline: true, configuration: { automation_actor_id: "actor", webhook_secret: { id: "webhook", version: 1 } } }); expect(body.request_id).toMatch(/^[0-9a-f-]{36}$/); expect(fetch.mock.calls.some(([url]) => /reveal|rotate|consume/.test(url))).toBe(false);
});
test("lost configure response retains exact UUID/body and freezes form until identical retry", async () => {
 let writes = 0; const { fetch } = setup(policy, resource, (_url, o) => o.method === "PUT" ? ++writes === 1 ? Promise.reject(new TypeError("lost")) : json({ ...policy, generation: 2, status: "disabled" }) : undefined); await references(); fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" })); const retry = await screen.findByRole("button", { name: "Retry identical push decision" }); await waitFor(() => expect((retry as HTMLButtonElement).disabled).toBe(false)); expect(screen.getByLabelText<HTMLSelectElement>("Automation service account").disabled).toBe(true); fireEvent.click(retry); await waitFor(() => expect(writes).toBe(2)); const calls = fetch.mock.calls.filter(([, o]) => o.method === "PUT"); expect(calls[0]![1].body).toBe(calls[1]![1].body);
});
test("409 refreshes metadata and requires explicit separate review without resubmission", async () => {
 let changed = false; const { fetch } = setup(policy, resource, (url, o) => o.method === "PUT" ? (changed = true, json({ error: "raw hidden" }, 409)) : changed && url.endsWith("/github-push-policy") ? json({ ...policy, generation: 4 }) : undefined); await references(); fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" })); await screen.findByText(/Current policy 4/); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical push decision" }).disabled).toBe(true); expect(fetch.mock.calls.filter(([, o]) => o.method === "PUT")).toHaveLength(1); fireEvent.click(screen.getByRole("button", { name: "Use reviewed policy for a separate decision" })); expect(screen.getByLabelText<HTMLInputElement>(/I reviewed these exact references/).checked).toBe(false); expect(screen.queryByText(/raw hidden/)).toBeNull();
});
test("private source requires distinct provider reference and eligible current grant", async () => {
 const privateResource = { ...resource, source: { kind: "git" as const, git: { ...(resource.source.kind === "git" ? resource.source.git : {}), credential_kind: "https_basic" as const } } } as Resource;
 setup(policy, privateResource); await references(); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save disabled push configuration" }).disabled).toBe(true); fireEvent.change(screen.getByLabelText("GitHub provider read managed secret"), { target: { value: "provider" } }); await waitFor(() => expect(screen.getByLabelText<HTMLSelectElement>("GitHub provider read immutable version").disabled).toBe(false)); fireEvent.change(screen.getByLabelText("GitHub provider read immutable version"), { target: { value: "1" } }); fireEvent.change(screen.getByLabelText("Push private source grant"), { target: { value: "grant" } }); fireEvent.click(screen.getByLabelText(/I reviewed these exact references/)); await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save disabled push configuration" }).disabled).toBe(false));
});
test("blocked verify result never enables and stale verification cannot enable", async () => {
 let current = policy;
 const { fetch } = setup(policy, resource, (url, o) => { if (url.endsWith("/verify") && o.method === "POST") { current = { ...policy, status: "blocked", reason: "provider_unavailable" }; return json(current); } return url.endsWith("/github-push-policy") ? json(current) : undefined; }); fireEvent.click(await screen.findByRole("button", { name: "Verify GitHub repository and branch" })); await screen.findByText("GitHub is unavailable. Retry verification later."); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Enable build on push" }).disabled).toBe(true); expect(fetch.mock.calls.filter(([, o]) => o.method === "POST")).toHaveLength(1);
});
test.each([401, 403, 404])("policy read %s never opens owner selectors", async (status) => {
 const { fetch } = setup(policy, resource, (url) => url.endsWith("/github-push-policy") ? json({}, status) : undefined); await screen.findByText(/Reload to verify current access/); expect(screen.queryByRole("form")).toBeNull(); expect(fetch.mock.calls.some(([url]) => /managed-secrets|service-accounts/.test(url))).toBe(false);
});
test("permission loss during a frozen write removes controls and ignores late success", async () => {
 let resolve!: (r: Response) => void; const { rerender } = setup(policy, resource, (_url, o) => o.method === "PUT" ? new Promise<Response>((done) => { resolve = done; }) : undefined); await references(); fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" })); await waitFor(() => expect(resolve).toBeTypeOf("function")); await act(async () => { client.setQueryData(["githubPushPolicy", "actor-identity", "project", "resource"], { ...policy, can_manage: false }); }); await act(async () => resolve(json({ ...policy, generation: 2 }))); expect(screen.queryByRole("form")).toBeNull(); rerender(<QueryClientProvider client={client}><ResourceGithubPushBuilds actor="different-actor" resource={resource} blocked /></QueryClientProvider>); expect(screen.queryByRole("form")).toBeNull();
});
test("unconfigured owner has no fabricated callback or recorded secret references", async () => {
 setup({ ...policy, id: "", generation: 0, status: "unconfigured" }); await screen.findByRole("form", { name: "Configure GitHub build on push" }); expect(screen.queryByLabelText("Server callback URL")).toBeNull(); expect(screen.queryByText(/Recorded configuration:/)).toBeNull(); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Verify GitHub repository and branch" }).disabled).toBe(true);
});
test.each([true, false])("enable requires recent current verification: %s", async (recent) => {
 const { fetch } = setup({ ...policy, status: "verified", reason: "verified", verified_at: new Date(Date.now() - (recent ? 1000 : 61_000)).toISOString() }); const enable = await screen.findByRole<HTMLButtonElement>("button", { name: "Enable build on push" }); expect(enable.disabled).toBe(!recent); if (recent) { fireEvent.click(enable); await waitFor(() => expect(fetch.mock.calls.some(([url, o]) => url.endsWith("/enabled") && o.method === "POST")).toBe(true)); expect(JSON.parse(fetch.mock.calls.find(([url]) => url.endsWith("/enabled"))![1].body as string)).toMatchObject({ expected_policy_generation: 1, enabled: true }); }
});
test.each(["retry_wait", "blocked_exhausted"])("retained %s delivery retry keeps exact ID/body after response loss", async (state) => {
 const delivery = { id: "11111111-1111-4111-8111-111111111111", policy_id: "policy", policy_generation: 1, state, reason: state === "blocked_exhausted" ? "attempt_expired" : "provider_unavailable", after_commit: "a", verified_commit: "", build_id: "", attempts: 2, received_at: "", expires_at: new Date(Date.now() + (state === "blocked_exhausted" ? -60_000 : 60_000)).toISOString() }; let writes = 0;
 const { fetch } = setup({ ...policy, enabled: true, status: "enabled" }, resource, (url, options) => url.includes("/deliveries?") ? json({ items: [delivery], next_cursor: "", has_more: false }) : url.endsWith("/retry") && options.method === "POST" ? ++writes === 1 ? Promise.reject(new Error("lost")) : json(delivery) : undefined);
 fireEvent.click(await screen.findByRole("button", { name: `Retry retained delivery ${delivery.id}` })); const retry = await screen.findByRole<HTMLButtonElement>("button", { name: "Retry identical delivery decision" }); expect(screen.getByText(/If accepted, this owner decision starts one new bounded recovery window/)).toBeTruthy(); expect(screen.getByText(/An identical retry returns the same window without extending it/)).toBeTruthy(); await waitFor(() => expect(retry.disabled).toBe(false)); fireEvent.click(retry); await waitFor(() => expect(writes).toBe(2)); const calls = fetch.mock.calls.filter(([url]) => url.endsWith("/retry")); expect(calls[0]![1].body).toBe(calls[1]![1].body); expect(JSON.parse(calls[0]![1].body as string)).toMatchObject({ delivery_id: delivery.id, expected_policy_generation: 1 });
});
test("metadata read error preserves review but disables configure; errors contain no provider output", async () => {
 setup(policy, resource, (url) => url.includes("/service-accounts?") ? json({ error: "private provider diagnostic" }, 503) : undefined); await screen.findByText(/Unable to verify current account/); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save disabled push configuration" }).disabled).toBe(true); expect(screen.queryByText(/private provider diagnostic/)).toBeNull();
});
test("mutating permission denial removes selectors and stops metadata reads", async () => {
 const { fetch } = setup(policy, resource, (_url, options) => options.method === "PUT" ? json({ error: "private provider diagnostic" }, 403) : undefined); await references(); fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" })); await screen.findByText(/Reload to verify current access/); expect(screen.queryByRole("form")).toBeNull(); const count = fetch.mock.calls.length; fireEvent.click(screen.getByRole("button", { name: "Refresh build-on-push metadata" })); await act(async () => {}); expect(fetch.mock.calls).toHaveLength(count);
});
test("refreshing revoked signing metadata clears selection and acknowledgment before any write", async () => {
 let revoked = false; const { fetch } = setup(policy, resource, (url) => url.includes("/managed-secrets?") && revoked ? json(page([{ id: "webhook", project_id: "project", name: "Signing secret", status: "revoked" }])) : undefined); await references(); await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save disabled push configuration" }).disabled).toBe(false)); revoked = true; fireEvent.click(screen.getByRole("button", { name: "Refresh build-on-push metadata" })); await waitFor(() => expect(screen.getByLabelText<HTMLSelectElement>("Webhook signing managed secret").value).toBe("")); expect(screen.getByLabelText<HTMLInputElement>(/I reviewed these exact references/).checked).toBe(false); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save disabled push configuration" }).disabled).toBe(true); expect(fetch.mock.calls.some(([, o]) => o.method === "PUT")).toBe(false);
});
test("503 preserves the exact command and pauses explicit retry for ten seconds without automatic writes", async () => {
 const { fetch } = setup(policy, resource, (_url, options) => options.method === "PUT" ? json({}, 503) : undefined); await references(); vi.useFakeTimers();
 try {
  fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" })); await act(async () => { await vi.advanceTimersByTimeAsync(0); });
  const retry = screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical push decision" }); expect(retry.disabled).toBe(true); const first = fetch.mock.calls.find(([, options]) => options.method === "PUT")![1].body;
  await act(async () => { await vi.advanceTimersByTimeAsync(9999); }); expect(retry.disabled).toBe(true); expect(fetch.mock.calls.filter(([, options]) => options.method === "PUT")).toHaveLength(1);
  await act(async () => { await vi.advanceTimersByTimeAsync(1); }); expect(retry.disabled).toBe(false); fireEvent.click(retry); await act(async () => { await vi.advanceTimersByTimeAsync(0); }); const writes = fetch.mock.calls.filter(([, options]) => options.method === "PUT"); expect(writes).toHaveLength(2); expect(writes[1]![1].body).toBe(first);
 } finally { vi.useRealTimers(); }
});
test("hung configure retires before transport settles and fences its late result from the exact retry", async () => {
 const pending: Array<{ resolve: (response: Response) => void; signal: AbortSignal }> = [];
 const { fetch } = setup(policy, resource, (_url, options) => options.method === "PUT" ? new Promise<Response>((resolve) => pending.push({ resolve, signal: options.signal as AbortSignal })) : undefined); await references(); vi.useFakeTimers();
 try {
  fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" })); await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
  const retry = screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical push decision" }); expect(retry.disabled).toBe(false); expect(pending[0]!.signal.aborted).toBe(true); expect(pending).toHaveLength(1);
  fireEvent.click(retry); await act(async () => { await vi.advanceTimersByTimeAsync(0); }); expect(pending).toHaveLength(2); expect(retry.disabled).toBe(true);
  await act(async () => { pending[0]!.resolve(json({ ...policy, generation: 99 })); await vi.advanceTimersByTimeAsync(0); }); expect(retry.disabled).toBe(true); expect(screen.getByText(/Retained configure request/)).toBeTruthy(); expect(screen.queryByText(/Current policy 99/)).toBeNull();
  await act(async () => { pending[1]!.resolve(json({ ...policy, generation: 2 })); await vi.advanceTimersByTimeAsync(0); }); expect(screen.queryByText(/Retained configure request/)).toBeNull(); const writes = fetch.mock.calls.filter(([, options]) => options.method === "PUT"); expect(writes).toHaveLength(2); expect(writes[0]![1].body).toBe(writes[1]![1].body);
 } finally { vi.useRealTimers(); }
});
test("hung initial policy read times out and manual refresh recovers despite an abort-ignoring old response", async () => {
 let resolve!: (r: Response) => void, reads = 0; vi.useFakeTimers(); setup(policy, resource, (url) => url.endsWith("/github-push-policy") && ++reads === 1 ? new Promise<Response>((done) => { resolve = done; }) : undefined);
 try {
  await act(async () => { await vi.advanceTimersByTimeAsync(5001); }); const refresh = screen.getByRole<HTMLButtonElement>("button", { name: "Refresh build-on-push metadata" }); expect(refresh.disabled).toBe(false); expect(screen.getByRole("alert").textContent).toMatch(/Unable to refresh/);
  fireEvent.click(refresh); await act(async () => { await vi.advanceTimersByTimeAsync(0); }); expect(screen.getByRole("form", { name: "Configure GitHub build on push" })).toBeTruthy();
  await act(async () => { resolve(json({ ...policy, can_manage: false })); await vi.advanceTimersByTimeAsync(0); }); expect(screen.getByRole("form", { name: "Configure GitHub build on push" })).toBeTruthy();
 } finally { vi.useRealTimers(); }
});
test("hung history read exposes manual recovery and cannot overwrite a recovered page", async () => {
 let resolve!: (r: Response) => void, reads = 0; vi.useFakeTimers(); setup(policy, resource, (url) => url.includes("/deliveries?") && ++reads === 1 ? new Promise<Response>((done) => { resolve = done; }) : undefined);
 try {
  await act(async () => { await vi.advanceTimersByTimeAsync(5001); }); const refresh = screen.getByRole<HTMLButtonElement>("button", { name: "Refresh build-on-push metadata" }); expect(refresh.disabled).toBe(false); expect(screen.getByRole("alert").textContent).toMatch(/Unable to refresh/); fireEvent.click(refresh); await act(async () => { await vi.advanceTimersByTimeAsync(0); }); expect(reads).toBe(2);
  await act(async () => { resolve(json({ items: [{ id: "11111111-1111-4111-8111-111111111111", policy_id: "policy", policy_generation: 1, state: "ignored", reason: "obsolete", after_commit: "stale-old-page", verified_commit: "", build_id: "", attempts: 1, received_at: "", expires_at: "" }], has_more: false, next_cursor: "" })); await vi.advanceTimersByTimeAsync(0); }); expect(screen.queryByText(/stale-old-page/)).toBeNull(); expect(screen.getByText("No push deliveries recorded.")).toBeTruthy();
 } finally { vi.useRealTimers(); }
});
test("verify preserves uncertainty only after its twenty-five second deadline", async () => {
 const pending: Array<(r: Response) => void> = []; const { fetch } = setup(policy, resource, (url, options) => url.endsWith("/verify") && options.method === "POST" ? new Promise<Response>((resolve) => pending.push(resolve)) : undefined); await screen.findByRole("form", { name: "Configure GitHub build on push" }); vi.useFakeTimers();
 try {
  fireEvent.click(screen.getByRole("button", { name: "Verify GitHub repository and branch" })); await act(async () => { await vi.advanceTimersByTimeAsync(24_999); }); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical push decision" }).disabled).toBe(true); expect(fetch.mock.calls.filter(([url]) => url.endsWith("/verify"))).toHaveLength(1);
  await act(async () => { await vi.advanceTimersByTimeAsync(1); }); const retry = screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical push decision" }); expect(retry.disabled).toBe(false); fireEvent.click(retry); await act(async () => { await vi.advanceTimersByTimeAsync(0); }); expect(pending).toHaveLength(2); const writes = fetch.mock.calls.filter(([url]) => url.endsWith("/verify")); expect(writes[0]![1].body).toBe(writes[1]![1].body);
 } finally { vi.useRealTimers(); }
});
test("pause releases an abort-ignoring mutation and exact retry remains available on resume", async () => {
 const pending: Array<{ resolve: (r: Response) => void; signal: AbortSignal }> = []; const { rerender, fetch } = setup(policy, resource, (_url, options) => options.method === "PUT" ? new Promise<Response>((resolve) => pending.push({ resolve, signal: options.signal as AbortSignal })) : undefined); await references(); fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" })); await waitFor(() => expect(pending).toHaveLength(1));
 rerender(<QueryClientProvider client={client}><ResourceGithubPushBuilds actor="actor-identity" resource={resource} blocked /></QueryClientProvider>); expect(pending[0]!.signal.aborted).toBe(true);
 rerender(<QueryClientProvider client={client}><ResourceGithubPushBuilds actor="actor-identity" resource={resource} blocked={false} /></QueryClientProvider>); const retry = await screen.findByRole<HTMLButtonElement>("button", { name: "Retry identical push decision" }); await waitFor(() => expect(retry.disabled).toBe(false)); fireEvent.click(retry); await waitFor(() => expect(pending).toHaveLength(2)); await act(async () => pending[0]!.resolve(json({ ...policy, generation: 99 }))); expect(retry.disabled).toBe(true); await act(async () => pending[1]!.resolve(json({ ...policy, generation: 2 }))); const writes = fetch.mock.calls.filter(([, options]) => options.method === "PUT"); expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
test.each(["actor", "resource"])("hung mutation cannot retain request state across %s changes", async (scope) => {
 let resolve!: (r: Response) => void, signal!: AbortSignal; const { rerender } = setup(policy, resource, (url, options) => options.method === "PUT" ? new Promise<Response>((done) => { resolve = done; signal = options.signal as AbortSignal; }) : url.includes("/other/") && url.endsWith("/github-push-policy") ? json({ ...policy, resource_id: "other" }) : undefined); await references(); fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" })); await waitFor(() => expect(resolve).toBeTypeOf("function"));
 rerender(<QueryClientProvider client={client}><ResourceGithubPushBuilds actor={scope === "actor" ? "new-actor" : "actor-identity"} resource={scope === "resource" ? { ...resource, id: "other" } : resource} blocked={false} /></QueryClientProvider>); expect(signal.aborted).toBe(true); await screen.findByRole("form", { name: "Configure GitHub build on push" }); await act(async () => resolve(json({ ...policy, generation: 99 }))); expect(screen.queryByText(/Retained configure request/)).toBeNull(); expect(screen.getByLabelText<HTMLSelectElement>("Automation service account").value).toBe("");
});
test("pending owner metadata is retired while current policy authority is being refreshed", async () => {
 let accountSignal!: AbortSignal, resolveAccount!: (r: Response) => void, resolvePolicy!: (r: Response) => void, policyReads = 0;
 setup(policy, resource, (url, options) => url.includes("/service-accounts?") ? new Promise<Response>((resolve) => { resolveAccount = resolve; accountSignal = options.signal as AbortSignal; }) : url.endsWith("/github-push-policy") && ++policyReads > 1 ? new Promise<Response>((resolve) => { resolvePolicy = resolve; }) : undefined);
 await waitFor(() => expect(resolveAccount).toBeTypeOf("function")); const refresh = screen.getByRole<HTMLButtonElement>("button", { name: "Refresh build-on-push metadata" }); await waitFor(() => expect(refresh.disabled).toBe(false)); fireEvent.click(refresh); await waitFor(() => expect(resolvePolicy).toBeTypeOf("function")); expect(accountSignal.aborted).toBe(true);
 await act(async () => resolveAccount(json(page([{ id: "late-owner", project_id: "project", name: "Late owner metadata", role: "owner", status: "active" }])))); expect(screen.queryByRole("option", { name: /Late owner metadata/ })).toBeNull(); await act(async () => resolvePolicy(json({ ...policy, can_manage: false }))); await waitFor(() => expect(screen.queryByRole("form")).toBeNull());
});

test.each(["old generation", "disabled", "viewer"])("expired delivery recovery remains unavailable for %s", async (denial) => {
 const delivery = { id: "11111111-1111-4111-8111-111111111111", policy_id: "policy", policy_generation: denial === "old generation" ? 0 : 1, state: "blocked_exhausted", reason: "attempt_expired", after_commit: "a", verified_commit: "", build_id: "", attempts: 32, received_at: "", expires_at: new Date(Date.now() - 60_000).toISOString() };
 const { fetch } = setup({ ...policy, enabled: denial !== "disabled", can_manage: denial !== "viewer" }, resource, (url) => url.includes("/deliveries?") ? json({ items: [delivery], next_cursor: "", has_more: false }) : undefined);
 await screen.findByText(`Delivery ${delivery.id} · blocked_exhausted · policy generation ${delivery.policy_generation}`); expect(screen.queryByRole("button", { name: `Retry retained delivery ${delivery.id}` })).toBeNull(); expect(fetch.mock.calls.some(([, options]) => options.method === "POST" || options.method === "PUT")).toBe(false);
});
test.each(["received", "verifying", "queued", "coalesced"])("uncertain receipt replay survives advanced delivery state %s", async (advanced) => {
 let state = "blocked_exhausted", writes = 0;
 const delivery = () => ({ id: "11111111-1111-4111-8111-111111111111", policy_id: "policy", policy_generation: 1, state, reason: state === "blocked_exhausted" ? "attempt_expired" : "build_queued", after_commit: "a", verified_commit: "b", build_id: "", attempts: 32, received_at: "", expires_at: new Date(Date.now() - 60_000).toISOString() });
 const enabled = { ...policy, enabled: true, status: "enabled" as const };
 const { fetch } = setup(enabled, resource, (url, options) => url.includes("/deliveries?") ? json({ items: [delivery()], next_cursor: "", has_more: false }) : url.endsWith("/retry") && options.method === "POST" ? ++writes === 1 ? Promise.reject(new Error("accepted response lost")) : json(delivery()) : undefined);
 fireEvent.click(await screen.findByRole("button", { name: `Retry retained delivery ${delivery().id}` })); const replay = await screen.findByRole<HTMLButtonElement>("button", { name: "Retry identical delivery decision" }); await waitFor(() => expect(replay.disabled).toBe(false));
 state = advanced; fireEvent.click(screen.getByRole("button", { name: "Refresh build-on-push metadata" })); await screen.findByText(`Delivery ${delivery().id} · ${advanced} · policy generation 1`); await waitFor(() => expect(replay.disabled).toBe(false)); expect(writes).toBe(1);
 fireEvent.click(replay); await waitFor(() => expect(writes).toBe(2)); await waitFor(() => expect(screen.queryByRole("button", { name: "Retry identical delivery decision" })).toBeNull()); expect(screen.queryByRole("button", { name: `Retry retained delivery ${delivery().id}` })).toBeNull();
 const commands = fetch.mock.calls.filter(([url]) => url.endsWith("/retry")); expect(commands).toHaveLength(2); expect(commands[0]![1].body).toBe(commands[1]![1].body); expect(JSON.parse(commands[0]![1].body as string)).toMatchObject({ expected_policy_generation: 1, delivery_id: delivery().id, request_id: expect.stringMatching(/^[0-9a-f-]{36}$/) });
});
test.each(["policy identity", "policy generation", "disabled policy", "viewer", "actor", "resource"])("uncertain receipt replay denies changed %s", async (change) => {
 const delivery = { id: "11111111-1111-4111-8111-111111111111", policy_id: "policy", policy_generation: 1, state: "blocked_exhausted", reason: "attempt_expired", after_commit: "a", verified_commit: "", build_id: "", attempts: 32, received_at: "", expires_at: "" }; let writes = 0;
 const { fetch, rerender } = setup({ ...policy, enabled: true }, resource, (url, options) => url.includes("/deliveries?") ? json({ items: [delivery], next_cursor: "", has_more: false }) : url.endsWith("/retry") && options.method === "POST" ? (writes++, Promise.reject(new Error("accepted response lost"))) : undefined);
 fireEvent.click(await screen.findByRole("button", { name: `Retry retained delivery ${delivery.id}` })); const replay = await screen.findByRole<HTMLButtonElement>("button", { name: "Retry identical delivery decision" }); await waitFor(() => expect(replay.disabled).toBe(false));
 if (change === "actor" || change === "resource") rerender(<QueryClientProvider client={client}><ResourceGithubPushBuilds actor={change === "actor" ? "new-actor" : "actor-identity"} resource={change === "resource" ? { ...resource, id: "other" } : resource} blocked /></QueryClientProvider>);
 else {
  const next = { ...policy, enabled: change !== "disabled policy", can_manage: change !== "viewer", id: change === "policy identity" ? "new-policy" : policy.id, generation: change === "policy generation" ? 2 : 1 };
  await act(async () => { client.setQueryData(["githubPushPolicy", "actor-identity", "project", "resource"], next); client.setQueryData(["githubPushDeliveries", "actor-identity", "project", "resource", ""], { items: [{ ...delivery, state: "queued", policy_id: next.id, policy_generation: next.generation }], next_cursor: "", has_more: false }); });
 }
 await waitFor(() => { const button = screen.queryByRole<HTMLButtonElement>("button", { name: "Retry identical delivery decision" }); expect(!button || button.disabled).toBe(true); });
 const button = screen.queryByRole("button", { name: "Retry identical delivery decision" }); if (button) fireEvent.click(button); await act(async () => {}); expect(writes).toBe(1); expect(fetch.mock.calls.filter(([url]) => url.endsWith("/retry"))).toHaveLength(1);
});


const automaticOptions: AutomaticDeploymentOptions = { available: true, reason: "eligible", baseline: { healthy_operation_id: "hop_manual", loaded_proxy_operation_id: "pop_loaded", loaded_generation: 7, runtime_configuration_generation: 2, container_port: 8080, readiness: { path: "/ready", timeout_seconds: 10 }, manual_epoch: 1, routes: [{ route_id: "route_b", hostname: "b.example.test", path_prefix: "/", version: 3, selected_release_operation_id: "hop_manual" }, { route_id: "route_a", hostname: "a.example.test", path_prefix: "/app", version: 2, selected_release_operation_id: "hop_manual" }] } };
const savedAutomatic: AutomaticDeploymentConfiguration = { mode: "automatic_unprotected", expected_healthy_operation_id: "hop_manual", expected_loaded_proxy_operation_id: "pop_loaded", expected_loaded_generation: 7, expected_runtime_configuration_generation: 2, container_port: 8080, readiness: { path: "/ready", timeout_seconds: 10 }, routes: [{ route_id: "route_a", expected_version: 2, expected_selected_release_operation_id: "hop_manual" }] };
const automaticPolicy: GitHubPushPolicy = { ...policy, owner_automatic_deployment_options: automaticOptions };
async function automaticReferences() {
  await references();
  fireEvent.click(screen.getByRole("radio", { name: "Automatic deployment without approval" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "https://b.example.test/" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "https://a.example.test/app" }));
  fireEvent.click(screen.getByLabelText(/I reviewed these exact references/));
}

test("owner explicitly selects automatic deployment with sorted server routes and readonly healthy baseline", async () => {
  const { fetch } = setup(automaticPolicy); await automaticReferences();
  expect(screen.queryByRole("textbox", { name: /route.*id/i })).toBeNull();
  expect(screen.getByText(/no approval step.*does not configure or enforce GitHub branch protection/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" }));
  await waitFor(() => expect(fetch.mock.calls.some(([, options]) => options.method === "PUT")).toBe(true));
  const body = JSON.parse(fetch.mock.calls.find(([, options]) => options.method === "PUT")![1].body as string);
  expect(body.configuration.automatic_deployment).toEqual({ ...savedAutomatic, routes: [...savedAutomatic.routes, { route_id: "route_b", expected_version: 3, expected_selected_release_operation_id: "hop_manual" }] });
  expect(body.request_id).toMatch(/^[0-9a-f-]{36}$/);
  expect(body.configuration.automatic_deployment).not.toHaveProperty("hostname");
});

test("build-only stays default even when automatic options are available", async () => {
  const { fetch } = setup(automaticPolicy); await references();
  expect(screen.getByRole<HTMLInputElement>("radio", { name: /Build only/ }).checked).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" }));
  await waitFor(() => expect(fetch.mock.calls.some(([, options]) => options.method === "PUT")).toBe(true));
  expect(JSON.parse(fetch.mock.calls.find(([, options]) => options.method === "PUT")![1].body as string).configuration).not.toHaveProperty("automatic_deployment");
});

test("saved automatic configuration is never silently removed when fresh owner options are unavailable", async () => {
  const p: GitHubPushPolicy = { ...policy, owner_configuration: { automation_actor_id: "actor", webhook_secret: { id: "webhook", version: 1 }, automatic_deployment: savedAutomatic }, owner_automatic_deployment_options: { available: false, reason: "proxy_not_ready" } };
  const { fetch } = setup(p); await references();
  expect(screen.getByRole<HTMLInputElement>("radio", { name: "Automatic deployment without approval" }).checked).toBe(true);
  fireEvent.submit(screen.getByRole("form", { name: "Configure GitHub build on push" }));
  expect(fetch.mock.calls.some(([, options]) => options.method === "PUT")).toBe(false);
  expect(screen.getByRole("status").textContent).toMatch(/saved automatic configuration is retained/);
  fireEvent.click(screen.getByRole("radio", { name: /Build only/ }));
  fireEvent.click(screen.getByLabelText(/I reviewed these exact references/));
  fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" }));
  await waitFor(() => expect(fetch.mock.calls.some(([, options]) => options.method === "PUT")).toBe(true));
  expect(JSON.parse(fetch.mock.calls.find(([, options]) => options.method === "PUT")![1].body as string).configuration).not.toHaveProperty("automatic_deployment");
});

test("fresh baseline drift blocks a new decision before submission and requires explicit route review", async () => {
  const { fetch } = setup(automaticPolicy); await automaticReferences();
  await act(async () => client.setQueryData(["githubPushPolicy", "actor-identity", "project", "resource"], { ...automaticPolicy, owner_automatic_deployment_options: { ...automaticOptions, baseline: { ...automaticOptions.baseline!, loaded_generation: 8 } } }));
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save disabled push configuration" }).disabled).toBe(true));
  fireEvent.submit(screen.getByRole("form", { name: "Configure GitHub build on push" }));
  expect(fetch.mock.calls.some(([, options]) => options.method === "PUT")).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "Review current automatic deployment baseline" }));
  expect(screen.getByRole<HTMLInputElement>("checkbox", { name: "https://a.example.test/app" }).checked).toBe(false);
  expect(screen.getByLabelText<HTMLInputElement>(/I reviewed these exact references/).checked).toBe(false);
});

test("lost automatic configure response keeps the exact UUID and baseline across a later options snapshot", async () => {
  let writes = 0;
  const { fetch } = setup(automaticPolicy, resource, (_url, options) => options.method === "PUT" ? ++writes === 1 ? Promise.reject(new TypeError("lost")) : json({ ...automaticPolicy, owner_configuration: { automation_actor_id: "actor", webhook_secret: { id: "webhook", version: 1 }, automatic_deployment: savedAutomatic } }) : undefined);
  await automaticReferences(); fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" }));
  const retry = await screen.findByRole("button", { name: "Retry identical push decision" });
  const firstBody = fetch.mock.calls.find(([, options]) => options.method === "PUT")![1].body;
  await act(async () => client.setQueryData(["githubPushPolicy", "actor-identity", "project", "resource"], { ...automaticPolicy, owner_automatic_deployment_options: { ...automaticOptions, baseline: { ...automaticOptions.baseline!, loaded_generation: 99, container_port: 9090 } } }));
  fireEvent.click(retry);
  await waitFor(() => expect(fetch.mock.calls.filter(([, options]) => options.method === "PUT")).toHaveLength(2));
  expect(fetch.mock.calls.filter(([, options]) => options.method === "PUT")[1]![1].body).toBe(firstBody);
});

test("configure receipt forces a fresh GET before another automatic review or enable", async () => {
  let reads = 0;
  const saved: GitHubPushPolicy = { ...policy, status: "verified", reason: "verified", verified_at: new Date().toISOString(), owner_configuration: { automation_actor_id: "actor", webhook_secret: { id: "webhook", version: 1 }, automatic_deployment: savedAutomatic } };
  const { fetch } = setup(automaticPolicy, resource, (url, options) => url.endsWith("/github-push-policy") ? options.method === "PUT" ? json({ ...saved, owner_automatic_deployment_options: automaticOptions }) : ++reads > 1 ? json({ ...saved, owner_automatic_deployment_options: { available: false, reason: "restore_hold" } }) : json(automaticPolicy) : undefined);
  await automaticReferences(); fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" }));
  await screen.findByText(/restore hold prevents new automatic deployment review/);
  expect(reads).toBeGreaterThanOrEqual(2);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Enable build and automatic deployment" }).disabled).toBe(true);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save disabled push configuration" }).disabled).toBe(true);
  expect(fetch.mock.calls.filter(([, options]) => options.method === "PUT")).toHaveLength(1);
});

test.each(["permission", "actor", "resource"])("automatic draft and late configure response cannot survive %s change", async (scope) => {
  let resolve!: (response: Response) => void;
  const { fetch, rerender } = setup(automaticPolicy, resource, (_url, options) => options.method === "PUT" ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  await automaticReferences(); fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" }));
  await waitFor(() => expect(resolve).toBeTypeOf("function"));
  const signal = fetch.mock.calls.find(([, options]) => options.method === "PUT")![1].signal!;
  if (scope === "permission") await act(async () => client.setQueryData(["githubPushPolicy", "actor-identity", "project", "resource"], { ...automaticPolicy, can_manage: false }));
  else rerender(<QueryClientProvider client={client}><ResourceGithubPushBuilds actor={scope === "actor" ? "other-owner" : "actor-identity"} resource={scope === "resource" ? { ...resource, id: "other-resource" } : resource} blocked={false} /></QueryClientProvider>);
  await waitFor(() => expect(signal.aborted).toBe(true));
  await act(async () => resolve(json({ ...automaticPolicy, generation: 99 })));
  expect(screen.queryByText(/Retained configure request/)).toBeNull();
  expect(screen.queryByText("99")).toBeNull();
  if (scope === "permission") expect(screen.queryByRole("radio", { name: "Automatic deployment without approval" })).toBeNull();
});


test("automatic configure conflict refreshes the baseline and requires a new explicit UUID decision", async () => {
  let writes = 0;
  const fresh: GitHubPushPolicy = { ...automaticPolicy, generation: 2, owner_automatic_deployment_options: { ...automaticOptions, baseline: { ...automaticOptions.baseline!, loaded_generation: 8, routes: automaticOptions.baseline!.routes.map((route) => ({ ...route, version: route.version + 1 })) } } };
  const { fetch } = setup(automaticPolicy, resource, (url, options) => options.method === "PUT" ? ++writes === 1 ? json({}, 409) : json(fresh) : url.endsWith("/github-push-policy") && writes > 0 ? json(fresh) : undefined);
  await automaticReferences(); fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" }));
  await screen.findByText(/The policy or source changed/);
  expect(writes).toBe(1);
  const first = JSON.parse(fetch.mock.calls.find(([, options]) => options.method === "PUT")![1].body as string);
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Use reviewed policy for a separate decision" }).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Use reviewed policy for a separate decision" }));
  await automaticReferences(); fireEvent.click(screen.getByRole("button", { name: "Save disabled push configuration" }));
  await waitFor(() => expect(writes).toBe(2));
  const second = JSON.parse(fetch.mock.calls.filter(([, options]) => options.method === "PUT")[1]![1].body as string);
  expect(second.request_id).not.toBe(first.request_id);
  expect(second.configuration.automatic_deployment.expected_loaded_generation).toBe(8);
  expect(first.configuration.automatic_deployment.expected_loaded_generation).toBe(7);
  expect(second.expected_policy_generation).toBe(2);
});
