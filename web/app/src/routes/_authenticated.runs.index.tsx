import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { projectsQuery, runsQuery, templatesQuery, type RunLog, useRunsPollingQuery } from "@/api";
import { apiSnapshot, useSnapshotMutation } from "@/api/compat";
import { RunsView } from "@/pages/RunsView";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/runs/")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(runsQuery()), context.queryClient.ensureQueryData(projectsQuery()), context.queryClient.ensureQueryData(templatesQuery())]),
  component: RunsIndexRoute,
});

function RunsIndexRoute() { return <RunsRoute q={Route.useSearch().q} />; }

export function RunsRoute({ logs = [] }: { runId?: string; logs?: RunLog[]; q?: string } = {}) {
  const runs = useRunsPollingQuery();
  const projects = useQuery(projectsQuery());
  const templates = useQuery(templatesQuery());
  const { busy, mutate } = useSnapshotMutation();
  const queries = [runs, projects, templates];
  const error = queries.find((query) => query.isError)?.error;
  if (error) return <p role="alert">{error.message}</p>;
  return <RunsView snapshot={apiSnapshot({ runs: runs.data, projects: projects.data, templates: templates.data, logs })} token="" busy={busy} mutate={mutate} loading={queries.some((query) => query.isPending)} />;
}
