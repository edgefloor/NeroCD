import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { projectsQuery, runsQuery, templatesQuery, type RunLog, useRunsPollingQuery, useSelectedRunLogsPollingQuery } from "@/api";
import { apiSnapshot, useSnapshotMutation } from "@/api/compat";
import { RunsView } from "@/pages/RunsView";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/runs/")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(runsQuery()), context.queryClient.ensureQueryData(projectsQuery()), context.queryClient.ensureQueryData(templatesQuery())]),
  component: RunsIndexRoute,
});

function RunsIndexRoute() { return <RunsRoute q={Route.useSearch().q} />; }

type RunsRouteProps = { runId?: string; q?: string };
type SelectedLogsState = { runID: string; logs: RunLog[]; loading: boolean; error?: Error };

export function RunsRoute({ runId, q: _q }: RunsRouteProps = {}) {
  const runs = useRunsPollingQuery();
  const projects = useQuery(projectsQuery());
  const templates = useQuery(templatesQuery());
  const { busy, mutate } = useSnapshotMutation();
  const [selectedRunID, setSelectedRunID] = useState(runId ?? "");
  const [selectedLogs, setSelectedLogs] = useState<SelectedLogsState>({ runID: "", logs: [], loading: false });
  const queries = [runs, projects, templates];
  const error = queries.find((query) => query.isError)?.error;
  if (error) return <p role="alert">{error.message}</p>;
  const logsMatchSelection = selectedLogs.runID === selectedRunID;
  return (
    <>
      {selectedRunID ? <SelectedRunLogs runID={selectedRunID} runs={runs.data} onChange={setSelectedLogs} /> : null}
    <RunsView
      snapshot={apiSnapshot({ runs: runs.data, projects: projects.data, templates: templates.data })}
      token=""
      busy={busy}
      mutate={mutate}
      loading={queries.some((query) => query.isPending)}
      selectedRunID={selectedRunID}
      selectedLogs={logsMatchSelection ? selectedLogs.logs : []}
      logsLoading={Boolean(selectedRunID) && (!logsMatchSelection || selectedLogs.loading)}
      logsError={logsMatchSelection ? selectedLogs.error : undefined}
      logsFollowing={Boolean(selectedRunID) && !["succeeded", "failed", "canceled", "rejected"].includes(runs.data?.find((run) => run.id === selectedRunID)?.status ?? "")}
      onSelectRun={setSelectedRunID}
      onCloseLogs={() => {
        setSelectedRunID("");
        setSelectedLogs({ runID: "", logs: [], loading: false });
      }}
    />
    </>
  );
}

function SelectedRunLogs({
  runID,
  runs,
  onChange,
}: {
  runID: string;
  runs: ReturnType<typeof useRunsPollingQuery>["data"];
  onChange: (value: SelectedLogsState) => void;
}) {
  const logs = useSelectedRunLogsPollingQuery(runID, runs);
  useEffect(() => {
    onChange({ runID, logs: logs.data ?? [], loading: logs.isPending, error: logs.isError ? logs.error : undefined });
  }, [logs.data, logs.error, logs.isError, logs.isPending, onChange, runID]);
  return null;
}
