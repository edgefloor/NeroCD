import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { allProjectMembersQuery, principalQuery } from "@/api";
import {
  databaseDenied,
  databaseError,
  databaseKey,
  databaseQuery,
  type HostingDatabase,
} from "@/api/hosting-databases";
import type { Resource } from "@/api/hosting";
import { canOwnProject } from "@/lib/permissions";
import { Button } from "@/components/ui/button";

export function useDatabaseAccess(project: string, environment: string) {
  const identity = useQuery(principalQuery());
  const members = useQuery(allProjectMembersQuery());
  const principal = identity.data;
  const actor = principal
    ? JSON.stringify([principal.provider, principal.id])
    : "";
  const ready =
    Boolean(principal) &&
    !identity.isPending &&
    Boolean(members.data) &&
    !members.isPending;
  const viewer = Boolean(
    principal &&
    (principal.roles.includes("system_admin") ||
      members.data?.some(
        (member) =>
          member.project_id === project && member.user_id === principal.id,
      )),
  );
  const owner = canOwnProject(principal, members.data ?? [], project);
  return {
    ready,
    refreshing: identity.isFetching || members.isFetching || identity.isError || members.isError,
    error: identity.isError ? identity.error : members.isError ? members.error : undefined,
    viewer,
    owner,
    scope: { actor, project, environment },
    epoch: JSON.stringify([actor, project, environment, viewer, owner]),
  };
}

export function DatabaseMetadata({
  database,
  label,
}: {
  database: HostingDatabase;
  label?: string;
}) {
  return (
    <section
      className="min-w-0 space-y-3 rounded-xl border p-3"
      aria-label={label ?? `Database metadata for ${database.resource.name}`}
    >
      <h4 className="break-all font-medium">{database.resource.name}</h4>
      <p role="status">
        Adapter unqualified. This saves database configuration only; it does not
        start an engine or confirm a running database.
      </p>
      <dl className="space-y-1 break-all text-sm">
        <dt>Engine</dt>
        <dd>{database.engine}</dd>
        <dt>Recipe</dt>
        <dd>{database.recipe_version}</dd>
        <dt>Resource ID</dt>
        <dd>{database.resource.id}</dd>
        <dt>Configuration generation</dt>
        <dd>{database.resource.generation}</dd>
        <dt>Destination</dt>
        <dd>{database.binding.destination_id}</dd>
        <dt>Runtime namespace</dt>
        <dd>{database.binding.runtime_namespace}</dd>
        <dt>Storage reference</dt>
        <dd>{database.storage_ref.storage_id}</dd>
        <dt>Storage generation</dt>
        <dd>{database.storage_ref.generation}</dd>
        <dt>Storage reference hash</dt>
        <dd>{database.storage_ref.hash}</dd>
      </dl>
      <ul
        className="space-y-2 break-all text-sm"
        aria-label="Database credential references"
      >
        {database.credentials.map((credential) => (
          <li key={credential.role}>
            {credential.role} · username {credential.username} · secret ID{" "}
            {credential.secret_id} · version {credential.version}
          </li>
        ))}
      </ul>
      <p className="text-xs text-muted-foreground">
        Credential references contain no password or connection string. Engine
        execution remains unqualified.
      </p>
    </section>
  );
}

export function ResourceDatabase({
  resource,
  blocked = false,
}: {
  resource: Resource;
  blocked?: boolean;
}) {
  const access = useDatabaseAccess(
    resource.project_id,
    resource.project_environment_id,
  );
  const [denied, setDenied] = useState("");
  if (!access.ready || access.refreshing || blocked)
    return (
      <p role="status">Database metadata access is pending or unavailable.</p>
    );
  if (!access.viewer || denied === access.epoch)
    return (
      <p role="alert">
        Database access is unavailable. Refresh project access to view metadata.
      </p>
    );
  return (
    <DatabaseDetail
      key={JSON.stringify([access.epoch, resource.id, resource.generation])}
      resource={resource}
      scope={access.scope}
      onDenied={() => setDenied(access.epoch)}
    />
  );
}
function DatabaseDetail({
  resource,
  scope,
  onDenied,
}: {
  resource: Resource;
  scope: ReturnType<typeof useDatabaseAccess>["scope"];
  onDenied: () => void;
}) {
  const client = useQueryClient();
  const detail = useQuery(databaseQuery(scope, resource.id));
  useEffect(() => {
    if (detail.isError && databaseDenied(detail.error)) {
      client.removeQueries({ queryKey: databaseKey(scope, resource.id) });
      onDenied();
    }
  }, [
    detail.isError,
    detail.error,
    client,
    scope.actor,
    scope.project,
    scope.environment,
    resource.id,
    onDenied,
  ]);
  return (
    <section
      className="min-w-0 space-y-2"
      aria-label={`Database detail for ${resource.name}`}
    >
      {detail.isPending ? (
        <p role="status">Loading database metadata...</p>
      ) : null}
      {detail.isError ? (
        <p role="alert">
          Database metadata is unavailable. {databaseError(detail.error)}
        </p>
      ) : null}
      {!detail.isError && detail.data ? (
        <DatabaseMetadata database={detail.data} />
      ) : null}
      <Button
        type="button"
        variant="outline"
        className="h-auto max-w-full whitespace-normal"
        disabled={detail.isFetching}
        onClick={() => void detail.refetch()}
      >
        Refresh database metadata
      </Button>
    </section>
  );
}
