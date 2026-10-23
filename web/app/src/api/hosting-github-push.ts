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
export type PushDecision = { action: "configure"; body: GitHubPushConfigure } | { action: "verify"; body: GitHubPushCommand } | { action: "enabled"; body: GitHubPushEnable } | { action: "retry"; body: GitHubPushRetry };
type Options = Pick<RequestOptions, "signal">;
const path = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}/github-push-policy`;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
function configuration(c: GitHubPushConfiguration): GitHubPushConfiguration {
  return { automation_actor_id: c.automation_actor_id, webhook_secret: { id: c.webhook_secret.id, version: c.webhook_secret.version }, ...(c.provider_read_secret ? { provider_read_secret: { id: c.provider_read_secret.id, version: c.provider_read_secret.version } } : {}), ...(c.source_grant_id ? { source_grant_id: c.source_grant_id } : {}) };
}
function policy(p: GitHubPushPolicy, resource: string): GitHubPushPolicy {
  if (!["unconfigured", "disabled", "unverified", "verifying", "verified", "enabled", "blocked"].includes(p.status) || !Object.hasOwn(pushReason, p.reason) || p.resource_id !== resource || !p.project_id || !Number.isSafeInteger(p.generation) || p.generation < 0 || typeof p.can_manage !== "boolean") throw new Error("Push policy scope mismatch");
  return { can_manage: p.can_manage, id: p.id, resource_id: p.resource_id, project_id: p.project_id, generation: p.generation, resource_generation: p.resource_generation, source_generation: p.source_generation, enabled: p.enabled, status: p.status, reason: p.reason, repository_id: p.repository_id, repository: p.repository, branch: p.branch, verified_at: p.verified_at, desired_commit: p.desired_commit, last_build_id: p.last_build_id, callback_url: p.callback_url, provider_read_configured: p.provider_read_configured, ...(p.can_manage && p.owner_configuration ? { owner_configuration: configuration(p.owner_configuration) } : {}) };
}
function delivery(d: GitHubPushDelivery): GitHubPushDelivery {
  if (!["accepted", "received", "verifying", "retry_wait", "queued", "coalesced", "ignored", "obsolete", "blocked_exhausted"].includes(d.state) || !Object.hasOwn(pushReason, d.reason) || !uuid.test(d.id)) throw new Error("Push delivery identity mismatch");
  return { id: d.id, policy_id: d.policy_id, policy_generation: d.policy_generation, state: d.state, reason: d.reason, after_commit: d.after_commit, verified_commit: d.verified_commit, build_id: d.build_id, attempts: d.attempts, received_at: d.received_at, next_retry_at: d.next_retry_at, expires_at: d.expires_at };
}
export async function getGitHubPushPolicy(resource: string, options?: Options): Promise<GitHubPushPolicy> { return policy(await request<GitHubPushPolicy>(path(resource), { ...options, cache: "no-store" }), resource); }
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
  "": "No blocker recorded", unsupported_source: "Save a supported GitHub.com HTTPS branch source.", unverified: "Verify the repository and branch before enabling.", disabled: "Build on push is disabled.", repository_identity_mismatch: "Repository identity changed. Disable and reconfigure after review.", repository_or_branch_unavailable: "The repository or branch is unavailable. Review provider access and source settings.", provider_credentials_required: "Select an existing provider read secret for this private repository.", provider_response_invalid: "GitHub returned an unusable response. Retry verification later.", rate_limited: "GitHub rate limit reached. Wait before retrying; public unauthenticated access has lower limits.", provider_unavailable: "GitHub is unavailable. Retry verification later.", comparison_limit: "Ancestry comparison exceeded 100 commits. Review history and explicitly reconfigure the baseline.", ancestry_unverified: "Ancestry is unverified. Review branch history before retrying or reconfiguring.", authority_unavailable: "Automation actor, source grant, runner, or project authority is unavailable. Restore access or reconfigure.", source_changed: "The saved source changed. Reconfigure and verify the current source.", policy_changed: "This delivery belongs to an older policy generation.", attempt_expired: "The 24-hour retry window expired. Review and reconfigure; retry cannot extend it.", quota_exceeded: "Build capacity is exhausted. Retry explicitly after capacity returns.", ignored_event: "This event was ignored.", ignored_disabled: "The policy was disabled when this event arrived.", verified: "Repository and branch verified.", build_queued: "A build was queued. Deployment remains manual.", already_queued: "The build was already queued; no duplicate build was requested.", blocked_exhausted: "The delivery exhausted its 32 attempts or 24-hour window. Disable or reconfigure after review.", obsolete: "A newer accepted revision superseded this delivery.", invalid_push: "The push did not match the configured repository and branch.", baseline_reset: "The branch baseline was reset by an explicit configuration decision.",
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
