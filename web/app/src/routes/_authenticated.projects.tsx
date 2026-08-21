import { createFileRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { projectsQuery, repositoriesQuery, useProjectMutations } from "@/api";
import { ProjectsView } from "@/pages/ProjectsView";
import { validateSearch } from "@/router/search";
export const Route = createFileRoute("/_authenticated/projects")({ validateSearch, loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(projectsQuery()), context.queryClient.ensureQueryData(repositoriesQuery())]), component: ProjectsRoute });
function ProjectsRoute() { const { q } = Route.useSearch(); const projects = useQuery(projectsQuery()); const repositories = useQuery(repositoriesQuery()); const { create, update, archive, repository } = useProjectMutations(); const error = [projects, repositories].find((query) => query.isError)?.error; if (error) return <p role="alert">{error.message}</p>; return <ProjectsView projects={projects.data ?? []} repositories={repositories.data ?? []} q={q} loading={projects.isPending || repositories.isPending} projectBusyID={update.isPending ? update.variables.id : archive.isPending ? archive.variables.id : undefined} repositoryBusy={repository.isPending} onCreateProject={(input) => create.mutate(input)} onUpdateProject={(input) => update.mutate(input)} onArchiveProject={(id) => archive.mutate({ id })} onCreateRepository={(input) => repository.mutate(input)} />; }
