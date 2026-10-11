import { createFileRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { allProjectMembersQuery, allProjectsQuery, principalQuery, projectMembersQuery, upsertProjectMember } from "@/api";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { SettingsView } from "@/pages/SettingsView";
import { Button } from "@/components/ui/button";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/settings")({
  validateSearch,
  loaderDeps: ({ search }) => ({ offset: search.offset ?? 0 }),
  loader: ({ context, deps }) => Promise.all([
    context.queryClient.prefetchQuery(principalQuery()),
    context.queryClient.prefetchQuery(allProjectsQuery()),
    context.queryClient.prefetchQuery(allProjectMembersQuery()),
    context.queryClient.prefetchQuery(projectMembersQuery({ limit: 50, offset: deps.offset })),
  ]),
  component: SettingsRoute,
});

function SettingsRoute() {
  const client = useQueryClient();
  const page = usePage();
  const principal = useQuery({ ...principalQuery(), retryOnMount: false });
  const projects = useQuery({ ...allProjectsQuery(), retryOnMount: false });
  const allMembers = useQuery({ ...allProjectMembersQuery(), retryOnMount: false });
  const members = useQuery({ ...projectMembersQuery({ limit: page.limit, offset: page.offset }), retryOnMount: false });
  const upsert = useMutation({
    mutationFn: (input: Parameters<typeof upsertProjectMember>[0]) => upsertProjectMember(input),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["projectMembers"] });
      toast.success("Project access updated");
    },
    onError: (error) => toast.error(error.message),
  });
  const queries = [principal, projects, allMembers, members];
  const failed = queries.filter((query) => query.isError);
  const permissionsUnavailable = [principal, projects, allMembers].some((query) => query.isError);
  const hasContext = principal.data && projects.data && allMembers.data;
  return <div className="grid min-w-0 gap-4">
    {failed.length > 0 ? <section className="space-y-3">
      <p role="alert">{hasContext ? "Unable to refresh settings. Previously loaded information remains available." : "Unable to load settings."}</p>
      {permissionsUnavailable && hasContext ? <p className="text-sm text-muted-foreground">Changes are unavailable until access information can be refreshed.</p> : null}
      <Button disabled={failed.some((query) => query.isFetching)} onClick={() => { for (const query of failed) void query.refetch(); }}>Retry settings</Button>
    </section> : null}
    {hasContext || failed.length === 0 ? <SettingsView
      principal={principal.data ?? { id: "", name: "", email: "", provider: "", roles: [] }}
      projects={projects.data ?? []} members={members.data?.items ?? []} authorizationMembers={allMembers.data ?? []}
      memberNavigation={<PageNavigation page={members.data ?? { count: 0, total: 0, limit: page.limit, offset: page.offset }} label="project members" loading={members.isFetching} onFirst={page.first} onPrevious={page.previous} onNext={page.next} />}
      loading={!hasContext} readOnly={permissionsUnavailable} updatingMember={upsert.isPending}
      onUpsertMember={(input, onSuccess) => { if (!permissionsUnavailable) upsert.mutate(input, { onSuccess }); }}
    /> : null}
  </div>;
}
