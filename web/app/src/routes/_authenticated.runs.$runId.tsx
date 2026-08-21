import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { projectsQuery, runLogsQuery, runsQuery, templatesQuery, useRunsPollingQuery, useSelectedRunLogsPollingQuery } from "@/api";
import { RunsRoute } from "./_authenticated.runs.index";
import { validateSearch } from "@/router/search";
export const Route = createFileRoute("/_authenticated/runs/$runId")({ validateSearch, loader: ({ context, params }) => Promise.all([context.queryClient.ensureQueryData(runsQuery()), context.queryClient.ensureQueryData(projectsQuery()), context.queryClient.ensureQueryData(templatesQuery()), context.queryClient.ensureQueryData(runLogsQuery({ run_id: params.runId, limit: 100, offset: 0 }))]), component: RunDetailRoute });
function RunDetailRoute() { const { runId } = Route.useParams(); const { q } = Route.useSearch(); const runs = useRunsPollingQuery(); const logs = useSelectedRunLogsPollingQuery(runId, runs.data); if (logs.isError) return <p role="alert">{logs.error.message}</p>; return <RunsRoute runId={runId} logs={logs.data ?? []} q={q} />; }
