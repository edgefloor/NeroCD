import type { components, paths } from "./generated";
import { request, type RequestOptions } from "./client";

type Schema<Name extends keyof components["schemas"]> = components["schemas"][Name];
type Operation<Path extends keyof paths, Method extends keyof paths[Path]> = NonNullable<paths[Path][Method]>;
type GetQuery<Path extends keyof paths> = Operation<Path, "get"> extends { parameters: { query?: infer Query } } ? Query : never;
type JSONRequest<Path extends keyof paths, Method extends keyof paths[Path]> = Operation<Path, Method> extends { requestBody: { content: { "application/json": infer Body } } } ? Body : never;
type JSONResponse<Path extends keyof paths, Method extends keyof paths[Path], Status extends number> = Operation<Path, Method> extends { responses: infer Responses }
  ? Status extends keyof Responses
    ? Responses[Status] extends { content: { "application/json": infer Body } }
      ? Body
      : void
    : never
  : never;

export type HealthResponse = Schema<"HealthResponse">;
export type BootstrapStatus = Schema<"BootstrapStatus">;
export type OIDCStatus = Schema<"OIDCStatus">;
export type OperationsStatus = Schema<"OperationsStatus">;
export type RunLogRetentionPolicy = Schema<"RunLogRetentionPolicy">;
export type RunLogRetentionPolicyInput = Schema<"RunLogRetentionPolicyInput">;
export type RunLogRetentionPreview = Schema<"RunLogRetentionPreview">;
export type RunLogRetentionStatus = Schema<"RunLogRetentionStatus">;
export type RunLogRetentionExecuteInput = Schema<"RunLogRetentionExecuteInput">;
export type RunLogRetentionExecution = Schema<"RunLogRetentionExecution">;
export type Principal = Schema<"Principal">;
export type Project = Schema<"Project">;
export type ProjectMember = Schema<"ProjectMember">;
export type TaskTemplate = Schema<"TaskTemplate">;
export type RunSpec = Schema<"RunSpec">;
export type RepositoryRef = Schema<"RepositoryRef">;
export type ProcessSpec = Schema<"ProcessSpec">;
export type ArtifactSpec = Schema<"ArtifactSpec">;
export type SecretBinding = Schema<"SecretBinding">;
export type Workflow = Schema<"Workflow">;
export type WorkflowStep = Schema<"WorkflowStep">;
export type WorkflowState = Schema<"WorkflowState">;
export type WorkflowStepState = Schema<"WorkflowStepState">;
export type TaskRun = Schema<"TaskRun">;
export type RunLog = Schema<"RunLog">;
export type ArtifactRecord = Schema<"ArtifactRecord">;
export type Capability = Schema<"Capability">;
export type Repository = Schema<"Repository">;
export type Approval = Schema<"Approval">;
export type AuditEvent = Schema<"AuditEvent">;
export type ProjectInput = Schema<"ProjectInput">;
export type ProjectMemberInput = Schema<"ProjectMemberInput">;
export type TemplateInput = Schema<"TemplateInput">;
export type RepositoryInput = Schema<"RepositoryInput">;
export type RunRequestInput = Schema<"RunRequestInput">;
export type BrowserSessionResponse = Schema<"BrowserSessionResponse">;
export type Service = Schema<"Service">;
export type Environment = Schema<"Environment">;
export type Revision = Schema<"Revision">;
export type Deployment = Schema<"Deployment">;
export type Runner = Schema<"Runner">;
export type RunnerDetail = Schema<"RunnerDetail">;
export type RunnerEnrollment = Schema<"RunnerEnrollment">;
export type RunnerEnrollmentInput = Schema<"RunnerEnrollmentInput">;
export type CreatedRunnerEnrollment = Schema<"CreatedRunnerEnrollment">;
export type ServiceList = Omit<Schema<"ServiceListResponse">, "items"> & { items: Service[] };
export type EnvironmentList = Omit<Schema<"EnvironmentListResponse">, "items"> & { items: Environment[] };
export type RevisionList = Omit<Schema<"RevisionListResponse">, "items"> & { items: Revision[] };
export type DeploymentList = Omit<Schema<"DeploymentListResponse">, "items"> & { items: Deployment[] };

