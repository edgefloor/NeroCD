import { createFileRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { allProjectMembersQuery, allProjectsQuery, principalQuery, projectMembersQuery, upsertProjectMember } from "@/api";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { SettingsView } from "@/pages/SettingsView";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/settings")({
  validateSearch,
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(principalQuery()),
    context.queryClient.ensureQueryData(allProjectsQuery()),
    context.queryClient.ensureQueryData(allProjectMembersQuery()),
    context.queryClient.ensureQueryData(projectMembersQuery({ limit: 50, offset: 0 })),
  ]),
  component: SettingsRoute,
});

function SettingsRoute() {
  const client = useQueryClient();
  const page = usePage();
  const principal = useQuery(principalQuery());
  const projects = useQuery(allProjectsQuery());
  const allMembers = useQuery(allProjectMembersQuery());
  const members = useQuery(projectMembersQuery({ limit: page.limit, offset: page.offset }));
  const upsert = useMutation({
    mutationFn: (input: Parameters<typeof upsertProjectMember>[0]) => upsertProjectMember(input),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["projectMembers"] });
      toast.success("Project access updated");
    },
    onError: (error) => toast.error(error.message),
  });
  const queries = [principal, projects, allMembers, members];
  const error = queries.find((query) => query.isError)?.error;
  if (error) return <p role="alert">{error.message}</p>;
  return <><SettingsView principal={principal.data ?? { id: "", name: "", email: "", provider: "", roles: [] }} projects={projects.data ?? []} members={members.data?.items ?? []} authorizationMembers={allMembers.data ?? []} loading={queries.some((query) => query.isPending)} updatingMember={upsert.isPending} onUpsertMember={(input, onSuccess) => upsert.mutate(input, { onSuccess })} /><PageNavigation page={members.data} label="project members" loading={members.isFetching} onPrevious={page.previous} onNext={page.next} /></>;
}
