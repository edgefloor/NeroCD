import { useCallback, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type {
  Approval,
  HealthResponse,
  Project,
  Repository,
  RunLog,
  TaskRun,
  TaskTemplate,
} from "./resources";
import {
  approveRun as approveRunRequest,
  archiveProject as archiveProjectRequest,
  cancelRun as cancelRunRequest,
  createProject as createProjectRequest,
  createRepository as createRepositoryRequest,
  createTemplate as createTemplateRequest,
  rejectRun as rejectRunRequest,
  requestRun as requestRunRequest,
  updateProject as updateProjectRequest,
  updateTemplate as updateTemplateRequest,
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

export function apiSnapshot(snapshot: Partial<ApiSnapshot>): ApiSnapshot {
  return {
    health: snapshot.health ?? { status: "ok" },
    projects: snapshot.projects ?? [],
    templates: snapshot.templates ?? [],
    runs: snapshot.runs ?? [],
    approvals: snapshot.approvals ?? [],
    logs: snapshot.logs ?? [],
    repositories: snapshot.repositories ?? [],
  };
}

// The browser session cookie authenticates these requests. The retained token
// argument keeps the existing view contract stable while no token is sent.
export const createProject = (_token: string, input: Parameters<typeof createProjectRequest>[0]) => createProjectRequest(input);
export const updateProject = (_token: string, id: string, input: Omit<Parameters<typeof updateProjectRequest>[0], "id">) => updateProjectRequest({ id, ...input });
export const archiveProject = (_token: string, id: string) => archiveProjectRequest({ id });
export const createRepository = (_token: string, input: Parameters<typeof createRepositoryRequest>[0]) => createRepositoryRequest(input);
export const createTemplate = (_token: string, input: Parameters<typeof createTemplateRequest>[0]) => createTemplateRequest(input);
export const updateTemplate = (_token: string, id: string, input: Omit<Parameters<typeof updateTemplateRequest>[0], "id">) => updateTemplateRequest({ id, ...input });
export const requestRun = (_token: string, input: Parameters<typeof requestRunRequest>[0]) => requestRunRequest(input);
export const approveRun = (_token: string, runID: string) => approveRunRequest({ run_id: runID });
export const rejectRun = (_token: string, runID: string) => rejectRunRequest({ run_id: runID });
export const cancelRun = (_token: string, runID: string) => cancelRunRequest({ run_id: runID });

export type MutateFn = <T>(key: string, action: () => Promise<T>, success: string) => Promise<T | undefined>;

/** Serializes the legacy screens' action state and refreshes generated-query data. */
export function useSnapshotMutation(): { busy: string; mutate: MutateFn } {
  const client = useQueryClient();
  const [busy, setBusy] = useState("");
  const mutate = useCallback<MutateFn>(async (key, action, success) => {
    setBusy(key);
    try {
      const result = await action();
      await client.invalidateQueries();
      toast.success(success);
      return result;
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
      return undefined;
    } finally {
      setBusy("");
    }
  }, [client]);
  return { busy, mutate };
}
