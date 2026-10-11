import { queryOptions, type QueryClient } from "@tanstack/react-query";
import type { paths } from "./generated";
import { ApiError } from "./errors";
import {
  getCurrentPrincipal, getHealth, getBootstrapStatus, getOIDCStatus, getOperationsStatus, getRunLogRetentionStatus, listApprovals, listAuditEvents,
  listProjectMembers, listProjects, listRepositories, listRunLogs, listRuns, getRun, listTemplates, listServices, listEnvironments, listRevisions, listDeployments, getDeployment, listRunners, getRunner,
} from "./resources";
import type { TaskRunListResponse } from "./resources";

type GetQuery<Path extends keyof paths> = paths[Path] extends { get?: infer Operation }
  ? Operation extends { parameters: { query?: infer Query } } ? Query : never
  : never;

export type ProjectFilters = GetQuery<"/api/v1/projects">;
export type ProjectMemberFilters = GetQuery<"/api/v1/project-members">;
export type RepositoryFilters = GetQuery<"/api/v1/repositories">;
export type TemplateFilters = GetQuery<"/api/v1/templates">;
export type RunFilters = GetQuery<"/api/v1/runs">;
export type RunLogFilters = GetQuery<"/api/v1/run-logs">;
export type ArtifactFilters = GetQuery<"/api/v1/artifacts">;
export type ApprovalFilters = GetQuery<"/api/v1/approvals">;
export type AuditEventFilters = GetQuery<"/api/v1/audit-events">;
export type CapabilityFilters = GetQuery<"/api/v1/capabilities">;
export type ServiceFilters = GetQuery<"/api/v1/services">;
export type EnvironmentFilters = GetQuery<"/api/v1/environments">;
export type RevisionFilters = GetQuery<"/api/v1/revisions">;
export type DeploymentFilters = GetQuery<"/api/v1/deployments">;
export type RunnerFilters = GetQuery<"/api/v1/runners">;

const filters = <T extends object>(value?: T): T => (value ?? {}) as T;

// Keys deliberately include only filters accepted by the generated API.
export const queryKeys = {
  health: ["health"] as const,
  bootstrapStatus: ["bootstrapStatus"] as const,
  oidcStatus: ["oidcStatus"] as const,
  operationsStatus: ["operationsStatus"] as const,
  runLogRetentionStatus: ["runLogRetentionStatus"] as const,
  principal: ["principal"] as const,
  projects: (value?: ProjectFilters) => ["projects", filters(value)] as const,
  allProjects: ["projects", "all"] as const,
  projectMembers: (value?: ProjectMemberFilters) => ["projectMembers", filters(value)] as const,
  allProjectMembers: ["projectMembers", "all"] as const,
  repositories: (value?: RepositoryFilters) => ["repositories", filters(value)] as const,
  templates: (value?: TemplateFilters) => ["templates", filters(value)] as const,
  allTemplates: ["templates", "all"] as const,
  runs: (value?: RunFilters) => ["runs", filters(value)] as const,
  run: (id: string) => ["run", id] as const,
  approvals: (value?: ApprovalFilters) => ["approvals", filters(value)] as const,
  auditEvents: (value?: AuditEventFilters) => ["auditEvents", filters(value)] as const,
  runLogs: (value?: RunLogFilters) => ["runLogs", filters(value)] as const,
  artifacts: (value?: ArtifactFilters) => ["artifacts", filters(value)] as const,
  capabilities: (value?: CapabilityFilters) => ["capabilities", filters(value)] as const,
  allServices: ["services", "all"] as const,
  allEnvironments: ["environments", "all"] as const,
  allRevisions: (serviceID: string) => ["revisions", "all", { service_id: serviceID }] as const,
  services: (value?: ServiceFilters) => ["services", filters(value)] as const,
  environments: (value?: EnvironmentFilters) => ["environments", filters(value)] as const,
  revisions: (value?: RevisionFilters) => ["revisions", filters(value)] as const,
  deployments: (value?: DeploymentFilters) => ["deployments", filters(value)] as const,
  deployment: (id: string) => ["deployment", id] as const,
  runners: (value?: RunnerFilters) => ["runners", filters(value)] as const,
  runner: (id: string) => ["runner", id] as const,
};

