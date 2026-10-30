import { HelpDetails } from "@/components/common/HelpDetails";
import { allProjectMembersQuery, principalQuery } from "@/api";
import type { Resource } from "@/api/hosting";
import {
  databaseDeploymentKey,
  databaseOperationKey,
} from "@/api/hosting-database-deployments";
import {
  databaseDenied,
  databaseError,
  databaseKey,
  databaseQuery,
  type HostingDatabase,
} from "@/api/hosting-databases";
import { Button } from "@/components/ui/button";
import { canMaintainProject, canOwnProject } from "@/lib/permissions";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo, useState } from "react";
import { DatabaseBackups } from "./DatabaseBackups";
import { DatabaseDeployments } from "./DatabaseDeployments";
import { useQueryAccessDenial } from "./useQueryAccessDenial";

export function useDatabaseAccess(project: string, environment: string) {
  const identity = useQuery(principalQuery());
  const members = useQuery(allProjectMembersQuery());
  const principal = identity.data;
  const actor = principal
    ? JSON.stringify([principal.provider, principal.id])
    : "";
  const scope = useMemo(
    () => ({ actor, project, environment }),
    [actor, project, environment],
  );
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
  const maintainer = canMaintainProject(principal, members.data ?? [], project);
  const errors = [
    identity.isError ? identity.error : undefined,
    members.isError ? members.error : undefined,
  ];
  return {
    ready,
    refreshing:
      identity.isFetching ||
      members.isFetching ||
      identity.isError ||
      members.isError,
    error: errors.find(databaseDenied) ?? errors.find(Boolean),
    viewer,
    owner,
    maintainer,
    scope,
    epoch: JSON.stringify([
      actor,
      project,
      environment,
      viewer,
      owner,
      maintainer,
    ]),
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
      <p role="status">Configuration saved.</p>
      <p className="text-sm">
        Engine: <span>{database.engine}</span>
      </p>
      <HelpDetails title="Saved configuration details">
        <dl className="space-y-1 break-all text-sm">
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
          These are credential references, not passwords or connection strings.
        </p>
      </HelpDetails>
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
  const onDenied = useCallback(() => setDenied(access.epoch), [access.epoch]);
  if (!access.ready)
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
      key={JSON.stringify([access.epoch, resource.id])}
      resource={resource}
      scope={access.scope}
      owner={access.owner}
      maintainer={access.maintainer}
      blocked={access.refreshing || blocked}
      accessError={access.error}
      onDenied={onDenied}
    />
  );
}
function DatabaseDetail({
  owner,
  resource,
  scope,
  maintainer,
  blocked,
  accessError,
  onDenied,
}: {
  owner: boolean;
  resource: Resource;
  scope: ReturnType<typeof useDatabaseAccess>["scope"];
  maintainer: boolean;
  blocked: boolean;
  accessError?: unknown;
  onDenied: () => void;
}) {
  const client = useQueryClient();
  const detail = useQuery({
    ...databaseQuery(scope, resource.id),
    enabled: !blocked,
  });
  const denial = [detail.error, accessError].find(databaseDenied);
  const deny = useCallback(() => {
    void client.cancelQueries({ queryKey: databaseKey(scope, resource.id) });
    client.removeQueries({ queryKey: databaseKey(scope, resource.id) });
    client.removeQueries({
      queryKey: databaseDeploymentKey(scope, resource.id),
    });
    client.removeQueries({
      queryKey: databaseOperationKey(scope, resource.id, "").slice(0, -1),
    });
    onDenied();
  }, [client, scope, resource.id, onDenied]);
  useQueryAccessDenial(
    [
      databaseQuery(scope, resource.id).queryKey,
      principalQuery().queryKey,
      allProjectMembersQuery().queryKey,
    ],
    deny,
    undefined,
    databaseDenied,
  );
  if (denial) return <p role="alert">Database access is unavailable.</p>;
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
      {!detail.isError && !blocked && detail.data ? (
        <DatabaseMetadata database={detail.data} />
      ) : null}
      {detail.data ? (
        <DatabaseDeployments
          database={detail.data}
          scope={scope}
          maintainer={maintainer}
          blocked={blocked || detail.isError || detail.isFetching}
          onDenied={deny}
          onRefresh={async () => !(await detail.refetch()).isError}
        />
      ) : null}
      {detail.data && !detail.isError ? (
        <DatabaseBackups
          database={detail.data}
          scope={scope}
          owner={owner}
          blocked={blocked || detail.isFetching}
          onDenied={deny}
        />
      ) : null}
      <Button
        type="button"
        variant="outline"
        className="h-auto max-w-full whitespace-normal"
        disabled={detail.isFetching || blocked}
        onClick={() => void detail.refetch()}
      >
        Refresh database metadata
      </Button>
    </section>
  );
}
