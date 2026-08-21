import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { runLogsQuery, runsQuery } from "@/api";
import { LogsView } from "@/pages/LogsView";
import { validateSearch } from "@/router/search";
export const Route = createFileRoute("/_authenticated/logs")({ validateSearch, loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(runLogsQuery({ limit: 100, offset: 0 })), context.queryClient.ensureQueryData(runsQuery({ limit: 100, offset: 0 }))]), component: LogsRoute });
function LogsRoute() { const { q } = Route.useSearch(); const logs = useQuery(runLogsQuery({ limit: 100, offset: 0 })); const runs = useQuery(runsQuery({ limit: 100, offset: 0 })); const error = [logs, runs].find((query) => query.isError)?.error; if (error) return <p role="alert">{error.message}</p>; return <LogsView logs={logs.data ?? []} runs={runs.data ?? []} q={q} loading={logs.isPending || runs.isPending} />; }
