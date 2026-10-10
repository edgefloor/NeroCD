import { createFileRoute } from "@tanstack/react-router";
import { useQueries, useQuery } from "@tanstack/react-query";
import { runLogsQuery, runQuery } from "@/api";
import { LogsView } from "@/pages/LogsView";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { validateSearch } from "@/router/search";
export const Route = createFileRoute("/_authenticated/logs")({ validateSearch, loader: ({ context }) => context.queryClient.ensureQueryData(runLogsQuery({ limit: 50, offset: 0 })), component: LogsRoute });
function LogsRoute() { const { q } = Route.useSearch(); const page = usePage(); const logs = useQuery(runLogsQuery({ limit: page.limit, offset: page.offset })); const runIDs = [...new Set((logs.data?.items ?? []).map((log) => log.run_id))]; const runs = useQueries({ queries: runIDs.map((id) => runQuery(id)) }); const error = [logs, ...runs].find((query) => query.isError)?.error; if (error) return <p role="alert">{error.message}</p>; return <><LogsView logs={logs.data?.items ?? []} runs={runs.flatMap((query) => query.data ? [query.data] : [])} q={q} loading={logs.isPending || runs.some((query) => query.isPending)} /><PageNavigation page={logs.data} label="logs" loading={logs.isFetching} onPrevious={page.previous} onNext={page.next} /></>; }
