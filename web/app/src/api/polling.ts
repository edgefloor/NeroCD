import { useQuery } from "@tanstack/react-query";
import { deploymentQuery, deploymentsQuery, runLogsQuery, runsQuery, shouldPollDeploymentList, shouldPollRunList, shouldPollSelectedLogs, type DeploymentFilters } from "./queries";

export function useRunsPollingQuery() {
  return useQuery({ ...runsQuery(), refetchInterval: (query) => shouldPollRunList(query.state.data) ? 3_000 : false });
}

export function useSelectedRunLogsPollingQuery(runID: string, runs: ReturnType<typeof useRunsPollingQuery>["data"]) {
  return useQuery({ ...runLogsQuery({ run_id: runID, limit: 100, offset: 0 }), refetchInterval: () => shouldPollSelectedLogs(runID, runs) ? 3_000 : false });
}

// Deployments are the only route-local resource which needs lifecycle polling.
// Query polling stops as soon as every listed deployment is terminal.
export function useDeploymentsPollingQuery(filters?: DeploymentFilters, enabled = true) {
  return useQuery({ ...deploymentsQuery(filters as DeploymentFilters), enabled, refetchInterval: (query) => shouldPollDeploymentList(query.state.data) ? 3_000 : false });
}

export function useDeploymentPollingQuery(id: string) {
  return useQuery({ ...deploymentQuery(id), refetchInterval: (query) => shouldPollDeploymentList(query.state.data ? [query.state.data] : undefined) ? 3_000 : false });
}
