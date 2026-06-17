export type HealthResponse = {
  status: "ok";
};

export type Principal = {
  id: string;
  email: string;
  name: string;
  roles: string[];
  provider: string;
};

export type Project = {
  id: string;
  name: string;
  description: string;
  created_at: string;
  archived_at?: string;
};

export type ProjectMember = {
  id: string;
  project_id: string;
  user_id: string;
  email: string;
  name: string;
  role: string;
  created_at: string;
  updated_at: string;
};

export type TaskTemplate = {
  id: string;
  project_id: string;
  name: string;
  kind: string;
  run_spec: RunSpec;
  workflow: Workflow;
  runner_tags: string[];
  requires_ack: boolean;
};

export type RunSpec = {
  type: string;
  inputs: Record<string, unknown>;
  repository?: RepositoryRef;
  process?: ProcessSpec;
  artifacts?: ArtifactSpec[];
  secrets?: SecretBinding[];
  workflow?: Workflow;
};

export type RepositoryRef = {
  id?: string;
  url?: string;
  provider?: string;
  ref?: string;
  path?: string;
};

export type ProcessSpec = {
  command: string[];
  working_dir?: string;
  environment?: Record<string, string>;
  timeout_seconds?: number;
};

export type ArtifactSpec = {
  name: string;
  path: string;
  required: boolean;
  retention?: string;
};

export type SecretBinding = {
  name: string;
  provider: string;
  reference: string;
  target: string;
};

export type Workflow = {
  steps: WorkflowStep[];
};

export type WorkflowStep = {
  id: string;
  name: string;
  run_spec: RunSpec;
  depends_on?: string[];
  requires_ack: boolean;
};

export type WorkflowState = {
  current_step_id?: string;
  steps: WorkflowStepState[];
};

export type WorkflowStepState = {
  id: string;
  name: string;
  status: string;
  started_at?: string;
  finished_at?: string;
};

export type TaskRun = {
  id: string;
  project_id: string;
  template_id?: string;
  run_spec: RunSpec;
  workflow: Workflow;
  workflow_state: WorkflowState;
  runner_tags: string[];
  status: string;
  requested_by: string;
  started_at: string;
  finished_at?: string;
};

export type RunLog = {
  id: string;
  run_id: string;
  sequence: number;
  stream: string;
  message: string;
  created_at: string;
};

export type ArtifactRecord = {
  id: string;
  run_id: string;
  lease_id: string;
  name: string;
  path: string;
  found: boolean;
  required: boolean;
  size: number;
  kind: string;
  created_at: string;
};

export type Capability = {
  name: string;
  status: string;
  description: string;
};

export type Repository = {
  id: string;
  project_id: string;
  name: string;
  url: string;
  provider: string;
  default_ref: string;
  created_at: string;
};

export type Approval = {
  id: string;
  run_id: string;
  status: string;
  requested_by: string;
  approved_by?: string;
  created_at: string;
  approved_at?: string;
};

export type AuditEvent = {
  id: string;
  actor_id: string;
  action: string;
  target_id: string;
  metadata: Record<string, unknown>;
  created_at: string;
};

export type ListEnvelope<T> = {
  items: T[];
  limit?: number;
  offset?: number;
  count?: number;
  total?: number;
};

export type ApiSnapshot = {
  health: HealthResponse;
  principal: Principal;
  projects: Project[];
  projectMembers: ProjectMember[];
  repositories: Repository[];
  templates: TaskTemplate[];
  runs: TaskRun[];
  logs: RunLog[];
  artifacts: ArtifactRecord[];
  approvals: Approval[];
  auditEvents: AuditEvent[];
  capabilities: Capability[];
};

export type CreatedSession = {
  session: {
    id: string;
    user_id: string;
    expires_at: string;
    created_at: string;
  };
  token: string;
};

async function getJSON<T>(path: string, token: string): Promise<T> {
  const response = await fetch(path, {
    headers: {
      Accept: "application/json",
      Authorization: `Bearer ${token}`,
    },
  });
  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText}`);
  }
  return response.json() as Promise<T>;
}

async function sendJSON<T>(path: string, token: string, method: "POST" | "PATCH", body: unknown): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
      Authorization: `Bearer ${token}`,
    },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      const envelope = (await response.json()) as { error?: string };
      message = envelope.error ?? message;
    } catch {
      // Keep the HTTP status text when the server does not return JSON.
    }
    throw new Error(message);
  }
  return response.json() as Promise<T>;
}

async function sendNoContent(path: string, token: string, method: "DELETE"): Promise<void> {
  const response = await fetch(path, {
    method,
    headers: {
      Accept: "application/json",
      Authorization: `Bearer ${token}`,
    },
  });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      const envelope = (await response.json()) as { error?: string };
      message = envelope.error ?? message;
    } catch {
      // Keep the HTTP status text when the server does not return JSON.
    }
    throw new Error(message);
  }
}

export async function createSession(email: string, password: string): Promise<CreatedSession> {
  const response = await fetch("/api/v1/sessions", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
    },
    body: JSON.stringify({ email, password }),
  });
  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText}`);
  }
  return response.json() as Promise<CreatedSession>;
}

