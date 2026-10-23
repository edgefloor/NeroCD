import { createFileRoute, useRouter } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, allProjectMembersQuery, principalQuery, useApprovalDecision, approvalsQuery, healthQuery, projectsQuery, runLogsQuery, runsQuery, shouldPollRunList, templatesQuery } from "@/api";
import { apiSnapshot } from "@/api/compat";
import { SkeletonCard } from "@/components/common/SkeletonCard";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { HomeView, HostingStart } from "@/pages/HomeView";
import { canMaintainProject } from "@/lib/permissions";
import { validateSearch } from "@/router/search";

const dashboardQueryKeys = [principalQuery().queryKey, allProjectMembersQuery().queryKey, healthQuery().queryKey, projectsQuery().queryKey, templatesQuery().queryKey, runsQuery().queryKey, approvalsQuery().queryKey, runLogsQuery({ limit: 5, offset: 0 }).queryKey];

export const Route = createFileRoute("/_authenticated/")({
  validateSearch,
  loader: ({ context }) => context.queryClient.ensureQueryData(principalQuery()),
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
  const navigate = Route.useNavigate();
  const principal = useQuery(principalQuery());
  const verified = Boolean(principal.data && !principal.isPending && !principal.isFetching && !principal.isError);
  const scope = JSON.stringify([principal.data?.provider, principal.data?.id, [...(principal.data?.roles ?? [])].sort()]);
  const scoped = <T extends { queryKey: readonly unknown[] }>(options: T) => ({ ...options, queryKey: [...options.queryKey, "home", scope], enabled: verified, gcTime: 0 });
  const health = useQuery(scoped(healthQuery()));
  const projects = useQuery(scoped(projectsQuery()));
  const templates = useQuery(scoped(templatesQuery()));
  const runs = useQuery({ ...scoped(runsQuery()), refetchInterval: (query) => query.state.status !== "error" && shouldPollRunList(query.state.data) ? 3_000 : false });
  const approvals = useQuery(scoped(approvalsQuery()));
  const logs = useQuery(scoped(runLogsQuery({ limit: 5, offset: 0 })));
  const { busy, decide, error } = useApprovalDecision();
  const members = useQuery(scoped(allProjectMembersQuery()));
  const queries = [health, projects, templates, runs, approvals, logs, principal, members];
  const failed = queries.filter((query) => query.isError);
  const hasSnapshot = queries.every((query) => query.data !== undefined);
  const denied = failed.some((query) => query.error instanceof ApiError && [401, 403].includes(query.error.status));
  if (!verified || denied) return <DashboardUnavailable />;
  if (failed.length > 0 && !hasSnapshot) return <div className="grid min-w-0 gap-4"><HostingStart /><DashboardUnavailable /></div>;
  const snapshot = apiSnapshot({ health: health.data, projects: projects.data, templates: templates.data, runs: runs.data, approvals: approvals.data, logs: logs.data });
  return <div className="grid min-w-0 gap-4">
    {failed.length > 0 ? <section className="space-y-3">
      <p role="alert">Unable to refresh dashboard. Showing previously loaded information.</p>
      <Button disabled={failed.some((query) => query.isFetching)} onClick={() => { for (const query of failed) void query.refetch(); }}>Retry dashboard</Button>
    </section> : null}
    <HomeView
      snapshot={snapshot}
      workSnapshot={snapshot}
      query={q}
      onClearQuery={() => void navigate({ search: (previous) => ({ ...previous, q: undefined }), replace: true })}
      busy={busy || failed.length > 0}
      decisionError={error} onDecide={(runID, decision) => { if (!failed.length) decide(runID, decision); }}
      canOperateProject={(projectID) => canMaintainProject(principal.data, members.data ?? [], projectID)}
      loading={queries.some((query) => query.isPending)}
      totals={{ projects: projects.data?.total ?? 0, templates: templates.data?.total ?? 0, logs: logs.data?.total ?? 0 }}
    />
  </div>;
}
