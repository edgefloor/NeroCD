import {
  databaseBackupTargetsKey,
  databaseBackupValidationsKey,
  listDatabaseBackupTargets,
  listDatabaseBackupValidations,
  type DatabaseBackupTransfer,
  type RemoteBackupTarget,
} from "@/api/hosting-database-backup-targets";
import {
  databaseDenied,
  type DatabaseScope,
  type HostingDatabase,
} from "@/api/hosting-databases";
import {
  listStorageValidations,
  type StorageBackupTransfer,
} from "@/api/hosting-storage-backup-remote";
import { readBackupMetadata } from "@/components/hosting/backup-access";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
type Props = {
  database?: HostingDatabase;
  storage?: { resource: { id: string }; destination_id: string };
  scope: DatabaseScope;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
};
export function useDatabaseBackupDestinations({
  database,
  storage,
  scope,
  blocked,
  onDenied,
}: Props) {
  const client = useQueryClient();
  const [accessRevoked, setAccessRevoked] = useState(false);
  const targets = useQuery({
    queryKey: databaseBackupTargetsKey(scope),
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => listDatabaseBackupTargets(scope, signal),
        retireDestinationAccess,
        [401, 403],
      ),
    enabled:
      !blocked &&
      !accessRevoked &&
      (!!storage || (database && ["postgresql", "mysql"].includes(database.engine))),
    retry: false,
  });
  const validations = useQueries({
    queries: (targets.data ?? []).map((target) => ({
      queryKey: [
        ...databaseBackupValidationsKey(scope, target.id),
        storage ? "storage" : "database",
      ],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        readBackupMetadata(
          signal,
          () =>
            storage
              ? listStorageValidations(scope, target.id, signal)
              : listDatabaseBackupValidations(scope, target.id, signal),
          retireDestinationAccess,
          [401, 403],
        ),
      enabled: !blocked && !accessRevoked,
      retry: false,
      refetchInterval: (query: {
        state: {
          data?: (DatabaseBackupTransfer | StorageBackupTransfer)[];
        };
      }) =>
        query.state.data?.some((v) =>
          ["queued", "dispatched", "applying"].includes(v.state),
        )
          ? 3000
          : false,
    })),
  });
  const denied =
    accessRevoked ||
    databaseDenied(targets.error) ||
    validations.some((v) => databaseDenied(v.error));
  function retireDestinationAccess() {
    setAccessRevoked(true);
    client.removeQueries({ queryKey: databaseBackupTargetsKey(scope) });
    onDenied();
  }

  const eligible = (targets.data ?? []).filter(
    (target, i) =>
      target.enabled &&
      !validations[i]?.isError &&
      validations[i]?.data?.some(
        (v) =>
          v.mode === "validate" &&
          v.state === "succeeded" &&
          v.target_revision === target.revision &&
          v.destination_id ===
            (storage?.destination_id ?? database?.binding?.destination_id),
      ),
  );
  return {
    targets,
    validations,
    eligible,
    denied,
    refresh: async () => {
      await targets.refetch();
      await Promise.all(validations.map((v) => v.refetch()));
    },
  };
}
export function validSelectedBackupDestination(
  value: RemoteBackupTarget | undefined,
  destinations: ReturnType<typeof useDatabaseBackupDestinations>,
) {
  return (
    !value ||
    destinations.eligible.some(
      (v) => v.id === value.target_id && v.revision === value.revision,
    )
  );
}
