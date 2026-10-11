import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { runQuery, shouldPollRun } from "@/api";
import { WorkflowDiagnostics } from "@/components/runs/WorkflowDiagnostics";
import { RunsRoute } from "./_authenticated.runs.index";
import { validateSearch } from "@/router/search";
export const Route = createFileRoute("/_authenticated/runs/$runId")({ validateSearch, loader: ({ context, params }) => context.queryClient.ensureQueryData(runQuery(params.runId)), component: RunDetailRoute });
function RunDetailRoute() {
  const { runId } = Route.useParams();
  const { q } = Route.useSearch();
  const run = useQuery({
    ...runQuery(runId),
    refetchInterval: (query) => shouldPollRun(query.state.data) ? 3_000 : false,
  }).data;
  return <><RunsRoute runId={runId} q={q} />{run ? <div className="mt-4"><WorkflowDiagnostics run={run} /></div> : null}</>;
}
