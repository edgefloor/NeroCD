import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { archiveProject, cancelDeployment, cancelRun, confirmDeployment, createDeployment, createEnvironment, createProject, createRepository, createRevision, createService, executeRunLogRetention, updateProject, updateRunLogRetentionPolicy } from "./resources";
import { queryKeys } from "./queries";

export function useProjectMutations() {
  const client = useQueryClient();
  const refreshProjects = () => client.invalidateQueries({ queryKey: queryKeys.projects() });
  return {
    create: useMutation({ mutationFn: (input: Parameters<typeof createProject>[0]) => createProject(input), onSuccess: async () => { await refreshProjects(); toast.success("Project created"); }, onError: (error) => toast.error(error.message) }),
    update: useMutation({ mutationFn: (input: Parameters<typeof updateProject>[0]) => updateProject(input), onSuccess: async () => { await refreshProjects(); toast.success("Project updated"); }, onError: (error) => toast.error(error.message) }),
    archive: useMutation({ mutationFn: (input: Parameters<typeof archiveProject>[0]) => archiveProject(input), onSuccess: async () => { await refreshProjects(); toast.success("Project archived"); }, onError: (error) => toast.error(error.message) }),
    repository: useMutation({ mutationFn: (input: Parameters<typeof createRepository>[0]) => createRepository(input), onSuccess: async (_result, input) => { await Promise.all([client.invalidateQueries({ queryKey: queryKeys.repositories({ project_id: input.project_id }) }), client.invalidateQueries({ queryKey: queryKeys.repositories() })]); toast.success("Repository registered"); }, onError: (error) => toast.error(error.message) }),
  };
}

export function useRunMutations() {
  const client = useQueryClient();
  return { cancel: useMutation({ mutationFn: (run_id: string) => cancelRun({ run_id }), onSuccess: async (_result, runID) => { await Promise.all([client.invalidateQueries({ queryKey: queryKeys.runs() }), client.invalidateQueries({ queryKey: queryKeys.runLogs({ run_id: runID, limit: 100, offset: 0 }) })]); toast.success("Run canceled"); }, onError: (error) => toast.error(error.message) }) };
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
  return {
    createService: useMutation({ mutationFn: (input: Parameters<typeof createService>[0]) => createService(input), onSuccess: async () => { await client.invalidateQueries({ queryKey: ["services"] }); } }),
    createEnvironment: useMutation({ mutationFn: (input: Parameters<typeof createEnvironment>[0]) => createEnvironment(input), onSuccess: async () => { await client.invalidateQueries({ queryKey: ["environments"] }); } }),
    createRevision: useMutation({ mutationFn: (input: Parameters<typeof createRevision>[0]) => createRevision(input), onSuccess: async () => { await client.invalidateQueries({ queryKey: ["revisions"] }); } }),
    create: useMutation({ mutationFn: (input: Parameters<typeof createDeployment>[0]) => createDeployment(input), onSuccess: refresh }),
    confirm: useMutation({ mutationFn: (input: Parameters<typeof confirmDeployment>[0]) => confirmDeployment(input), onSuccess: refresh }),
    cancel: useMutation({ mutationFn: (input: Parameters<typeof cancelDeployment>[0]) => cancelDeployment(input), onSuccess: refresh }),
  };
}
