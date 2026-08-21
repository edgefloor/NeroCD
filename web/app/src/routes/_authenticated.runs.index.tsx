import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { projectsQuery, runsQuery, templatesQuery, type RunLog, useRunMutations, useRunsPollingQuery } from "@/api";
import { RunsView } from "@/pages/RunsView";
import { validateSearch } from "@/router/search";
export const Route = createFileRoute("/_authenticated/runs/")({ validateSearch, loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(runsQuery()), context.queryClient.ensureQueryData(projectsQuery()), context.queryClient.ensureQueryData(templatesQuery())]), component: RunsIndexRoute });
function RunsIndexRoute() { return <RunsRoute q={Route.useSearch().q} />; }
export function RunsRoute({ runId, logs = [], q }: { runId?: string; logs?: RunLog[]; q?: string } = {}) { const navigate = useNavigate(); const runs = useRunsPollingQuery(); const projects = useQuery(projectsQuery()); const templates = useQuery(templatesQuery()); const { cancel } = useRunMutations(); const error = [runs, projects, templates].find((query) => query.isError)?.error; if (error) return <p role="alert">{error.message}</p>; return <RunsView runs={runs.data ?? []} projects={projects.data ?? []} templates={templates.data ?? []} logs={logs} q={q} selectedRunID={runId} loading={runs.isPending || projects.isPending || templates.isPending} cancelingRunID={cancel.isPending ? cancel.variables : undefined} onCancel={(runID) => cancel.mutate(runID)} onOpenLogs={(id) => void navigate({ to: "/runs/$runId", params: { runId: id }, search: (previous) => previous })} onCloseLogs={() => void navigate({ to: "/runs", search: (previous) => previous })} />; }
