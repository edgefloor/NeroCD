import { useCallback, useRef, useState, type Dispatch, type SetStateAction } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { listRunLogs, type RunLog } from "./resources";
import { deploymentQuery, deploymentsQuery, runLogsQuery, runsQuery, shouldPollDeploymentList, shouldPollRunList, type DeploymentFilters, type RunFilters } from "./queries";

export function useRunsPollingQuery(filters?: RunFilters) {
  return useQuery({ ...runsQuery(filters), retryOnMount: false, refetchInterval: (query) => query.state.status !== "error" && shouldPollRunList(query.state.data) ? 3_000 : false });
}

type LogWindow = { runID: string; logs: RunLog[]; firstOffset: number; nextOffset?: number };
const logPageSize = 100;

export function useSelectedRunLogsPollingQuery(runID: string, follow: boolean) {
  const probe = useQuery({ ...runLogsQuery({ run_id: runID, limit: 1, offset: 0 }), enabled: Boolean(runID) });
  const [current, setWindow] = useLogWindow(runID, probe.data?.total);

  const pageFilters = { run_id: runID, limit: logPageSize, offset: current.nextOffset ?? 0 };
  const pageOptions = runLogsQuery(pageFilters);
  const page = useQuery({
    // A terminal transition takes one final snapshot at the current cursor.
    // Query identity schedules that read once, even if the offset is unchanged.
    queryKey: [...pageOptions.queryKey, { follow }],
    queryFn: ({ signal }) => listRunLogs(pageFilters, { signal }),
    enabled: Boolean(runID) && current.nextOffset !== undefined,
    refetchInterval: (query) => follow && !probe.isError && query.state.status !== "error" ? 3_000 : false,
  });
  if (page.data && page.data.count > 0 && page.data.offset === current.nextOffset) {
    const byID = new Map(current.logs.map((log) => [log.id, log]));
    for (const log of page.data.items) byID.set(log.id, log);
    setWindow({ ...current, logs: [...byID.values()].sort((left, right) => left.sequence - right.sequence), nextOffset: page.data.offset + page.data.count });
  }

  const previous = usePreviousLogPage(runID, current.firstOffset, setWindow);

  const retry = useCallback(async () => {
    if (!runID) return;
    await probe.refetch();
    if (current.nextOffset !== undefined) await page.refetch();
  }, [runID, probe.refetch, page.refetch, current.nextOffset]);

  return {
    retry,
    isFetching: probe.isFetching || page.isFetching,
    data: current.logs,
    isPending: !probe.isError && !page.isError && (probe.isPending || (current.logs.length === 0 && page.isPending)),
    isError: probe.isError || page.isError,
    error: (probe.error ?? page.error) ?? undefined,
    ...previous,
  };
}

function useLogWindow(runID: string, total: number | undefined) {
  const [window, setWindow] = useState<LogWindow>({ runID, logs: [], firstOffset: 0 });
  const current: LogWindow = window.runID === runID ? window : { runID, logs: [], firstOffset: 0 };

  if (window.runID !== runID) setWindow(current);
  if (runID && total !== undefined && current.nextOffset === undefined) {
    const tailOffset = Math.max(0, total - logPageSize);
    setWindow({ runID, logs: [], firstOffset: tailOffset, nextOffset: tailOffset });
  }

  return [current, setWindow] as const;
}

function usePreviousLogPage(runID: string, firstOffset: number, setWindow: Dispatch<SetStateAction<LogWindow>>) {
  const client = useQueryClient();
  const previousRequests = useRef(new Set<string>());
  const [previousStates, setPreviousStates] = useState<Record<string, { loading: boolean; error?: Error }>>({});
  const loadPrevious = useCallback(async () => {
    if (!runID || firstOffset === 0 || previousRequests.current.has(runID)) return;
    previousRequests.current.add(runID);
    setPreviousStates((states) => ({ ...states, [runID]: { loading: true } }));
    try {
      const offset = Math.max(0, firstOffset - logPageSize);
      const previous = await client.fetchQuery(runLogsQuery({ run_id: runID, limit: firstOffset - offset, offset }));
      setWindow((value) => {
        if (value.runID !== runID) return value;
        const byID = new Map(previous.items.map((log) => [log.id, log]));
        for (const log of value.logs) byID.set(log.id, log);
        return { ...value, firstOffset: offset, logs: [...byID.values()].sort((left, right) => left.sequence - right.sequence) };
      });
    } catch (cause) {
      const error = cause instanceof Error ? cause : new Error("Unable to load earlier output");
      setPreviousStates((states) => ({ ...states, [runID]: { loading: true, error } }));
    } finally {
      previousRequests.current.delete(runID);
      setPreviousStates((states) => ({ ...states, [runID]: { ...states[runID], loading: false } }));
    }
  }, [client, firstOffset, runID, setWindow]);

  return {
    previousError: previousStates[runID]?.error,
    hasPrevious: firstOffset > 0,
    loadingPrevious: previousStates[runID]?.loading ?? false,
    loadPrevious,
  };
}

// Stop lifecycle polling on errors or when all deployments are terminal.
export function useDeploymentsPollingQuery(filters?: DeploymentFilters, enabled = true) {
  return useQuery({ ...deploymentsQuery(filters as DeploymentFilters), enabled, retryOnMount: false, refetchInterval: (query) => query.state.status !== "error" && shouldPollDeploymentList(query.state.data) ? 3_000 : false });
}

export function useDeploymentPollingQuery(id: string) {
  return useQuery({ ...deploymentQuery(id), retryOnMount: false, refetchInterval: (query) => query.state.status !== "error" && shouldPollDeploymentList(query.state.data ? { items: [query.state.data], count: 1, total: 1, limit: 1, offset: 0 } : undefined) ? 3_000 : false });
}
