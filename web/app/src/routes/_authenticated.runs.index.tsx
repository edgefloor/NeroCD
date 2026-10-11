import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { allProjectMembersQuery, principalQuery, allProjectsQuery, allTemplatesQuery, runQuery, runsQuery, type RunLog, useRunsPollingQuery, useSelectedRunLogsPollingQuery } from "@/api";
import { apiSnapshot, useSnapshotMutation } from "@/api/compat";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { RunsView } from "@/pages/RunsView";
import { canMaintainProject } from "@/lib/permissions";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/runs/")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(runsQuery({ limit: 50, offset: 0 })), context.queryClient.ensureQueryData(allProjectsQuery()), context.queryClient.ensureQueryData(allTemplatesQuery())]),
  component: RunsIndexRoute,
});

function RunsIndexRoute() { return <RunsRoute q={Route.useSearch().q} />; }

type RunsRouteProps = { runId?: string; q?: string };
type SelectedLogsState = { runID: string; logs: RunLog[]; loading: boolean; error?: Error; hasPrevious: boolean; loadingPrevious: boolean; loadPrevious?: () => void };

export function RunsRoute({ runId, q }: RunsRouteProps = {}) {
  const page = usePage();
  const navigate = useNavigate();
  const runs = useRunsPollingQuery({ limit: page.limit, offset: page.offset });
  const projects = useQuery(allProjectsQuery());
  const principal = useQuery(principalQuery());
  const members = useQuery(allProjectMembersQuery());
  const templates = useQuery(allTemplatesQuery());
  const { busy, mutate } = useSnapshotMutation();
  const [selectedRunID, setSelectedRunID] = useState(runId ?? "");
  const [selectedLogs, setSelectedLogs] = useState<SelectedLogsState>({ runID: "", logs: [], loading: false, hasPrevious: false, loadingPrevious: false });
  const selectedRun = useQuery({
    ...runQuery(selectedRunID),
    enabled: Boolean(selectedRunID),
    refetchInterval: (query) => query.state.data && !["succeeded", "failed", "canceled", "rejected"].includes(query.state.data.status) ? 3_000 : false,
  });
  useEffect(() => {
    if (!runId) return;
    setSelectedRunID(runId);
    setSelectedLogs({ runID: "", logs: [], loading: false, hasPrevious: false, loadingPrevious: false });
  }, [runId]);
  const queries = [runs, projects, templates, principal, members, ...(selectedRunID ? [selectedRun] : [])];
  const error = queries.find((query) => query.isError)?.error;
  if (error) return <p role="alert">{error.message}</p>;
  const logsMatchSelection = selectedLogs.runID === selectedRunID;
  return (
    <>
      {selectedRunID ? <SelectedRunLogs runID={selectedRunID} follow={Boolean(selectedRun.data && !["succeeded", "failed", "canceled", "rejected"].includes(selectedRun.data.status))} onChange={setSelectedLogs} /> : null}
    <RunsView
      q={q}
      snapshot={apiSnapshot({ runs: runs.data, projects: projects.data, templates: templates.data })}
      token=""
      busy={busy}
      mutate={mutate}
      canOperateProject={(projectID) => canMaintainProject(principal.data, members.data ?? [], projectID)}
      loading={queries.some((query) => query.isPending)}
      selectedRunID={selectedRunID}
      selectedLogs={logsMatchSelection ? selectedLogs.logs : []}
      logsLoading={Boolean(selectedRunID) && (!logsMatchSelection || selectedLogs.loading)}
      logsError={logsMatchSelection ? selectedLogs.error : undefined}
      logsFollowing={Boolean(selectedRun.data && !["succeeded", "failed", "canceled", "rejected"].includes(selectedRun.data.status))}
      logsHavePrevious={logsMatchSelection && selectedLogs.hasPrevious}
      logsLoadingPrevious={logsMatchSelection && selectedLogs.loadingPrevious}
      onLoadPreviousLogs={logsMatchSelection ? selectedLogs.loadPrevious : undefined}
      selectedRun={selectedRun.data ?? undefined}
      onSelectRun={(nextRunID) => {
        if (runId && runId !== nextRunID) {
          void navigate({ to: "/runs/$runId", params: { runId: nextRunID }, search: true, resetScroll: false });
          return;
        }
        setSelectedRunID(nextRunID);
      }}
      onCloseLogs={() => {
        setSelectedRunID("");
        setSelectedLogs({ runID: "", logs: [], loading: false, hasPrevious: false, loadingPrevious: false });
      }}
    />
      <PageNavigation page={runs.data} label="runs" loading={runs.isFetching} onPrevious={page.previous} onNext={page.next} />
    </>
  );
}

function SelectedRunLogs({
  runID,
  follow,
  onChange,
}: {
  runID: string;
  follow: boolean;
  onChange: (value: SelectedLogsState) => void;
}) {
  const logs = useSelectedRunLogsPollingQuery(runID, follow);
  useEffect(() => {
    onChange({ runID, logs: logs.data, loading: logs.isPending, error: logs.isError ? logs.error : undefined, hasPrevious: logs.hasPrevious, loadingPrevious: logs.loadingPrevious, loadPrevious: logs.loadPrevious });
  }, [logs.data, logs.error, logs.hasPrevious, logs.isError, logs.isPending, logs.loadPrevious, logs.loadingPrevious, onChange, runID]);
  return null;
}
