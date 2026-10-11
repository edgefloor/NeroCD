import { useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { approveRun, rejectRun, archiveProject, cancelDeployment, cancelRun, confirmDeployment, createDeployment, createEnvironment, createProject, createRepository, createRevision, createService, executeRunLogRetention, updateProject, updateRunLogRetentionPolicy, type TaskRunListResponse, type Deployment, type DeploymentList, type ApprovalListResponse, type Project, type ProjectListResponse } from "./resources";
import { queryKeys } from "./queries";

export function useProjectMutations() {
  const client = useQueryClient();
  const refreshProjects = () => client.invalidateQueries({ queryKey: ["projects"] });
  async function retainConfirmedProject(project: Project) {
    client.setQueriesData<ProjectListResponse | Project[]>({ queryKey: ["projects"] }, (cached) => {
      if (!cached) return cached;
      const replace = (item: Project) => item.id === project.id ? project : item;
      return Array.isArray(cached) ? cached.map(replace) : { ...cached, items: cached.items.map(replace) };
    });
    await refreshProjects();
  }
  return {
    create: useMutation({ mutationFn: (input: Parameters<typeof createProject>[0]) => createProject(input), onSuccess: async () => { await Promise.all([refreshProjects(), client.invalidateQueries({ queryKey: ["projectMembers"] })]); toast.success("Project created"); }, onError: (error) => toast.error(error.message) }),
    update: useMutation({ mutationFn: (input: Parameters<typeof updateProject>[0]) => updateProject(input), onSuccess: async (project) => { await retainConfirmedProject(project); toast.success("Project updated"); }, onError: (error) => toast.error(error.message) }),
    archive: useMutation({ mutationFn: (input: Parameters<typeof archiveProject>[0]) => archiveProject(input), onSuccess: async (project) => {
      client.setQueriesData<ProjectListResponse | Project[]>({ queryKey: ["projects"] }, (cached) => {
        if (!cached) return cached;
        const keep = (item: Project) => item.id !== project.id;
        if (Array.isArray(cached)) return cached.filter(keep);
        const items = cached.items.filter(keep);
        const removed = cached.items.length - items.length;
        return removed ? { ...cached, items, count: items.length, total: Math.max(0, cached.total - removed) } : cached;
      });
      await refreshProjects();
      toast.success("Project archived");
    }, onError: (error) => toast.error(error.message) }),
    repository: useMutation({ mutationFn: (input: Parameters<typeof createRepository>[0]) => createRepository(input), onSuccess: async () => { await client.invalidateQueries({ queryKey: ["repositories"] }); toast.success("Repository registered"); }, onError: (error) => toast.error(error.message) }),
  };
}

export type ProjectActions = {
  create: (input: Parameters<typeof createProject>[0]) => Promise<unknown>;
  update: (input: Parameters<typeof updateProject>[0]) => Promise<unknown>;
  archive: (input: Parameters<typeof archiveProject>[0]) => Promise<unknown>;
  repository: (input: Parameters<typeof createRepository>[0]) => Promise<unknown>;
};

export function useProjectActions(disabled = false): { busy: string; actions: ProjectActions } {
  const mutations = useProjectMutations();
  const pending = useRef(false);
  const [busy, setBusy] = useState("");
  async function perform<T>(key: string, action: () => Promise<T>): Promise<T | undefined> {
    if (disabled || pending.current) return undefined;
    pending.current = true;
    setBusy(key);
    try {
      return await action();
    } catch {
      // The resource mutation reports the error. Forms retain their draft.
      return undefined;
    } finally {
      pending.current = false;
      setBusy("");
    }
  }
  return { busy, actions: {
    create: (input) => perform("project", () => mutations.create.mutateAsync(input)),
    update: (input) => perform(`project:update:${input.id}`, () => mutations.update.mutateAsync(input)),
    archive: (input) => perform(`project:archive:${input.id}`, () => mutations.archive.mutateAsync(input)),
    repository: (input) => perform("repository", () => mutations.repository.mutateAsync(input)),
  } };
}

export function useRunMutations() {
  const client = useQueryClient();
  return { cancel: useMutation({
    mutationFn: (run_id: string) => cancelRun({ run_id }),
    onSuccess: async (result, runID) => {
      client.setQueryData(queryKeys.run(runID), result);
      client.setQueriesData<TaskRunListResponse>({ queryKey: ["runs"] }, (cached) => cached ? {
        ...cached,
        items: cached.items.map((run) => run.id === result.id ? result : run),
      } : cached);
      await Promise.all([
        client.invalidateQueries({ queryKey: ["runs"] }),
        client.invalidateQueries({ queryKey: queryKeys.run(runID) }),
        client.invalidateQueries({ predicate: (query) => query.queryKey[0] === "runLogs" && (query.queryKey[1] as { run_id?: string } | undefined)?.run_id === runID }),
      ]);
      toast.success("Run canceled");
    },
    onError: (error) => toast.error(error.message),
  }) };
}

export type ApprovalDecision = "approve" | "reject";

export function useApprovalDecision() {
  const client = useQueryClient();
  const pending = useRef(false);
  const mutation = useMutation({
    mutationFn: ({ runID, decision }: { runID: string; decision: ApprovalDecision }) => decision === "approve" ? approveRun({ run_id: runID }) : rejectRun({ run_id: runID }),
    onSuccess: async (approval) => {
      client.setQueriesData<ApprovalListResponse>({ queryKey: ["approvals"] }, (cached) => cached ? {
        ...cached,
        items: cached.items.map((item) => item.id === approval.id ? approval : item),
      } : cached);
      await Promise.all([
        client.invalidateQueries({ queryKey: ["approvals"] }),
        client.invalidateQueries({ queryKey: ["runs"] }),
        client.invalidateQueries({ queryKey: queryKeys.run(approval.run_id) }),
        client.invalidateQueries({ predicate: (query) => query.queryKey[0] === "runLogs" && (!(query.queryKey[1] as { run_id?: string } | undefined)?.run_id || (query.queryKey[1] as { run_id?: string } | undefined)?.run_id === approval.run_id) }),
      ]);
      toast.success(approval.status === "approved" ? "Run approved" : "Run rejected");
    },
    onError: (error) => toast.error(error.message),
    onSettled: () => { pending.current = false; },
  });
  return {
    busy: mutation.isPending,
    decide: (runID: string, decision: ApprovalDecision) => {
      if (pending.current) return;
      pending.current = true;
      mutation.mutate({ runID, decision });
    },
  };
}

/** Retention is deliberately manual.  The execution key is owned by the view
 * and survives a retry of the same confirmation; mutations never auto-retry. */
export function useRunLogRetentionMutations() {
  const client = useQueryClient();
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.runLogRetentionStatus });
  return {
    update: useMutation({ mutationFn: (input: Parameters<typeof updateRunLogRetentionPolicy>[0]) => updateRunLogRetentionPolicy(input), onSuccess: refresh }),
    execute: useMutation({ mutationFn: ({ policyVersion, requestID }: { policyVersion: number; requestID: string }) => executeRunLogRetention({ policy_version: policyVersion }, requestID), onSuccess: refresh }),
  };
}