export const healthQuery = () => queryOptions({ queryKey: queryKeys.health, queryFn: ({ signal }) => getHealth({ signal }) });
// Bootstrap state contains only a fixed, public lifecycle enum. Keep it short
// lived so the sign-in guidance changes promptly after the CLI completes.
export const bootstrapStatusQuery = () => queryOptions({
  queryKey: queryKeys.bootstrapStatus,
  staleTime: 0,
  queryFn: ({ signal }) => getBootstrapStatus({ signal }),
  // Bootstrap may complete in a supported CLI process while this public page
  // remains open. Poll only until it completes, then leave it cached.
  refetchInterval: (query) => query.state.data?.status === "complete" ? false : 1_000,
});
export const oidcStatusQuery = () => queryOptions({ queryKey: queryKeys.oidcStatus, staleTime: 0, queryFn: ({ signal }) => getOIDCStatus({ signal }), retry: false });
// A mounted Operations page is the only owner of this query.  Avoid polling a
// background tab; the cached snapshot remains available for an immediate
// render when the operator returns, then refreshes on focus.
const visibleOperationsRefresh = (): number | false => typeof document === "undefined" || document.visibilityState === "visible" ? 10_000 : false;
export const operationsStatusQuery = () => queryOptions({ queryKey: queryKeys.operationsStatus, staleTime: 5_000, queryFn: ({ signal }) => getOperationsStatus({ signal }), refetchInterval: visibleOperationsRefresh, refetchOnWindowFocus: true });
export const runLogRetentionStatusQuery = () => queryOptions({ queryKey: queryKeys.runLogRetentionStatus, staleTime: 5_000, queryFn: ({ signal }) => getRunLogRetentionStatus({ signal }), refetchOnWindowFocus: true });
export const principalQuery = () => queryOptions({ queryKey: queryKeys.principal, staleTime: 10_000, queryFn: ({ signal }) => getCurrentPrincipal({ signal }) });
type BoundedPage<T> = { items: T[]; count: number; total: number; offset: number };
async function collectBoundedPages<T>(load: (offset: number) => Promise<BoundedPage<T>>): Promise<T[]> {
  const items: T[] = [];
  let offset = 0;
  for (;;) {
    const page = await load(offset);
    items.push(...page.items);
    if (page.count === 0 || page.offset + page.count >= page.total) return items;
    offset = page.offset + page.count;
    if (offset > 100_000) throw new Error("list exceeds the supported pagination range");
  }
}
export const allProjectsQuery = () => queryOptions({ queryKey: queryKeys.allProjects, queryFn: ({ signal }) => collectBoundedPages((offset) => listProjects({ limit: 100, offset }, { signal })) });
export const allTemplatesQuery = () => queryOptions({ queryKey: queryKeys.allTemplates, queryFn: ({ signal }) => collectBoundedPages((offset) => listTemplates({ limit: 100, offset }, { signal })) });
export const allProjectMembersQuery = () => queryOptions({ queryKey: queryKeys.allProjectMembers, queryFn: ({ signal }) => collectBoundedPages((offset) => listProjectMembers({ limit: 100, offset }, { signal })) });
export const projectsQuery = (value?: ProjectFilters) => queryOptions({ queryKey: queryKeys.projects(value), queryFn: ({ signal }) => listProjects(value, { signal }) });
export const projectMembersQuery = (value?: ProjectMemberFilters) => queryOptions({ queryKey: queryKeys.projectMembers(value), queryFn: ({ signal }) => listProjectMembers(value, { signal }) });
export const repositoriesQuery = (value?: RepositoryFilters) => queryOptions({ queryKey: queryKeys.repositories(value), queryFn: ({ signal }) => listRepositories(value, { signal }) });
export const templatesQuery = (value?: TemplateFilters) => queryOptions({ queryKey: queryKeys.templates(value), queryFn: ({ signal }) => listTemplates(value, { signal }) });
export const runsQuery = (value?: RunFilters) => queryOptions({ queryKey: queryKeys.runs(value), queryFn: ({ signal }) => listRuns(value, { signal }) });
export const approvalsQuery = (value?: ApprovalFilters) => queryOptions({ queryKey: queryKeys.approvals(value), queryFn: ({ signal }) => listApprovals(value, { signal }) });
export const auditEventsQuery = (value?: AuditEventFilters) => queryOptions({ queryKey: queryKeys.auditEvents(value), queryFn: ({ signal }) => listAuditEvents(value, { signal }) });
export const runLogsQuery = (value: RunLogFilters = {}) => queryOptions({ queryKey: queryKeys.runLogs(value), queryFn: ({ signal }) => listRunLogs(value, { signal }) });
export const allServicesQuery = () => queryOptions({ queryKey: queryKeys.allServices, queryFn: ({ signal }) => collectBoundedPages((offset) => listServices({ limit: 100, offset }, { signal })) });
export const allEnvironmentsQuery = () => queryOptions({ queryKey: queryKeys.allEnvironments, queryFn: ({ signal }) => collectBoundedPages((offset) => listEnvironments({ limit: 100, offset }, { signal })) });
export const allRevisionsQuery = (serviceID: string) => queryOptions({ queryKey: queryKeys.allRevisions(serviceID), queryFn: ({ signal }) => collectBoundedPages((offset) => listRevisions({ service_id: serviceID, limit: 100, offset }, { signal })) });
export const servicesQuery = (value?: ServiceFilters) => queryOptions({ queryKey: queryKeys.services(value), queryFn: ({ signal }) => listServices(value, { signal }) });
export const environmentsQuery = (value?: EnvironmentFilters) => queryOptions({ queryKey: queryKeys.environments(value), queryFn: ({ signal }) => listEnvironments(value, { signal }) });
export const revisionsQuery = (value?: RevisionFilters) => queryOptions({ queryKey: queryKeys.revisions(value), queryFn: ({ signal }) => listRevisions(value, { signal }) });
export const deploymentsQuery = (value?: DeploymentFilters) => queryOptions({ queryKey: queryKeys.deployments(value), queryFn: ({ signal }) => listDeployments(value, { signal }) });
export const runQuery = (id: string) => queryOptions({ queryKey: queryKeys.run(id), queryFn: async ({ signal }) => {
  try {
    return await getRun(id, { signal });
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) return null;
    throw error;
  }
} });
// The server deliberately normalizes inaccessible and absent deployment IDs to
// one 404. Preserve that non-enumerating state as normal route data so the
// detail view can render its safe unavailable state rather than surface an
// implementation error boundary.
export const deploymentQuery = (id: string) => queryOptions({ queryKey: queryKeys.deployment(id), queryFn: async ({ signal }) => {
  try {
    return await getDeployment(id, { signal });
  } catch (error) {
    // React Query reserves undefined for a query-function contract failure;
    // null is the explicit, cacheable unavailable detail state.
    if (error instanceof ApiError && error.status === 404) return null;
    throw error;
  }
} });
export const runnersQuery = (value?: RunnerFilters) => queryOptions({ queryKey: queryKeys.runners(value), queryFn: ({ signal }) => listRunners(value, { signal }) });
export const runnerQuery = (id: string) => queryOptions({ queryKey: queryKeys.runner(id), queryFn: ({ signal }) => getRunner(id, { signal }) });

export function retryRemote(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError) return error.status >= 500 && error.status !== 501 && failureCount < 2;
  return failureCount < 2;
}

const terminalRunStatuses = new Set(["succeeded", "failed", "canceled", "rejected"]);
const terminalDeploymentStatuses = new Set(["succeeded", "failed", "canceled", "rolled_back", "rollback_failed", "manual_intervention"]);
export function shouldPollRunList(runs: TaskRunListResponse | undefined): boolean {
  return Boolean(runs?.items.some((run) => !terminalRunStatuses.has(run.status)));
}
export function shouldPollSelectedLogs(runID: string, runs: TaskRunListResponse | undefined): boolean {
  return Boolean(runs?.items.some((run) => run.id === runID && !terminalRunStatuses.has(run.status)));
}
export function shouldPollDeploymentList(deployments: import("./resources").DeploymentList | undefined): boolean {
  return Boolean(deployments?.items.some((deployment) => !terminalDeploymentStatuses.has(deployment.status)));
}

export function configureQueryClient(client: QueryClient): QueryClient {
  client.setDefaultOptions({ queries: { retry: retryRemote }, mutations: { retry: false } });
  return client;
}
