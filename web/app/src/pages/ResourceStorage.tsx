import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, listProjectMembers, principalQuery } from "@/api";
import { getResource, type Resource } from "@/api/hosting";
import { getResourceBinding } from "@/api/destinations";
import {
  createHostingStorage,
  scopedStorage,
  storageKey,
  storageQuery,
  storageRequestID,
  type HostingStorageInput,
} from "@/api/hosting-storage";
import { canOwnProject } from "@/lib/permissions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

const button = "h-auto max-w-full whitespace-normal";
const denied = (error: unknown) =>
  error instanceof ApiError && [401, 403].includes(error.status);
// Bound reads owned by this panel even when a transport ignores abort.
function boundedStorageRead<T>(
  signal: AbortSignal,
  read: (signal: AbortSignal) => Promise<T>,
): Promise<T> {
  const transport = new AbortController();
  let stop: () => void = () => {};
  const aborted = new Promise<never>((_, reject) => {
    stop = () => reject(new Error("Storage read stopped"));
    transport.signal.addEventListener("abort", stop, { once: true });
  });
  const cancel = () => transport.abort();
  signal.addEventListener("abort", cancel, { once: true });
  if (signal.aborted) cancel();
  const deadline = window.setTimeout(cancel, 30000);
  return Promise.race([
    aborted,
    transport.signal.aborted ? aborted : read(transport.signal),
  ]).finally(() => {
    window.clearTimeout(deadline);
    signal.removeEventListener("abort", cancel);
    transport.signal.removeEventListener("abort", stop);
  });
}

