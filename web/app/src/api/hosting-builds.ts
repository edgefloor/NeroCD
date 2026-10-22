import { queryOptions } from "@tanstack/react-query";
import { request, type RequestOptions } from "./client";
import { ApiError } from "./errors";
import type { components } from "./generated";
export type HostingBuild = components["schemas"]["PublicHostingBuild"];
export type HostingBuildInput = components["schemas"]["HostingBuildRequest"];
export type HostingBuildPage = components["schemas"]["HostingBuildListResponse"];
type Options = Pick<RequestOptions, "signal">;
const historyPath = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}/builds`;
const detailPath = (id: string) => `/api/v1/hosting-builds/${encodeURIComponent(id)}`;
// Keep the public DTO whitelist at the client cache boundary too. Runner plans,
// policy/grant data and transport credentials never become UI build metadata.
function publicBuild(b: HostingBuild): HostingBuild {
  const s = b.snapshot.source, settings = s.settings, t = b.snapshot.target;
  return { id: b.id, project_id: b.project_id, resource_id: b.resource_id, state: b.state, revision: b.revision, run_id: b.run_id, created_by: b.created_by, created_at: b.created_at, updated_at: b.updated_at, failure_code: b.failure_code,
    snapshot: { resource_generation: b.snapshot.resource_generation, source: { source_id: s.source_id, generation: s.generation, repository_url: s.repository_url, requested_ref: s.requested_ref, credential_kind: s.credential_kind, settings: { strategy: settings.strategy, context_dir: settings.context_dir, dockerfile_path: settings.dockerfile_path, build_target: settings.build_target, publish_dir: settings.publish_dir, spa_fallback: settings.spa_fallback } }, target: { binding_id: t.binding_id, destination_id: t.destination_id, server_id: t.server_id, runner_id: t.runner_id } },
    ...(b.source_receipt ? { source_receipt: { commit: b.source_receipt.commit, tree_digest: b.source_receipt.tree_digest } } : {}),
    ...(b.artifact ? { artifact: { origin: b.artifact.origin, image_id: b.artifact.image_id, index_digest: b.artifact.index_digest, manifest_digest: b.artifact.manifest_digest, config_digest: b.artifact.config_digest, platform: b.artifact.platform } } : {}) };
}
export async function requestHostingBuild(resourceID: string, input: HostingBuildInput, options?: Options): Promise<HostingBuild> {
  const body = { expected_resource_generation: input.expected_resource_generation, expected_source_generation: input.expected_source_generation, idempotency_key: input.idempotency_key };
  const response = await request<{ build: HostingBuild }>(historyPath(resourceID), { ...options, method: "POST", body }); return publicBuild(response.build);
}
export async function getHostingBuild(id: string, options?: Options): Promise<HostingBuild> { const response = await request<{ build: HostingBuild }>(detailPath(id), options); return publicBuild(response.build); }
export async function cancelHostingBuild(id: string, expectedRevision: number, options?: Options): Promise<HostingBuild> { const response = await request<{ build: HostingBuild }>(`${detailPath(id)}/cancel`, { ...options, method: "POST", body: { expected_revision: expectedRevision } }); return publicBuild(response.build); }
export async function listHostingBuilds(id: string, offset: number, options?: Options): Promise<HostingBuildPage> { const page = await request<HostingBuildPage>(historyPath(id), { ...options, query: { limit: 20, offset } }); return { items: page.items.map(publicBuild), count: page.count, limit: page.limit, offset: page.offset, total: page.total }; }
export const buildKeys = {
  history: (actor: string, project: string, resource: string) => ["hostingBuilds", actor, project, resource] as const,
  detail: (actor: string, project: string, resource: string, id: string) => ["hostingBuild", actor, project, resource, id] as const,
};
export function checkBuildScope(build: HostingBuild, project: string, resource: string, id?: string) { if (build.project_id !== project || build.resource_id !== resource || (id && build.id !== id)) throw new Error("Build scope mismatch"); }
export const buildsQuery = (actor: string, project: string, resource: string, offset: number) => queryOptions({ queryKey: [...buildKeys.history(actor, project, resource), offset], retry: false, queryFn: async ({ signal }) => { const page = await listHostingBuilds(resource, offset, { signal }); page.items.forEach((build) => checkBuildScope(build, project, resource)); return page; } });
export const buildQuery = (actor: string, project: string, resource: string, id: string) => queryOptions({ queryKey: buildKeys.detail(actor, project, resource, id), retry: false, queryFn: async ({ signal }) => { const build = await getHostingBuild(id, { signal }); checkBuildScope(build, project, resource, id); return build; } });
export const buildTerminal = (state: string) => ["succeeded", "failed", "canceled"].includes(state);
export function buildError(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 503) return "Build admission is unavailable or disabled by the server operator. Verify the trusted build policy, PostgreSQL/River service and runner prerequisites. Your exact request is preserved.";
    if (cause.status === 409) return "Build request conflicts with current generations, placement, prerequisites or a retained request key. Review refreshed metadata explicitly; nothing was automatically resubmitted.";
    if (cause.status === 401 || cause.status === 403) return "Build permission denied. Reload to refresh current project access before another mutation.";
    if (cause.status === 400) return "The server rejected this public Git build request. Review the saved source and required generations.";
    if (cause.status === 404) return "The resource or build is unavailable under current access.";
  }
  return "No confirmed response was received. The build may already exist; retry the identical request to recover it.";
}

export type HostingBuildLogItem = components["schemas"]["HostingBuildLogItem"];
export type HostingBuildLogPage = components["schemas"]["HostingBuildLogPage"];
// Output stays in the mounted viewer, never in the metadata query cache.
export function getHostingBuildLogs(id: string, afterSequence: number, options?: Options): Promise<HostingBuildLogPage> {
  return request<HostingBuildLogPage>(`${detailPath(id)}/logs`, { ...options, cache: "no-store", query: { after_sequence: afterSequence, limit: 200 } });
}
