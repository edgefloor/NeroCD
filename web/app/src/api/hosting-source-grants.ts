import { queryOptions } from "@tanstack/react-query";
import { request, type RequestOptions } from "./client";
import type { components, operations } from "./generated";
import { ApiError } from "./errors";
import type { Resource } from "./hosting";
export type HostingSourceGrant = components["schemas"]["HostingSourceGrant"];
export type HostingSourceGrantRequest = components["schemas"]["HostingSourceGrantRequest"];
export type HostingSourceGrantPage = operations["listHostingSourceGrants"]["responses"][200]["content"]["application/json"];
type Options = Pick<RequestOptions, "signal">;
const path = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}/source-grants`;
const publicGrant = ({ id, version, source_id, source_generation, resource_id, kind, revoked }: HostingSourceGrant): HostingSourceGrant => ({ id, version, source_id, source_generation, resource_id, kind, revoked });
export const sourceGrantKeys = { resource: (actor: string, project: string, resource: string) => ["hostingSourceGrants", actor, project, resource] as const };
export function eligibleSourceGrant(grant: HostingSourceGrant, resource: Resource) {
  return resource.source.kind === "git" && resource.source.git.credential_kind === "https_basic" && grant.resource_id === resource.id && grant.source_id === resource.source.git.source_id && grant.source_generation > 0 && grant.source_generation === resource.source.git.generation && grant.version === 1 && grant.kind === "https_basic" && grant.revoked === false;
}
export function newSourceGrantRequestID(): string { const bytes = crypto.getRandomValues(new Uint8Array(16)); return Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join(""); }
export async function listHostingSourceGrants(resource: string, offset: number, options?: Options): Promise<HostingSourceGrantPage> {
  const page = await request<HostingSourceGrantPage>(path(resource), { ...options, cache: "no-store", query: { limit: 20, offset } }).catch((cause) => { throw cause instanceof ApiError ? new ApiError(cause.status, "Source grant metadata is unavailable", cause.requestID) : new Error("Source grant metadata is unavailable"); });
  if (!Array.isArray(page.items) || page.items.length > 20 || page.items.some((grant) => grant.resource_id !== resource)) throw new Error("Grant scope mismatch");
  return { items: page.items.map(publicGrant), limit: page.limit, offset: page.offset, count: page.count, total: page.total };
}
export const sourceGrantsQuery = (actor: string, project: string, resource: string, offset: number) => queryOptions({ queryKey: [...sourceGrantKeys.resource(actor, project, resource), offset], gcTime: 0, retry: false, queryFn: ({ signal }) => listHostingSourceGrants(resource, offset, { signal }) });
export async function requestHostingSourceGrant(resource: string, input: HostingSourceGrantRequest, options?: Options): Promise<HostingSourceGrant> {
  const { expected_resource_generation, expected_source_generation, secret_id, secret_version, request_id } = input;
  const result = await request<{ grant: HostingSourceGrant }>(path(resource), { ...options, method: "POST", cache: "no-store", body: { expected_resource_generation, expected_source_generation, secret_id, secret_version, request_id } }); return publicGrant(result.grant);
}
export function revokeHostingSourceGrant(resource: string, grant: string, options?: Options): Promise<{ revoked: boolean }> { return request(`${path(resource)}/${encodeURIComponent(grant)}/revoke`, { ...options, method: "POST", cache: "no-store", body: {} }); }
