import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { allProjectMembersQuery, principalQuery, allProjectsQuery, templatesQuery } from "@/api";
import { apiSnapshot, useSnapshotMutation } from "@/api/compat";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { TemplatesView } from "@/pages/TemplatesView";
import { canMaintainProject } from "@/lib/permissions";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/templates")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(allProjectsQuery()), context.queryClient.ensureQueryData(templatesQuery({ limit: 50, offset: 0 }))]),
  component: TemplatesRoute,
});

function TemplatesRoute() {
  const { q } = Route.useSearch();
  const navigate = Route.useNavigate();
  const page = usePage();
  const projects = useQuery(allProjectsQuery());
  const templates = useQuery(templatesQuery({ limit: page.limit, offset: page.offset }));
  const { busy, mutate } = useSnapshotMutation();
  const principal = useQuery(principalQuery());
  const members = useQuery(allProjectMembersQuery());
  const queries = [projects, templates, principal, members];
  const error = queries.find((query) => query.isError)?.error;
  if (error) return <p role="alert">{error.message}</p>;
  return <><TemplatesView canOperateProject={(projectID) => canMaintainProject(principal.data, members.data ?? [], projectID)} onRunRequested={(runId) => void navigate({ to: "/runs/$runId", params: { runId }, search: {} })} q={q} snapshot={apiSnapshot({ projects: projects.data, templates: templates.data })} token="" busy={busy} mutate={mutate} loading={queries.some((query) => query.isPending)} /><PageNavigation page={templates.data} label="templates" loading={templates.isFetching} onFirst={page.first} onPrevious={page.previous} onNext={page.next} /></>;
}
