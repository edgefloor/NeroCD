import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { principalQuery, revokeRunnerToken, runnerQuery } from "@/api";
import { RunnerDetailView } from "@/pages/RunnersView";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/runners/$runnerId")({ validateSearch, loader: ({ context, params }) => Promise.all([context.queryClient.ensureQueryData(runnerQuery(params.runnerId)), context.queryClient.ensureQueryData(principalQuery())]), component: RunnerDetailRoute });
function RunnerDetailRoute() { const { runnerId } = Route.useParams(); const runner = useQuery(runnerQuery(runnerId)); const principal = useQuery(principalQuery()); if (runner.isError || principal.isError) return <p role="alert">Runner is unavailable.</p>; return <RunnerDetailView runner={runner.data} canAdmin={(principal.data?.roles ?? []).includes("system_admin")} onRevoke={async () => { await revokeRunnerToken({ runner_id: runnerId }); await runner.refetch(); }} />; }
