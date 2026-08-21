import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { createRunnerEnrollment, principalQuery, revokeRunnerToken, runnersQuery } from "@/api";
import { RunnersView } from "@/pages/RunnersView";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/runners/")({ validateSearch, loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(runnersQuery()), context.queryClient.ensureQueryData(principalQuery())]), component: RunnersRoute });

function RunnersRoute() {
  const navigate = useNavigate(); const { q } = Route.useSearch(); const runners = useQuery(runnersQuery()); const principal = useQuery(principalQuery());
  if (runners.isError || principal.isError) return <p role="alert">Runner inventory is unavailable.</p>;
  const canAdmin = (principal.data?.roles ?? []).includes("system_admin");
  return <RunnersView runners={runners.data ?? []} q={q} canAdmin={canAdmin} onEnroll={async (input) => { const created = await createRunnerEnrollment(input); await runners.refetch(); return created; }} onRevokeRunner={async (id) => { await revokeRunnerToken({ runner_id: id }); await runners.refetch(); void navigate({ to: "/runners/$runnerId", params: { runnerId: id }, search: (previous) => previous, replace: true }); }} />;
}