export function getHealth(options?: Pick<RequestOptions, "signal">): Promise<HealthResponse> { return request("/api/v1/health", options); }
export function getBootstrapStatus(options?: Pick<RequestOptions, "signal">): Promise<BootstrapStatus> { return request("/api/v1/bootstrap-status", { ...options, cache: "no-store" }); }
export function getOIDCStatus(options?: Pick<RequestOptions, "signal">): Promise<OIDCStatus> { return request("/api/v1/oidc/status", { ...options, cache: "no-store" }); }
export function getOperationsStatus(options?: Pick<RequestOptions, "signal">): Promise<OperationsStatus> { return request("/api/v1/operations/status", options); }
export function getRunLogRetentionStatus(options?: Pick<RequestOptions, "signal">): Promise<RunLogRetentionStatus> { return request("/api/v1/run-log-retention", options); }
export function updateRunLogRetentionPolicy(input: JSONRequest<"/api/v1/run-log-retention", "put">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/run-log-retention", "put", 200>> { return request("/api/v1/run-log-retention", { ...options, method: "PUT", body: input }); }
export function executeRunLogRetention(input: JSONRequest<"/api/v1/run-log-retention/execute", "post">, requestID: string, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/run-log-retention/execute", "post", 200>> { return request("/api/v1/run-log-retention/execute", { ...options, method: "POST", body: input, requestID }); }
export function getCurrentPrincipal(options?: Pick<RequestOptions, "signal">): Promise<Principal> { return request("/api/v1/me", options); }
export function createBrowserSession(input: JSONRequest<"/api/v1/browser-sessions", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/browser-sessions", "post", 201>> { return request("/api/v1/browser-sessions", { ...options, method: "POST", body: input }); }
export function revokeBrowserSession(options?: Pick<RequestOptions, "signal">): Promise<void> { return request("/api/v1/browser-sessions", { ...options, method: "DELETE" }); }
export function listProjects(query?: GetQuery<"/api/v1/projects">, options?: Pick<RequestOptions, "signal">): Promise<Schema<"ProjectListResponse">> { return request("/api/v1/projects", { ...options, query }); }
export function createProject(input: JSONRequest<"/api/v1/projects", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/projects", "post", 201>> { return request("/api/v1/projects", { ...options, method: "POST", body: input }); }
export function updateProject(input: JSONRequest<"/api/v1/projects", "patch">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/projects", "patch", 200>> { return request("/api/v1/projects", { ...options, method: "PATCH", body: input }); }
export function archiveProject(input: JSONRequest<"/api/v1/projects/archive", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/projects/archive", "post", 200>> { return request("/api/v1/projects/archive", { ...options, method: "POST", body: input }); }
export function listProjectMembers(query?: GetQuery<"/api/v1/project-members">, options?: Pick<RequestOptions, "signal">): Promise<Schema<"ProjectMemberListResponse">> { return request("/api/v1/project-members", { ...options, query }); }
export function upsertProjectMember(input: JSONRequest<"/api/v1/project-members", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/project-members", "post", 200>> { return request("/api/v1/project-members", { ...options, method: "POST", body: input }); }
export function listRepositories(query?: GetQuery<"/api/v1/repositories">, options?: Pick<RequestOptions, "signal">): Promise<Schema<"RepositoryListResponse">> { return request("/api/v1/repositories", { ...options, query }); }
export function createRepository(input: JSONRequest<"/api/v1/repositories", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/repositories", "post", 201>> { return request("/api/v1/repositories", { ...options, method: "POST", body: input }); }
export function listTemplates(query?: GetQuery<"/api/v1/templates">, options?: Pick<RequestOptions, "signal">): Promise<Schema<"TaskTemplateListResponse">> { return request("/api/v1/templates", { ...options, query }); }
export function createTemplate(input: JSONRequest<"/api/v1/templates", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/templates", "post", 201>> { return request("/api/v1/templates", { ...options, method: "POST", body: input }); }
export function updateTemplate(input: JSONRequest<"/api/v1/templates", "patch">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/templates", "patch", 200>> { return request("/api/v1/templates", { ...options, method: "PATCH", body: input }); }
export function listRuns(query?: GetQuery<"/api/v1/runs">, options?: Pick<RequestOptions, "signal">): Promise<Schema<"TaskRunListResponse">> { return request("/api/v1/runs", { ...options, query }); }
export function requestRun(input: JSONRequest<"/api/v1/runs", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/runs", "post", 201>> { return request("/api/v1/runs", { ...options, method: "POST", body: input }); }
export function approveRun(input: JSONRequest<"/api/v1/runs/approve", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/runs/approve", "post", 200>> { return request("/api/v1/runs/approve", { ...options, method: "POST", body: input }); }
export function rejectRun(input: JSONRequest<"/api/v1/runs/reject", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/runs/reject", "post", 200>> { return request("/api/v1/runs/reject", { ...options, method: "POST", body: input }); }
export function cancelRun(input: JSONRequest<"/api/v1/runs/cancel", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/runs/cancel", "post", 200>> { return request("/api/v1/runs/cancel", { ...options, method: "POST", body: input }); }
export function listRunLogs(query?: GetQuery<"/api/v1/run-logs">, options?: Pick<RequestOptions, "signal">): Promise<Schema<"RunLogListResponse">> { return request("/api/v1/run-logs", { ...options, query }); }
export function listArtifacts(query?: GetQuery<"/api/v1/artifacts">, options?: Pick<RequestOptions, "signal">): Promise<Schema<"ArtifactListResponse">> { return request("/api/v1/artifacts", { ...options, query }); }
export function listApprovals(query?: GetQuery<"/api/v1/approvals">, options?: Pick<RequestOptions, "signal">): Promise<Schema<"ApprovalListResponse">> { return request("/api/v1/approvals", { ...options, query }); }
export function listAuditEvents(query?: GetQuery<"/api/v1/audit-events">, options?: Pick<RequestOptions, "signal">): Promise<Schema<"AuditEventListResponse">> { return request("/api/v1/audit-events", { ...options, query }); }
export function listServices(query?: GetQuery<"/api/v1/services">, options?: Pick<RequestOptions, "signal">): Promise<ServiceList> { return request<ServiceList>("/api/v1/services", { ...options, query }); }
export function listEnvironments(query?: GetQuery<"/api/v1/environments">, options?: Pick<RequestOptions, "signal">): Promise<EnvironmentList> { return request<EnvironmentList>("/api/v1/environments", { ...options, query }); }
export function listRevisions(query?: GetQuery<"/api/v1/revisions">, options?: Pick<RequestOptions, "signal">): Promise<RevisionList> { return request<RevisionList>("/api/v1/revisions", { ...options, query }); }
export function listDeployments(query?: GetQuery<"/api/v1/deployments">, options?: Pick<RequestOptions, "signal">): Promise<DeploymentList> { return request<DeploymentList>("/api/v1/deployments", { ...options, query }); }
export function getDeployment(id: string, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/deployments/{id}", "get", 200>> { return request(`/api/v1/deployments/${encodeURIComponent(id)}`, options); }
export function listRunners(query?: GetQuery<"/api/v1/runners">, options?: Pick<RequestOptions, "signal">): Promise<Schema<"RunnerListResponse">> { return request("/api/v1/runners", { ...options, query }); }
export function getRunner(id: string, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/runners/{id}", "get", 200>> { return request(`/api/v1/runners/${encodeURIComponent(id)}`, options); }
export function createRunnerEnrollment(input: JSONRequest<"/api/v1/runner-enrollments", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/runner-enrollments", "post", 201>> { return request("/api/v1/runner-enrollments", { ...options, method: "POST", body: input }); }
export function revokeRunnerToken(input: JSONRequest<"/api/v1/runners/revoke-token", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/runners/revoke-token", "post", 200>> { return request("/api/v1/runners/revoke-token", { ...options, method: "POST", body: input }); }
export function createService(input: JSONRequest<"/api/v1/services", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/services", "post", 201>> { return request("/api/v1/services", { ...options, method: "POST", body: input }); }
export function createEnvironment(input: JSONRequest<"/api/v1/environments", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/environments", "post", 201>> { return request("/api/v1/environments", { ...options, method: "POST", body: input }); }
export function createRevision(input: JSONRequest<"/api/v1/revisions", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/revisions", "post", 201>> { return request("/api/v1/revisions", { ...options, method: "POST", body: input }); }
export function createDeployment(input: JSONRequest<"/api/v1/deployments", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/deployments", "post", 201>> { return request("/api/v1/deployments", { ...options, method: "POST", body: input }); }
export function confirmDeployment(input: JSONRequest<"/api/v1/deployments/confirm", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/deployments/confirm", "post", 200>> { return request("/api/v1/deployments/confirm", { ...options, method: "POST", body: input }); }
export function cancelDeployment(input: JSONRequest<"/api/v1/deployments/cancel", "post">, options?: Pick<RequestOptions, "signal">): Promise<JSONResponse<"/api/v1/deployments/cancel", "post", 200>> { return request("/api/v1/deployments/cancel", { ...options, method: "POST", body: input }); }
