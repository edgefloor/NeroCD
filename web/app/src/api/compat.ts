import type {
  Approval,
  HealthResponse,
  Project,
  Repository,
  RunLog,
  TaskRun,
  TaskTemplate,
} from "./resources";
/** View-model boundary for the established snapshot-oriented screens.
 * Generated OpenAPI models remain the authoritative API contract. */
export type ApiSnapshot = {
  health: HealthResponse;
  projects: Project[];
  templates: TaskTemplate[];
  runs: TaskRun[];
  approvals: Approval[];
  logs: RunLog[];
  repositories: Repository[];
};

type PageOrItems<T> = T[] | { items: T[] };
type SnapshotInput = { [K in keyof ApiSnapshot]: ApiSnapshot[K] extends Array<infer T> ? PageOrItems<T> : ApiSnapshot[K] };
function items<T>(value: PageOrItems<T> | undefined): T[] {
  if (!value) return [];
  return Array.isArray(value) ? value : value.items;
}

export function apiSnapshot(snapshot: Partial<SnapshotInput>): ApiSnapshot {
  return {
    health: snapshot.health ?? { status: "ok" },
    projects: items(snapshot.projects),
    templates: items(snapshot.templates),
    runs: items(snapshot.runs),
    approvals: items(snapshot.approvals),
    logs: items(snapshot.logs),
    repositories: items(snapshot.repositories),
  };
}
