import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { projectsQuery, runLogsQuery, runsQuery, templatesQuery } from "@/api";
import { apiSnapshot, useSnapshotMutation } from "@/api/compat";
import { TemplatesView } from "@/pages/TemplatesView";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/templates")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(projectsQuery()), context.queryClient.ensureQueryData(templatesQuery())]),
  component: TemplatesRoute,
});

function TemplatesRoute() {
  const projects = useQuery(projectsQuery());
  const templates = useQuery(templatesQuery());
  const runs = useQuery(runsQuery());
  const logs = useQuery(runLogsQuery({ limit: 100, offset: 0 }));
  const { busy, mutate } = useSnapshotMutation();
  const queries = [projects, templates, runs, logs];
  const error = queries.find((query) => query.isError)?.error;
  if (error) return <p role="alert">{error.message}</p>;
  return <TemplatesView snapshot={apiSnapshot({ projects: projects.data, templates: templates.data, runs: runs.data, logs: logs.data })} token="" busy={busy} mutate={mutate} loading={queries.some((query) => query.isPending)} />;
}
