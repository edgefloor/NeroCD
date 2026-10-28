import { useEffect, useEffectEvent, useState } from "react";
import { ApiError } from "@/api/errors";
import {
  listManagedSecrets,
  listManagedSecretVersions,
} from "@/api/managed-secrets";

type Metadata = {
  secrets: Awaited<ReturnType<typeof listManagedSecrets>>;
  versions?: Awaited<ReturnType<typeof listManagedSecretVersions>>;
};
type Request = {
  actor: string;
  project: string;
  secret: string;
  offset: number;
  versionOffset: number;
  enabled: boolean;
  disabled: boolean;
  refreshable: boolean;
};

export function useRegistryMetadata(
  input: Request,
  onRead: (metadata: Metadata) => void,
  onDenied: () => void,
  onRestored?: () => void,
) {
  const {
    actor,
    project,
    secret,
    offset,
    versionOffset,
    enabled,
    disabled,
    refreshable,
  } = input;
  const [revision, setRevision] = useState(0);
  const [result, setResult] = useState<{
    identity: string;
    data?: Metadata;
    error: boolean;
    loading: boolean;
  }>();
  const identity = JSON.stringify([
    actor,
    project,
    secret,
    offset,
    versionOffset,
  ]);
  const publish = useEffectEvent((metadata: Metadata) => {
    if (!disabled) onRead(metadata);
    if (refreshable) onRestored?.();
  });
  const reject = useEffectEvent((cause: unknown) => {
    if (cause instanceof ApiError && [401, 403, 404].includes(cause.status))
      onDenied();
  });
  useEffect(() => {
    if (!enabled || (disabled && !refreshable)) return;
    const controller = new AbortController();
    setResult((previous) => ({
      identity,
      data: previous?.identity === identity ? previous.data : undefined,
      loading: true,
      error: false,
    }));
    // Metadata belongs to one committed selection. A retired read cannot validate
    // a later selection or notify a parent after this picker has unmounted.
    void Promise.all([
      listManagedSecrets(
        { project_id: project, offset, limit: 20 },
        { signal: controller.signal },
      ),
      secret
        ? listManagedSecretVersions(
            secret,
            { offset: versionOffset, limit: 20 },
            { signal: controller.signal },
          )
        : undefined,
    ])
      .then(([secrets, versions]) => {
        if (controller.signal.aborted) return;
        const data = { secrets, versions };
        setResult({ identity, data, loading: false, error: false });
        publish(data);
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        setResult({ identity, loading: false, error: true });
        reject(cause);
      });
    return () => controller.abort();
  }, [
    actor,
    project,
    secret,
    offset,
    versionOffset,
    enabled,
    disabled,
    refreshable,
    revision,
    identity,
  ]);
  const current = result?.identity === identity ? result : undefined;
  return {
    data: current?.data,
    isFetching: Boolean(enabled && (!current || current.loading)),
    isError: current?.error ?? false,
    refresh: () => setRevision((value) => value + 1),
  };
}

export function registrySelection(
  metadata: Metadata | undefined,
  project: string,
  secret: string,
  version: string,
) {
  const selected = metadata?.secrets.items.some(
    (item) =>
      item.id === secret &&
      item.project_id === project &&
      item.status === "active",
  );
  const number = Number(version);
  const valid = Boolean(
    selected &&
      Number.isSafeInteger(number) &&
      number > 0 &&
      metadata?.versions?.items.some(
        (item) => item.secret_id === secret && item.version === number,
      ),
  );
  return {
    valid,
    ...(valid ? { credential: { secret_id: secret, version: number } } : {}),
  };
}
