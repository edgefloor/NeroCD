import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { RunLog } from "./resources";
import { deploymentQuery, deploymentsQuery, runLogsQuery, runsQuery, shouldPollDeploymentList, shouldPollRunList, type DeploymentFilters, type RunFilters } from "./queries";

export function useRunsPollingQuery(filters?: RunFilters) {
  return useQuery({ ...runsQuery(filters), retryOnMount: false, refetchInterval: (query) => query.state.status !== "error" && shouldPollRunList(query.state.data) ? 3_000 : false });
}

type LogWindow = { runID: string; logs: RunLog[]; firstOffset: number; nextOffset?: number };
const logPageSize = 100;

export function useSelectedRunLogsPollingQuery(runID: string, follow: boolean) {
  const client = useQueryClient();
  const [window, setWindow] = useState<LogWindow>({ runID, logs: [], firstOffset: 0 });
  const previousRequests = useRef(new Set<string>());
  const [previousStates, setPreviousStates] = useState<Record<string, { loading: boolean; error?: Error }>>({});
  const current: LogWindow = window.runID === runID ? window : { runID, logs: [], firstOffset: 0 };
  const probe = useQuery({ ...runLogsQuery({ run_id: runID, limit: 1, offset: 0 }), enabled: Boolean(runID) });

  useEffect(() => setWindow({ runID, logs: [], firstOffset: 0 }), [runID]);
  useEffect(() => {
    if (!runID || !probe.data || current.nextOffset !== undefined) return;
    const tailOffset = Math.max(0, probe.data.total - logPageSize);
    setWindow({ runID, logs: [], firstOffset: tailOffset, nextOffset: tailOffset });
  }, [current.nextOffset, probe.data, runID]);

  const page = useQuery({
    ...runLogsQuery({ run_id: runID, limit: logPageSize, offset: current.nextOffset ?? 0 }),
    enabled: Boolean(runID) && current.nextOffset !== undefined,
    refetchInterval: (query) => follow && !probe.isError && query.state.status !== "error" ? 3_000 : false,
  });
  const wasFollowing = useRef(follow);
  useEffect(() => {
    if (wasFollowing.current && !follow && current.nextOffset !== undefined) void page.refetch();
    wasFollowing.current = follow;
  }, [current.nextOffset, follow, page.refetch]);
  useEffect(() => {
    if (!page.data || current.nextOffset === undefined || page.data.offset !== current.nextOffset) return;
    setWindow((previous) => {
      if (previous.runID !== runID) return previous;
      const byID = new Map(previous.logs.map((log) => [log.id, log]));
      for (const log of page.data.items) byID.set(log.id, log);
      return {
        ...previous,
        logs: [...byID.values()].sort((left, right) => left.sequence - right.sequence),
        nextOffset: page.data.offset + page.data.count,
      };
    });
  }, [current.nextOffset, page.data, runID]);

  const loadPrevious = useCallback(async () => {
    if (!runID || current.firstOffset === 0 || previousRequests.current.has(runID)) return;
    previousRequests.current.add(runID);
    setPreviousStates((states) => ({ ...states, [runID]: { loading: true } }));
    try {
      const offset = Math.max(0, current.firstOffset - logPageSize);
      const previous = await client.fetchQuery(runLogsQuery({ run_id: runID, limit: current.firstOffset - offset, offset }));
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
  }, [client, current.firstOffset, runID]);

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
    previousError: previousStates[runID]?.error,
    hasPrevious: current.firstOffset > 0,
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