export function revokeSession(token: string): Promise<void> {
  return sendNoContent("/api/v1/sessions", token, "DELETE");
}

export async function loadSnapshot(token: string): Promise<ApiSnapshot> {
  const [health, principal, projects, projectMembers, repositories, templates, runs, logs, artifacts, approvals, auditEvents, capabilities] = await Promise.all([
    fetch("/api/v1/health", { headers: { Accept: "application/json" } }).then((response) => response.json() as Promise<HealthResponse>),
    getJSON<Principal>("/api/v1/me", token),
    getJSON<ListEnvelope<Project>>("/api/v1/projects", token),
    getJSON<ListEnvelope<ProjectMember>>("/api/v1/project-members", token),
    getJSON<ListEnvelope<Repository>>("/api/v1/repositories", token),
    getJSON<ListEnvelope<TaskTemplate>>("/api/v1/templates", token),
    getJSON<ListEnvelope<TaskRun>>("/api/v1/runs", token),
    getJSON<ListEnvelope<RunLog>>("/api/v1/run-logs", token),
    getJSON<ListEnvelope<ArtifactRecord>>("/api/v1/artifacts", token),
    getJSON<ListEnvelope<Approval>>("/api/v1/approvals", token),
    getJSON<ListEnvelope<AuditEvent>>("/api/v1/audit-events", token),
    getJSON<ListEnvelope<Capability>>("/api/v1/capabilities", token),
  ]);

  return {
    health,
    principal,
    projects: projects.items,
    projectMembers: projectMembers.items,
    repositories: repositories.items,
    templates: templates.items,
    runs: runs.items,
    logs: logs.items,
    artifacts: artifacts.items,
    approvals: approvals.items,
    auditEvents: auditEvents.items,
    capabilities: capabilities.items,
  };
}

export type ProjectInput = {
  name: string;
  description?: string;
};

export type ProjectMemberInput = {
  project_id: string;
  email: string;
  role: string;
};

export type TemplateInput = {
  project_id: string;
  name: string;
  kind: string;
  run_spec: RunSpec;
  workflow?: Workflow;
  runner_tags?: string[];
  requires_ack?: boolean;
};

export type RepositoryInput = {
  project_id: string;
  name: string;
  url: string;
  provider?: string;
  default_ref?: string;
};

export type RunRequestInput = {
  project_id?: string;
  template_id?: string;
  run_spec?: RunSpec;
  workflow?: Workflow;
  runner_tags?: string[];
  requires_ack?: boolean;
};

export function createProject(token: string, input: ProjectInput): Promise<Project> {
  return sendJSON<Project>("/api/v1/projects", token, "POST", input);
}

export function updateProject(token: string, id: string, input: ProjectInput): Promise<Project> {
  return sendJSON<Project>("/api/v1/projects", token, "PATCH", { id, ...input });
}

export function archiveProject(token: string, id: string): Promise<Project> {
  return sendJSON<Project>("/api/v1/projects/archive", token, "POST", { id });
}

export function upsertProjectMember(token: string, input: ProjectMemberInput): Promise<ProjectMember> {
  return sendJSON<ProjectMember>("/api/v1/project-members", token, "POST", input);
}

export function createRepository(token: string, input: RepositoryInput): Promise<Repository> {
  return sendJSON<Repository>("/api/v1/repositories", token, "POST", input);
}

export function createTemplate(token: string, input: TemplateInput): Promise<TaskTemplate> {
  return sendJSON<TaskTemplate>("/api/v1/templates", token, "POST", { workflow: { steps: [] }, runner_tags: [], requires_ack: false, ...input });
}

export function updateTemplate(token: string, id: string, input: TemplateInput): Promise<TaskTemplate> {
  return sendJSON<TaskTemplate>("/api/v1/templates", token, "PATCH", { id, workflow: { steps: [] }, runner_tags: [], requires_ack: false, ...input });
}

export function requestRun(token: string, input: RunRequestInput): Promise<TaskRun> {
  return sendJSON<TaskRun>("/api/v1/runs", token, "POST", input);
}

export function approveRun(token: string, runID: string): Promise<Approval> {
  return sendJSON<Approval>("/api/v1/runs/approve", token, "POST", { run_id: runID });
}

export function rejectRun(token: string, runID: string): Promise<Approval> {
  return sendJSON<Approval>("/api/v1/runs/reject", token, "POST", { run_id: runID });
}

export function cancelRun(token: string, runID: string): Promise<TaskRun> {
  return sendJSON<TaskRun>("/api/v1/runs/cancel", token, "POST", { run_id: runID });
}