export function ResourceStorage({
  resource,
  blocked,
}: {
  resource: Resource;
  blocked: boolean;
}) {
  const identity = useQuery(principalQuery());
  if (resource.kind !== "application" || resource.source.kind !== "image")
    return null;
  if (!identity.data || identity.isError)
    return <p role="status">Storage access is pending or unavailable.</p>;
  const actor = JSON.stringify([identity.data.provider, identity.data.id]);
  return (
    <StorageAccess
      key={JSON.stringify([actor, resource.project_id, resource.id])}
      actor={actor}
      resource={resource}
      paused={blocked || identity.isFetching}
    />
  );
}
function StorageAccess({
  resource,
  actor,
  paused,
}: {
  resource: Resource;
  actor: string;
  paused: boolean;
}) {
  const principal = useQuery(principalQuery());
  const members = useQuery({
    queryKey: ["storageMembers", actor, resource.project_id],
    retry: false,
    gcTime: 0,
    queryFn: async ({ signal }) => {
      const rows = [];
      let offset = 0;
      for (;;) {
        const page = await listProjectMembers(
          { project_id: resource.project_id, limit: 100, offset },
          { signal },
        );
        if (page.items.some((row) => row.project_id !== resource.project_id))
          throw new Error("Storage membership scope mismatch");
        rows.push(...page.items);
        if (!page.count || page.offset + page.count >= page.total) return rows;
        const next = page.offset + page.count;
        if (next <= offset || next > 100000)
          throw new Error("Storage membership pagination unavailable");
        offset = next;
      }
    },
  });
  const owner = Boolean(
    members.data &&
      canOwnProject(principal.data, members.data, resource.project_id),
  );
  return (
    <section
      aria-label={`Persistent storage for ${resource.name}`}
      className="min-w-0 space-y-3 border-t pt-3"
    >
      <h5 className="font-medium">Persistent storage</h5>
      <p>
        Configure one retained local volume at a fixed container mount. Saving
        records desired metadata only; it does not create a volume or deploy the
        application.
      </p>
      <p>
        Deployment requires a storage-capable agent. Deployment preflight
        verifies prerequisites. Actual volume materialization and workload use
        are unknown here.
      </p>
      <p className="text-sm">
        The initial volume is empty. Image files and existing uploads are not
        imported. Ownership and permission initialization are unsupported; write
        access for non-root images must be qualified separately.
      </p>
      <p className="text-sm">
        Only one release can write at a time, so replacement can interrupt
        service. Image rollback does not undo file writes. The volume is
        retained when releases are cleaned up.
      </p>
      <p className="text-sm">
        For an application with HTTPS, review its route when replacing the
        deployment. Storage configuration does not switch public traffic.
      </p>
      <Button
        type="button"
        variant="outline"
        className={button}
        disabled={members.isFetching}
        onClick={() => void members.refetch()}
      >
        Refresh storage access
      </Button>
      {members.isPending || members.isError ? (
        <p role={members.isError ? "alert" : "status"}>
          Unable to verify current storage access. Configuration is paused.
        </p>
      ) : (
        <StorageValues
          key={JSON.stringify([actor, resource.id, owner])}
          actor={actor}
          resource={resource}
          owner={owner}
          paused={paused || members.isFetching}
        />
      )}
    </section>
  );
}
function StorageValues({
  actor,
  resource,
  owner,
  paused,
}: {
  actor: string;
  resource: Resource;
  owner: boolean;
  paused: boolean;
}) {
  const id = useId();
  const client = useQueryClient();
  const authorityLost = useRef(false);
  const storageOptions = storageQuery(actor, resource.project_id, resource.id);
  const storage = useQuery({
    ...storageOptions,
    queryFn: (context) =>
      boundedStorageRead(context.signal, async (signal) => {
        if (!storageOptions.queryFn)
          throw new Error("Storage query unavailable");
        return storageOptions.queryFn({ ...context, signal });
      }),
  });
  const binding = useQuery({
    queryKey: ["storageBinding", actor, resource.project_id, resource.id],
    retry: false,
    gcTime: 0,
    queryFn: ({ signal }) =>
      boundedStorageRead(signal, async (signal) => {
        try {
          const value = await getResourceBinding(resource.id, { signal });
          if (value.resource_id !== resource.id)
            throw new Error("Storage binding scope mismatch");
          return value;
        } catch (cause) {
          if (cause instanceof ApiError && cause.status === 404) return null;
          throw cause;
        }
      }),
  });
  const [mount, setMount] = useState("/app/uploads");
  const [confirmed, setConfirmed] = useState(false);
  const [attempt, setAttempt] = useState<HostingStorageInput>();
  const [generation, setGeneration] = useState(resource.generation);
  const [reviewedGeneration, setReviewedGeneration] = useState<number>();
  const [conflict, setConflict] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [accessDenied, setAccessDenied] = useState(false);
  const active = useRef(true),
    lock = useRef(false),
    controller = useRef<AbortController | undefined>(undefined);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      controller.current?.abort();
    };
  }, []);
  useEffect(
    () =>
      client.getQueryCache().subscribe(({ query }) => {
        const [kind, queryActor, project, resourceID] = query.queryKey;
        if (
          ["hostingStorage", "storageBinding"].includes(String(kind)) &&
          queryActor === actor &&
          project === resource.project_id &&
          resourceID === resource.id &&
          query.state.status === "error" &&
          denied(query.state.error)
        ) {
          authorityLost.current = true;
          setAccessDenied(true);
          controller.current?.abort();
        }
      }),
    [client, actor, resource.project_id, resource.id],
  );
  const failed = storage.isError || binding.isError;
  const blocked =
    paused ||
    busy ||
    failed ||
    storage.isFetching ||
    binding.isFetching ||
    storage.isPending ||
    binding.isPending;
  function beginAttempt() {
    const transport = new AbortController();
    controller.current = transport;
    lock.current = true;
    setBusy(true);
    setError("");
    const cancellations: (() => void)[] = [];
    let stop: () => void = () => {};
    const aborted = new Promise<never>((_, reject) => {
      stop = () => {
        if (controller.current === transport)
          cancellations.forEach((cancel) => cancel());
        reject(new Error("Storage request stopped"));
      };
      transport.signal.addEventListener("abort", stop, { once: true });
    });
    const deadline = window.setTimeout(() => transport.abort(), 30000);
    const owned = () =>
      active.current &&
      !authorityLost.current &&
      controller.current === transport;
    const current = () => owned() && !transport.signal.aborted;
    const wait = <T,>(promise: Promise<T>) => Promise.race([promise, aborted]);
    return {
      signal: transport.signal,
      owned,
      current,
      wait,
      read<T>(queryKey: readonly unknown[], read: () => Promise<T>) {
        const result = read();
        const query = client.getQueryCache().find({ queryKey, exact: true });
        const promise = query?.promise;
        if (query && promise)
          cancellations.push(() => {
            // A retired attempt must not cancel a newer fetch of the same key.
            void client.cancelQueries({
              predicate: (candidate) =>
                candidate === query && candidate.promise === promise,
            });
          });
        return wait(result);
      },
      finish() {
        window.clearTimeout(deadline);
        transport.signal.removeEventListener("abort", stop);
        if (controller.current === transport) {
          controller.current = undefined;
          lock.current = false;
          if (active.current) setBusy(false);
        }
      },
    };
  }
  const storageReadKey = storageKey(actor, resource.project_id, resource.id);
  const bindingReadKey = [
    "storageBinding",
    actor,
    resource.project_id,
    resource.id,
  ] as const;
  async function save(event: FormEvent) {
    event.preventDefault();
    if (
      !owner ||
      blocked ||
      lock.current ||
      conflict ||
      accessDenied ||
      authorityLost.current ||
      !confirmed ||
      !binding.data ||
      storage.data
    )
      return;
    const body = attempt ?? {
      request_id: storageRequestID(),
      expected_resource_generation: generation,
      mount_path: mount,
    };
    setAttempt(body);
    const request = beginAttempt();
    try {
      const value = scopedStorage(
        await request.wait(
          createHostingStorage(resource.id, body, { signal: request.signal }),
        ),
        resource.project_id,
        resource.id,
      );
      if (!request.current()) return;
      if (
        value.binding_id !== binding.data.id ||
        value.destination_id !== binding.data.destination_id ||
        value.mount_path !== body.mount_path
      )
        throw new Error("Storage placement mismatch");
      // Confirm with a current authorized read instead of restoring a denied cache.
      const currentStorage = await request.read(storageReadKey, () =>
        storage.refetch(),
      );
      if (!request.current()) return;
      if (currentStorage.isError) throw currentStorage.error;
    } catch (cause) {
      if (!request.owned()) return;
      if (denied(cause)) {
        setAccessDenied(true);
        return;
      }
      if (cause instanceof ApiError && cause.status === 409) {
        setConflict(true);
        setError(
          "Storage or application configuration changed. Your draft is preserved. Refresh and review before a separate submission.",
        );
      } else if (cause instanceof ApiError && [400, 413].includes(cause.status))
        setError(
          "The server rejected this configuration. Review the absolute mount path. Your draft and request identity are preserved until you change the submission.",
        );
      else
        setError(
          "Save delivery is unconfirmed. Retry the identical saved request to recover its result. Your mount and request identity are preserved.",
        );
    } finally {
      request.finish();
    }
  }
  function refreshError(
    cause: unknown,
    request: ReturnType<typeof beginAttempt>,
  ) {
    if (!request.owned()) return;
    if (denied(cause)) setAccessDenied(true);
    else
      setError(
        request.signal.aborted
          ? "Configuration refresh timed out. Your draft is preserved. Retry the refresh; nothing was resubmitted."
          : "Unable to refresh current configuration. Nothing was resubmitted.",
      );
  }
  async function refreshConfigured() {
    if (busy || paused || accessDenied || authorityLost.current || lock.current)
      return;
    const request = beginAttempt();
    try {
      const results = await Promise.all([
        request.read(storageReadKey, () => storage.refetch()),
        request.read(bindingReadKey, () => binding.refetch()),
      ]);
      if (!request.current()) return;
      for (const result of results) if (result.isError) throw result.error;
    } catch (cause) {
      refreshError(cause, request);
    } finally {
      request.finish();
    }
  }
  async function refreshGeneration() {
    if (busy || paused || accessDenied || authorityLost.current || lock.current)
      return;
    const request = beginAttempt();
    setReviewedGeneration(undefined);
    try {
      const latest = await request.wait(
        getResource(resource.id, { signal: request.signal }),
      );
      if (!request.current()) return;
      if (
        latest.id !== resource.id ||
        latest.project_id !== resource.project_id
      )
        throw new Error("Storage resource scope mismatch");
      const currentStorage = await request.read(storageReadKey, () =>
        storage.refetch(),
      );
      if (!request.current()) return;
      if (currentStorage.isError) throw currentStorage.error;
      const currentBinding = await request.read(bindingReadKey, () =>
        binding.refetch(),
      );
      if (!request.current()) return;
      if (currentBinding.isError) throw currentBinding.error;
      setReviewedGeneration(latest.generation);
    } catch (cause) {
      refreshError(cause, request);
    } finally {
      request.finish();
    }
  }

  if (accessDenied || denied(storage.error) || denied(binding.error))
    return (
      <p role="alert">
        Storage access is unavailable. Refresh access before continuing.
      </p>
    );
  return (
    <>
      {storage.isPending || binding.isPending ? (
        <p role="status">Loading configured storage...</p>
      ) : null}
      {failed ? (
        <p role="alert">
          Unable to read storage or placement. Changes are paused.
        </p>
      ) : null}
      <Button
        type="button"
        variant="outline"
        className={button}
        disabled={busy || storage.isFetching || binding.isFetching}
        onClick={() => void refreshConfigured()}
      >
        Refresh configured storage
      </Button>
      {!storage.isError && storage.data ? (
        <dl
          className="min-w-0 space-y-1 break-all text-sm"
          aria-label="Configured storage metadata"
        >
          <dt>Configured volume</dt>
          <dd>{storage.data.volume_name}</dd>
          <dt>Destination</dt>
          <dd>{storage.data.destination_id}</dd>
          <dt>Immutable mount path</dt>
          <dd>{storage.data.mount_path}</dd>
          <dt>Retention</dt>
          <dd>{storage.data.retention}</dd>
          <dt>Access</dt>
          <dd>Single writer · read/write</dd>
          <dt>Materialization</dt>
          <dd>Unknown; configured metadata only</dd>
        </dl>
      ) : null}
      {storage.data === null ? <p>No configured persistent storage.</p> : null}
      {binding.data === null ? (
        <p>Save a desired destination above before configuring storage.</p>
      ) : null}
      {!owner ? (
        <p>Project owner access is required to configure persistent storage.</p>
      ) : !storage.data ? (
        <form
          aria-label="Configure persistent storage"
          onSubmit={(event) => void save(event)}
          className="min-w-0 space-y-3"
          aria-busy={busy}
        >
          <label htmlFor={`${id}-mount`}>Container mount path</label>
          <Input
            id={`${id}-mount`}
            value={mount}
            required
            maxLength={512}
            disabled={busy || paused}
            onChange={(event) => {
              setMount(event.target.value);
              setAttempt(undefined);
              setConfirmed(false);
            }}
          />
          <p className="text-xs">
            Use a canonical absolute container directory, such as /app/uploads.
            Root, /proc, /sys and /dev paths are unsupported. The mount cannot
            be edited after saving in this version.
          </p>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={confirmed}
              disabled={busy || paused}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            <span>
              I understand this saves immutable desired storage metadata only
              and neither creates a volume nor deploys the application.
            </span>
          </label>
          <Button
            type="submit"
            className={button}
            disabled={blocked || conflict || !confirmed || !binding.data}
          >
            {busy
              ? "Saving storage..."
              : attempt
                ? "Retry identical storage request"
                : "Save desired storage"}
          </Button>
          {conflict ? (
            <section
              className="space-y-2"
              aria-label="Storage configuration conflict"
            >
              <Button
                type="button"
                variant="outline"
                className={button}
                disabled={busy || paused}
                onClick={() => void refreshGeneration()}
              >
                Refresh storage configuration for review
              </Button>
              {reviewedGeneration ? (
                <>
                  <p>Current resource generation {reviewedGeneration}</p>
                  <Button
                    type="button"
                    className={button}
                    disabled={blocked}
                    onClick={() => {
                      setGeneration(reviewedGeneration);
                      setAttempt(undefined);
                      setConflict(false);
                      setConfirmed(false);
                      setError("");
                    }}
                  >
                    Keep mount with reviewed generation for a separate save
                  </Button>
                </>
              ) : null}
            </section>
          ) : null}
        </form>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
    </>
  );
}
