import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { approvalsQuery, healthQuery, projectsQuery, runLogsQuery, runsQuery, shouldPollRunList, templatesQuery } from "@/api";
import { apiSnapshot, useSnapshotMutation } from "@/api/compat";
import { HomeView } from "@/pages/HomeView";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(healthQuery()), context.queryClient.ensureQueryData(projectsQuery()), context.queryClient.ensureQueryData(templatesQuery()), context.queryClient.ensureQueryData(runsQuery()), context.queryClient.ensureQueryData(approvalsQuery()), context.queryClient.ensureQueryData(runLogsQuery({ limit: 5, offset: 0 }))]),
  component: HomeRoute,
});

function HomeRoute() {
  const { q } = Route.useSearch();
  const health = useQuery(healthQuery());
  const projects = useQuery(projectsQuery());
  const templates = useQuery(templatesQuery());
  const runs = useQuery({ ...runsQuery(), refetchInterval: (query) => shouldPollRunList(query.state.data) ? 3_000 : false });
  const approvals = useQuery(approvalsQuery());
  const logs = useQuery(runLogsQuery({ limit: 5, offset: 0 }));
  const { busy, mutate } = useSnapshotMutation();
  const queries = [health, projects, templates, runs, approvals, logs];
  const error = queries.find((query) => query.isError)?.error;
  if (error) return <p role="alert">{error.message}</p>;
  const snapshot = apiSnapshot({ health: health.data, projects: projects.data, templates: templates.data, runs: runs.data, approvals: approvals.data, logs: logs.data });
  return <HomeView snapshot={snapshot} workSnapshot={snapshot} query={q} onClearQuery={() => undefined} token="" busy={busy} mutate={mutate} loading={queries.some((query) => query.isPending)} />;
}
