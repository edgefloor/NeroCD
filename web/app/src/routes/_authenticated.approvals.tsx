import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { approvalsQuery, projectsQuery, runsQuery, templatesQuery } from "@/api";
import { apiSnapshot, useSnapshotMutation } from "@/api/compat";
import { ApprovalsView } from "@/pages/ApprovalsView";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/approvals")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(approvalsQuery()), context.queryClient.ensureQueryData(runsQuery()), context.queryClient.ensureQueryData(projectsQuery()), context.queryClient.ensureQueryData(templatesQuery())]),
  component: ApprovalsRoute,
});

function ApprovalsRoute() {
  const approvals = useQuery(approvalsQuery());
  const runs = useQuery(runsQuery());
  const projects = useQuery(projectsQuery());
  const templates = useQuery(templatesQuery());
  const { busy, mutate } = useSnapshotMutation();
  const queries = [approvals, runs, projects, templates];
  const error = queries.find((query) => query.isError)?.error;
  if (error) return <p role="alert">{error.message}</p>;
  return <ApprovalsView snapshot={apiSnapshot({ approvals: approvals.data, runs: runs.data, projects: projects.data, templates: templates.data })} token="" busy={busy} mutate={mutate} loading={queries.some((query) => query.isPending)} />;
}