/** Deployment mutations are deliberately non-retrying. The caller owns one stable
 * idempotency/request key for the lifetime of an operator intent. */
export function useDeploymentMutations() {
  const client = useQueryClient();
  const refresh = () => client.invalidateQueries({ queryKey: ["deployments"] });
  async function retainConfirmedDeployment(deployment: Deployment) {
    client.setQueryData(queryKeys.deployment(deployment.id), deployment);
    client.setQueriesData<DeploymentList | Deployment[]>({ queryKey: ["deployments"] }, (cached) => {
      if (!cached) return cached;
      const replace = (item: Deployment) => item.id === deployment.id ? deployment : item;
      return Array.isArray(cached) ? cached.map(replace) : { ...cached, items: cached.items.map(replace) };
    });
    await Promise.all([refresh(), client.invalidateQueries({ queryKey: queryKeys.deployment(deployment.id) })]);
  }
  return {
    createService: useMutation({ mutationFn: (input: Parameters<typeof createService>[0]) => createService(input), onSuccess: async () => { await client.invalidateQueries({ queryKey: ["services"] }); } }),
    createEnvironment: useMutation({ mutationFn: (input: Parameters<typeof createEnvironment>[0]) => createEnvironment(input), onSuccess: async () => { await client.invalidateQueries({ queryKey: ["environments"] }); } }),
    createRevision: useMutation({ mutationFn: (input: Parameters<typeof createRevision>[0]) => createRevision(input), onSuccess: async () => { await client.invalidateQueries({ queryKey: ["revisions"] }); } }),
    create: useMutation({ mutationFn: (input: Parameters<typeof createDeployment>[0]) => createDeployment(input), onSuccess: refresh }),
    confirm: useMutation({ mutationFn: (input: Parameters<typeof confirmDeployment>[0]) => confirmDeployment(input), onSuccess: retainConfirmedDeployment }),
    cancel: useMutation({ mutationFn: (input: Parameters<typeof cancelDeployment>[0]) => cancelDeployment(input), onSuccess: retainConfirmedDeployment }),
  };
}
