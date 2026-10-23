import { ApiError } from "./errors";
import { request, type RequestOptions } from "./client";
import type { components } from "./generated";

type Schema<K extends keyof components["schemas"]> = components["schemas"][K];
export type GitHubPushPolicy = Schema<"GitHubPushPolicy">;
export type GitHubPushDelivery = Schema<"GitHubPushDelivery">;
export type GitHubPushConfigure = Schema<"GitHubPushConfigure">;
export type GitHubPushConfiguration = Schema<"GitHubPushConfiguration">;
export type GitHubPushCommand = Schema<"GitHubPushCommand">;
export type GitHubPushEnable = Schema<"GitHubPushEnable">;
export type GitHubPushRetry = Schema<"GitHubPushRetry">;
export type GitHubPushPage = Schema<"GitHubPushDeliveryPage">;
export type AutomaticDeploymentConfiguration = Schema<"HostingAutoDeployConfiguration">;
export type AutomaticDeploymentOptions = Schema<"HostingAutoDeployOptions">;
export type AutomaticDeploymentStatus = Schema<"HostingAutoDeployStatus">;
export type PushDecision = { action: "configure"; body: GitHubPushConfigure } | { action: "verify"; body: GitHubPushCommand } | { action: "enabled"; body: GitHubPushEnable } | { action: "retry"; body: GitHubPushRetry };
type Options = Pick<RequestOptions, "signal">;
const path = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}/github-push-policy`;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const identifier = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const integer = (value: unknown, minimum = 0, maximum = Number.MAX_SAFE_INTEGER): value is number => Number.isSafeInteger(value) && Number(value) >= minimum && Number(value) <= maximum;
function malformed(): never { throw new Error("Automatic deployment evidence is malformed"); }
function object(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) malformed();
  return value as Record<string, unknown>;
}
function readiness(value: unknown): AutomaticDeploymentConfiguration["readiness"] {
  const r = object(value);
  if (typeof r.path !== "string" || r.path.length > 1024 || !r.path.startsWith("/") || r.path.startsWith("//") || /[\\#\x00-\x1f\x7f]/.test(r.path) || !integer(r.timeout_seconds, 1, 120)) malformed();
  let decoded: string;
  try { decoded = decodeURIComponent(r.path); } catch { return malformed(); }
  if (/[\\\x00-\x1f\x7f]/.test(decoded)) malformed();
  return { path: r.path, timeout_seconds: r.timeout_seconds };
}
function automaticConfiguration(value: unknown): AutomaticDeploymentConfiguration {
  const c = object(value);
  if (c.mode !== "automatic_unprotected" || !identifier(c.expected_healthy_operation_id) || !identifier(c.expected_loaded_proxy_operation_id) || !integer(c.expected_loaded_generation, 1) || !integer(c.expected_runtime_configuration_generation) || !integer(c.container_port, 1, 65535) || !Array.isArray(c.routes) || c.routes.length < 1 || c.routes.length > 32) malformed();
  const routes = c.routes.map((value) => {
    const route = object(value);
    if (!identifier(route.route_id) || !integer(route.expected_version, 1) || !identifier(route.expected_selected_release_operation_id)) malformed();
    return { route_id: route.route_id, expected_version: route.expected_version, expected_selected_release_operation_id: route.expected_selected_release_operation_id };
  }).sort((left, right) => left.route_id < right.route_id ? -1 : left.route_id > right.route_id ? 1 : 0);
  if (new Set(routes.map((route) => route.route_id)).size !== routes.length) malformed();
  return { mode: c.mode, expected_healthy_operation_id: c.expected_healthy_operation_id, expected_loaded_proxy_operation_id: c.expected_loaded_proxy_operation_id, expected_loaded_generation: c.expected_loaded_generation, expected_runtime_configuration_generation: c.expected_runtime_configuration_generation, container_port: c.container_port, readiness: readiness(c.readiness), routes };
}
function automaticOptions(value: unknown): AutomaticDeploymentOptions {
  const o = object(value);
  if (typeof o.available !== "boolean" || typeof o.reason !== "string" || !Object.hasOwn(automaticDeploymentAvailability, o.reason) || o.available !== (o.reason === "eligible")) malformed();
  const reason = o.reason as AutomaticDeploymentOptions["reason"];
  if (!o.available) {
    if (o.baseline !== undefined) malformed();
    return { available: false, reason };
  }
  const b = object(o.baseline);
  if (!identifier(b.healthy_operation_id) || !identifier(b.loaded_proxy_operation_id) || !integer(b.loaded_generation, 1) || !integer(b.runtime_configuration_generation) || !integer(b.container_port, 1, 65535) || !integer(b.manual_epoch) || !Array.isArray(b.routes) || b.routes.length < 1 || b.routes.length > 32) malformed();
  const routes = b.routes.map((value) => {
    const route = object(value);
    if (!identifier(route.route_id) || !integer(route.version, 1) || !identifier(route.selected_release_operation_id) || typeof route.hostname !== "string" || route.hostname.length < 1 || route.hostname.length > 253 || !/^[a-zA-Z0-9.-]+$/.test(route.hostname) || typeof route.path_prefix !== "string" || route.path_prefix.length > 256 || !route.path_prefix.startsWith("/") || route.path_prefix.startsWith("//") || /[\\#\x00-\x1f\x7f]/.test(route.path_prefix)) malformed();
    return { route_id: route.route_id, hostname: route.hostname, path_prefix: route.path_prefix, version: route.version, selected_release_operation_id: route.selected_release_operation_id };
  }).sort((left, right) => left.route_id < right.route_id ? -1 : left.route_id > right.route_id ? 1 : 0);
  if (new Set(routes.map((route) => route.route_id)).size !== routes.length) malformed();
  return { available: true, reason, baseline: { healthy_operation_id: b.healthy_operation_id, loaded_proxy_operation_id: b.loaded_proxy_operation_id, loaded_generation: b.loaded_generation, runtime_configuration_generation: b.runtime_configuration_generation, container_port: b.container_port, readiness: readiness(b.readiness), routes, manual_epoch: b.manual_epoch } };
}
function automaticStatus(value: unknown): AutomaticDeploymentStatus {
  const s = object(value);
  if (s.mode !== "automatic_unprotected" || !["idle", "waiting_build", "waiting_deployment", "waiting_switch", "succeeded", "failed", "blocked", "expired"].includes(String(s.state)) || typeof s.reason !== "string" || !/^[a-z0-9_]{0,64}$/.test(s.reason) || !integer(s.manual_epoch) || typeof s.desired_commit !== "string" || !/^([0-9a-f]{40})?$/.test(s.desired_commit) || !integer(s.attempts, 0, 32) || !integer(s.observed_loaded_generation)) malformed();
  for (const name of ["build_id", "deployment_operation_id", "switch_operation_id"] as const) if (s[name] !== "" && !identifier(s[name])) malformed();
  if (s.expires_at !== undefined && (typeof s.expires_at !== "string" || !Number.isFinite(Date.parse(s.expires_at)))) malformed();
  return { mode: s.mode, state: s.state as AutomaticDeploymentStatus["state"], reason: s.reason, manual_epoch: s.manual_epoch, desired_commit: s.desired_commit, build_id: s.build_id as string, deployment_operation_id: s.deployment_operation_id as string, switch_operation_id: s.switch_operation_id as string, attempts: s.attempts, observed_loaded_generation: s.observed_loaded_generation, ...(s.expires_at !== undefined ? { expires_at: s.expires_at as string } : {}) };
}
export const automaticDeploymentAvailability: Record<AutomaticDeploymentOptions["reason"], string> = {
  eligible: "The server has current healthy-release and loaded HTTPS route evidence for review.",
  unbound: "Bind this application to an eligible server before configuring automatic deployment.",
  manual_healthy_required: "Deploy this source manually and confirm a healthy local build first.",
  proxy_not_ready: "Load the application's ordinary HTTPS routes through the managed proxy first.",
  runner_unsupported: "The selected runner does not currently qualify for automatic deployment.",
  routes_unavailable: "No complete set of eligible existing HTTPS routes is available. Review the application's routes.",
  restore_hold: "A restore hold prevents new automatic deployment review. Inspect its recorded operation; this panel cannot release it.",
  baseline_changed: "The saved source, runtime configuration, placement or healthy release changed. Complete a healthy manual deployment and refresh before reviewing again.",
};
export function automaticDeploymentFromOptions(options: AutomaticDeploymentOptions | undefined, routeIDs: string[]): AutomaticDeploymentConfiguration | undefined {
  const baseline = options?.available ? options.baseline : undefined;
  if (!baseline || routeIDs.length < 1 || routeIDs.length > 32 || new Set(routeIDs).size !== routeIDs.length) return undefined;
  const selected = new Set(routeIDs), routes = baseline.routes.filter((route) => selected.has(route.route_id));
  if (routes.length !== routeIDs.length) return undefined;
  return automaticConfiguration({ mode: "automatic_unprotected", expected_healthy_operation_id: baseline.healthy_operation_id, expected_loaded_proxy_operation_id: baseline.loaded_proxy_operation_id, expected_loaded_generation: baseline.loaded_generation, expected_runtime_configuration_generation: baseline.runtime_configuration_generation, container_port: baseline.container_port, readiness: baseline.readiness, routes: routes.map((route) => ({ route_id: route.route_id, expected_version: route.version, expected_selected_release_operation_id: route.selected_release_operation_id })) });
}
export function automaticDeploymentMatchesOptions(saved: AutomaticDeploymentConfiguration, options: AutomaticDeploymentOptions | undefined): boolean {
  const current = automaticDeploymentFromOptions(options, saved.routes.map((route) => route.route_id));
  return Boolean(current && JSON.stringify(automaticConfiguration(saved)) === JSON.stringify(current));
}
function configuration(c: GitHubPushConfiguration): GitHubPushConfiguration {
  return { automation_actor_id: c.automation_actor_id, webhook_secret: { id: c.webhook_secret.id, version: c.webhook_secret.version }, ...(c.provider_read_secret ? { provider_read_secret: { id: c.provider_read_secret.id, version: c.provider_read_secret.version } } : {}), ...(c.source_grant_id ? { source_grant_id: c.source_grant_id } : {}), ...(c.automatic_deployment !== undefined ? { automatic_deployment: automaticConfiguration(c.automatic_deployment) } : {}) };
}
function policy(p: GitHubPushPolicy, resource: string, includeOwnerOptions = false): GitHubPushPolicy {
  if (!["unconfigured", "disabled", "unverified", "verifying", "verified", "enabled", "blocked"].includes(p.status) || !Object.hasOwn(pushReason, p.reason) || p.resource_id !== resource || !p.project_id || !Number.isSafeInteger(p.generation) || p.generation < 0 || typeof p.can_manage !== "boolean") throw new Error("Push policy scope mismatch");
  return { can_manage: p.can_manage, id: p.id, resource_id: p.resource_id, project_id: p.project_id, generation: p.generation, resource_generation: p.resource_generation, source_generation: p.source_generation, enabled: p.enabled, status: p.status, reason: p.reason, repository_id: p.repository_id, repository: p.repository, branch: p.branch, verified_at: p.verified_at, desired_commit: p.desired_commit, last_build_id: p.last_build_id, callback_url: p.callback_url, provider_read_configured: p.provider_read_configured, ...(p.can_manage && p.owner_configuration ? { owner_configuration: configuration(p.owner_configuration) } : {}), ...(p.automatic_deployment !== undefined ? { automatic_deployment: automaticStatus(p.automatic_deployment) } : {}), ...(p.can_manage && includeOwnerOptions && p.owner_automatic_deployment_options !== undefined ? { owner_automatic_deployment_options: automaticOptions(p.owner_automatic_deployment_options) } : {}) };
}
function delivery(d: GitHubPushDelivery): GitHubPushDelivery {
  if (!["accepted", "received", "verifying", "retry_wait", "queued", "coalesced", "ignored", "obsolete", "blocked_exhausted"].includes(d.state) || !Object.hasOwn(pushReason, d.reason) || !uuid.test(d.id)) throw new Error("Push delivery identity mismatch");
  return { id: d.id, policy_id: d.policy_id, policy_generation: d.policy_generation, state: d.state, reason: d.reason, after_commit: d.after_commit, verified_commit: d.verified_commit, build_id: d.build_id, attempts: d.attempts, received_at: d.received_at, next_retry_at: d.next_retry_at, expires_at: d.expires_at };
}
export async function getGitHubPushPolicy(resource: string, options?: Options): Promise<GitHubPushPolicy> { return policy(await request<GitHubPushPolicy>(path(resource), { ...options, cache: "no-store" }), resource, true); }
export async function listGitHubPushDeliveries(resource: string, cursor = "", options?: Options): Promise<GitHubPushPage> {
  if (cursor && !uuid.test(cursor)) throw new Error("Invalid push history cursor");
  const page = await request<GitHubPushPage>(`${path(resource)}/deliveries`, { ...options, cache: "no-store", query: { limit: 20, ...(cursor ? { cursor } : {}) } });
  if (!Array.isArray(page.items) || page.items.length > 20 || typeof page.has_more !== "boolean" || (page.next_cursor && !uuid.test(page.next_cursor)) || (page.has_more && (!page.next_cursor || page.next_cursor === cursor))) throw new Error("Invalid push history page");
  return { items: page.items.map(delivery), next_cursor: page.next_cursor, has_more: page.has_more };
}
export async function executeGitHubPushDecision(resource: string, decision: PushDecision, options?: Options): Promise<GitHubPushPolicy | GitHubPushDelivery> {
  const base = { expected_policy_generation: decision.body.expected_policy_generation, request_id: decision.body.request_id };
  if (!uuid.test(base.request_id)) throw new Error("Invalid push request identity");
  const body = decision.action === "configure" ? { ...base, expected_resource_generation: decision.body.expected_resource_generation, expected_source_generation: decision.body.expected_source_generation, configuration: configuration(decision.body.configuration), reset_branch_baseline: decision.body.reset_branch_baseline } : decision.action === "enabled" ? { ...base, enabled: decision.body.enabled } : decision.action === "retry" ? { ...base, delivery_id: decision.body.delivery_id } : base;
  const response = await request<GitHubPushPolicy | GitHubPushDelivery>(`${path(resource)}${decision.action === "configure" ? "" : `/${decision.action}`}`, { ...options, cache: "no-store", method: decision.action === "configure" ? "PUT" : "POST", body });
  return decision.action === "retry" ? delivery(response as GitHubPushDelivery) : policy(response as GitHubPushPolicy, resource);
}
export function githubPushError(cause: unknown): string {
  if (cause instanceof ApiError) {
    if ([401, 403, 404].includes(cause.status)) return "Build-on-push access is unavailable. Reload to verify current project access.";
    if (cause.status === 409) return "The policy or source changed. Review refreshed metadata before a separate decision. Nothing was resubmitted.";
    if (cause.status === 400 || cause.status === 413) return "The server rejected this decision. Review the selected references and current generations.";
    if (cause.status === 503) return "Build-on-push is unavailable or at capacity. Wait at least 10 seconds before retrying the identical request.";
  }
  return "No confirmed response was received. The decision may already be recorded. Retry the identical request to recover its result.";
}
export const githubPushDenied = (cause: unknown) => cause instanceof ApiError && [401, 403, 404].includes(cause.status);
export function verificationCurrent(p: GitHubPushPolicy, now = Date.now()): boolean {
  const verified = Date.parse(p.verified_at ?? "");
  return p.status === "verified" && p.reason === "verified" && Number.isFinite(verified) && verified <= now && now - verified <= 60_000;
}
export const pushReason: Record<GitHubPushPolicy["reason"], string> = {
  "": "No blocker recorded", unsupported_source: "Save a supported GitHub.com HTTPS branch source.", unverified: "Verify the repository and branch before enabling.", disabled: "Build on push is disabled.", repository_identity_mismatch: "Repository identity changed. Disable and reconfigure after review.", repository_or_branch_unavailable: "The repository or branch is unavailable. Review provider access and source settings.", provider_credentials_required: "Select an existing provider read secret for this private repository.", provider_response_invalid: "GitHub returned an unusable response. Retry verification later.", rate_limited: "GitHub rate limit reached. Wait before retrying; public unauthenticated access has lower limits.", provider_unavailable: "GitHub is unavailable. Retry verification later.", comparison_limit: "Ancestry comparison exceeded 100 commits. Review history and explicitly reconfigure the baseline.", ancestry_unverified: "Ancestry is unverified. Review branch history before retrying or reconfiguring.", authority_unavailable: "Automation actor, source grant, runner, or project authority is unavailable. Restore access or reconfigure.", source_changed: "The saved source changed. Reconfigure and verify the current source.", policy_changed: "This delivery belongs to an older policy generation.", attempt_expired: "The automatic retry window expired. For the current enabled policy, an authorized owner can explicitly retry to start one new bounded recovery window.", quota_exceeded: "Build capacity is exhausted. Retry explicitly after capacity returns.", ignored_event: "This event was ignored.", ignored_disabled: "The policy was disabled when this event arrived.", verified: "Repository and branch verified.", build_queued: "A build was queued. A queued build alone does not prove deployment or HTTPS switch completion.", already_queued: "The build was already queued; no duplicate build was requested.", blocked_exhausted: "Automatic retries exhausted 32 attempts or 24 hours. For the current enabled policy, an authorized owner can explicitly retry for a new bounded recovery window, or review disabling or reconfiguring.", obsolete: "A newer accepted revision superseded this delivery.", invalid_push: "The push did not match the configured repository and branch.", baseline_reset: "The branch baseline was reset by an explicit configuration decision.",
};

// This deadline settles independently of fetch, including transports that ignore
// abort. Retire local ownership before notifying the transport of cancellation.
export function withGitHubPushDeadline<T>(signal: AbortSignal, milliseconds: number, operation: (signal: AbortSignal) => Promise<T>, beforeAbort?: () => void): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const controller = new AbortController();
    let owned = true, timer: ReturnType<typeof setTimeout> | undefined;
    const cleanup = () => { if (timer !== undefined) clearTimeout(timer); signal.removeEventListener("abort", parentAborted); };
    const cancel = (cause: Error) => {
      if (!owned) return;
      owned = false; cleanup(); beforeAbort?.(); reject(cause); controller.abort();
    };
    const parentAborted = () => cancel(new Error("Build-on-push request canceled; outcome may be unconfirmed"));
    if (signal.aborted) { parentAborted(); return; }
    signal.addEventListener("abort", parentAborted, { once: true });
    timer = setTimeout(() => cancel(new Error("Build-on-push request deadline exceeded; outcome may be unconfirmed")), milliseconds);
    try {
      operation(controller.signal).then((result) => { if (!owned) return; owned = false; cleanup(); resolve(result); }, (cause: unknown) => { if (!owned) return; owned = false; cleanup(); reject(cause); });
    } catch (cause) { if (!owned) return; owned = false; cleanup(); reject(cause); }
  });
}
