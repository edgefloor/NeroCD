import { createFileRoute } from "@tanstack/react-router";
import { allProjectsQuery, allTemplatesQuery, runsQuery } from "@/api";
import { RunsRoute } from "@/components/runs/RunsRoute";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/runs/")({
  validateSearch,
  loaderDeps: ({ search }) => ({ offset: search.offset ?? 0 }),
  loader: ({ context, deps }) => Promise.all([context.queryClient.prefetchQuery(runsQuery({ limit: 50, offset: deps.offset })), context.queryClient.prefetchQuery(allProjectsQuery()), context.queryClient.prefetchQuery(allTemplatesQuery())]),
  component: RunsIndexRoute,
});

function RunsIndexRoute() { return <RunsRoute q={Route.useSearch().q} />; }
