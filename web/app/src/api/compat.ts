import { useCallback, useRef, useState } from "react";
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
  createTemplate as createTemplateRequest,
  requestRun as requestRunRequest,
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

// The browser session cookie authenticates these requests. The retained token
// argument keeps the existing view contract stable while no token is sent.
export const createTemplate = (_token: string, input: Parameters<typeof createTemplateRequest>[0]) => createTemplateRequest(input);
export const updateTemplate = (_token: string, id: string, input: Omit<Parameters<typeof updateTemplateRequest>[0], "id">) => updateTemplateRequest({ id, ...input });
export const requestRun = (_token: string, input: Parameters<typeof requestRunRequest>[0]) => requestRunRequest(input);

export type MutateFn = <T>(key: string, action: () => Promise<T>, success: string) => Promise<T | undefined>;

/** Serializes the legacy screens' action state and refreshes generated-query data. */
export function useSnapshotMutation(): { busy: string; mutate: MutateFn } {
  const client = useQueryClient();
  const [busy, setBusy] = useState("");
  const pending = useRef(false);
  const mutate = useCallback<MutateFn>(async (key, action, success) => {
    if (pending.current) return undefined;
    pending.current = true;
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
      pending.current = false;
      setBusy("");
    }
  }, [client]);
  return { busy, mutate };
}
