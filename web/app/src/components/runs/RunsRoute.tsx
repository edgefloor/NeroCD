import { useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { allProjectMembersQuery, principalQuery, shouldPollRun, allProjectsQuery, allTemplatesQuery, runQuery, useRunMutations, useRunsPollingQuery, useSelectedRunLogsPollingQuery } from "@/api";
import { apiSnapshot } from "@/api/compat";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { RunsView } from "@/pages/RunsView";
import { canMaintainProject } from "@/lib/permissions";
import { Button } from "@/components/ui/button";

type RunsRouteProps = { runId?: string; q?: string };

export function RunsRoute({ runId, q }: RunsRouteProps = {}) {
  const page = usePage();
  const navigate = useNavigate();
  const runs = useRunsPollingQuery({ limit: page.limit, offset: page.offset });
  const projects = useQuery({ ...allProjectsQuery(), retryOnMount: false });
  const principal = useQuery({ ...principalQuery(), retryOnMount: false });
  const members = useQuery({ ...allProjectMembersQuery(), retryOnMount: false });
  const templates = useQuery({ ...allTemplatesQuery(), retryOnMount: false });
  const { cancel } = useRunMutations();
  const cancelPending = useRef(false);
  const [selectedRunID, setSelectedRunID] = useRunSelection(runId);
  const selectedRun = useQuery({
    ...runQuery(selectedRunID),
    enabled: Boolean(selectedRunID),
    retryOnMount: false,
    refetchInterval: (query) => query.state.status !== "error" && shouldPollRun(query.state.data) ? 3_000 : false,
  });
  const queries = [runs, projects, templates, principal, members, ...(selectedRunID ? [selectedRun] : [])];
  const failed = queries.filter((query) => query.isError);
  const recovery = failed.length > 0 ? <section className="space-y-3">
    <p role="alert">{runs.data ? "Unable to refresh run information. Loaded output remains available; retry before changing runs." : "Unable to load runs."}</p>
    <Button disabled={failed.some((query) => query.isFetching)} onClick={() => { for (const query of failed) void query.refetch(); }}>Retry runs</Button>
  </section> : null;
  const logs = useSelectedRunLogsPollingQuery(selectedRunID, shouldPollRun(selectedRun.data));
  return (
    <>
    {recovery}
    {runs.data || selectedRun.data || !runs.isError ? <RunsView
      q={q}
      offset={page.offset}
      snapshot={apiSnapshot({ runs: runs.data, projects: projects.data, templates: templates.data })}
      busy={failed.length > 0 || cancel.isPending}
      recovery={recovery}
      cancelError={cancel.isError && cancel.variables ? { key: cancel.variables, message: cancel.error.message } : undefined}
      onCancel={(runID) => {
        if (failed.length > 0 || cancelPending.current) return;
        cancelPending.current = true;
        cancel.mutate(runID, { onSettled: () => { cancelPending.current = false; } });
      }}
      canOperateProject={(projectID) => canMaintainProject(principal.data, members.data ?? [], projectID)}
      loading={queries.some((query) => query.isPending)}
      selectedRunID={selectedRunID}
      selectedLogs={logs.data}
      logsLoading={Boolean(selectedRunID) && logs.isPending}
      logsError={logs.error}
      onRetryLogs={logs.retry}
      logsRetrying={logs.isFetching}
      logsFollowing={shouldPollRun(selectedRun.data)}
      logsHavePrevious={logs.hasPrevious}
      logsLoadingPrevious={logs.loadingPrevious}
      logsPreviousError={logs.previousError}
      onLoadPreviousLogs={logs.loadPrevious}
      selectedRun={selectedRun.data ?? undefined}
      onSelectRun={(nextRunID) => {
        if (runId && runId !== nextRunID) {
          void navigate({ to: "/runs/$runId", params: { runId: nextRunID }, search: true, resetScroll: false });
          return;
        }
        setSelectedRunID(nextRunID);
      }}
      onCloseLogs={() => setSelectedRunID("")}
    /> : null}
      <PageNavigation page={runs.data ?? { count: 0, total: 0, limit: page.limit, offset: page.offset }} label="runs" loading={runs.isFetching} onFirst={page.first} onPrevious={page.previous} onNext={page.next} />
    </>
  );
}

function useRunSelection(runId?: string) {
  const [selectedRunID, setSelectedRunID] = useState(runId ?? "");
  const [observedRunID, setObservedRunID] = useState(runId);
  if (observedRunID !== runId) {
    setObservedRunID(runId);
    if (runId) {
      setSelectedRunID(runId);
    }
  }
  return [selectedRunID, setSelectedRunID] as const;
}
