import { createFileRoute, useRouter } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { approvalsQuery, healthQuery, projectsQuery, runLogsQuery, runsQuery, shouldPollRunList, templatesQuery } from "@/api";
import { apiSnapshot, useSnapshotMutation } from "@/api/compat";
import { SkeletonCard } from "@/components/common/SkeletonCard";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { HomeView } from "@/pages/HomeView";
import { validateSearch } from "@/router/search";

const dashboardQueryKeys = [healthQuery().queryKey, projectsQuery().queryKey, templatesQuery().queryKey, runsQuery().queryKey, approvalsQuery().queryKey, runLogsQuery({ limit: 5, offset: 0 }).queryKey];

export const Route = createFileRoute("/_authenticated/")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(healthQuery()), context.queryClient.ensureQueryData(projectsQuery()), context.queryClient.ensureQueryData(templatesQuery()), context.queryClient.ensureQueryData(runsQuery()), context.queryClient.ensureQueryData(approvalsQuery()), context.queryClient.ensureQueryData(runLogsQuery({ limit: 5, offset: 0 }))]),
  pendingComponent: DashboardPending,
  errorComponent: DashboardUnavailable,
  component: HomeRoute,
});

function DashboardPending() {
  return <section aria-label="Loading dashboard" className="grid gap-6"><SkeletonCard rows={3} /><div className="grid gap-6 xl:grid-cols-[minmax(0,1.35fr)_minmax(340px,0.9fr)]"><SkeletonCard rows={5} /><SkeletonCard rows={3} /></div></section>;
}

function DashboardUnavailable({ reset }: { reset?: () => void }) {
  const router = useRouter();
  const client = useQueryClient();
  const retry = async () => { await Promise.all(dashboardQueryKeys.map((queryKey) => client.invalidateQueries({ queryKey }))); await router.invalidate(); reset?.(); };
  return <Card><CardHeader><CardTitle>Dashboard unavailable</CardTitle></CardHeader><CardContent className="grid gap-4"><p role="alert" className="text-sm text-muted-foreground">We could not load the dashboard. Try again to refresh its current activity.</p><div><Button type="button" onClick={() => void retry()}>Try again</Button></div></CardContent></Card>;
}

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
  if (error) return <DashboardUnavailable />;
  const snapshot = apiSnapshot({ health: health.data, projects: projects.data, templates: templates.data, runs: runs.data, approvals: approvals.data, logs: logs.data });
  return <HomeView snapshot={snapshot} workSnapshot={snapshot} query={q} onClearQuery={() => undefined} token="" busy={busy} mutate={mutate} loading={queries.some((query) => query.isPending)} />;
}
