import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, listProjectMembers, principalQuery } from "@/api";
import { getResource, type Resource } from "@/api/hosting";
import { getResourceBinding } from "@/api/destinations";
import {
  createHostingStorage,
  scopedStorage,
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
  const storage = useQuery(
    storageQuery(actor, resource.project_id, resource.id),
  );
  const binding = useQuery({
    queryKey: ["storageBinding", actor, resource.project_id, resource.id],
    retry: false,
    gcTime: 0,
    queryFn: async ({ signal }) => {
      try {
        const value = await getResourceBinding(resource.id, { signal });
        if (value.resource_id !== resource.id)
          throw new Error("Storage binding scope mismatch");
        return value;
      } catch (cause) {
        if (cause instanceof ApiError && cause.status === 404) return null;
        throw cause;
      }
    },
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
  async function save(event: FormEvent) {
    event.preventDefault();
    if (
      !owner ||
      blocked ||
      lock.current ||
      conflict ||
      accessDenied ||
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
    setBusy(true);
    lock.current = true;
    setError("");
    const transport = new AbortController();
    controller.current = transport;
    let stop: () => void = () => {};
    const aborted = new Promise<never>((_, reject) => {
      stop = () => reject(new Error("Storage response not confirmed"));
      transport.signal.addEventListener("abort", stop, { once: true });
    });
    const deadline = window.setTimeout(() => transport.abort(), 30000);
    try {
      const value = scopedStorage(
        await Promise.race([
          createHostingStorage(resource.id, body, { signal: transport.signal }),
          aborted,
        ]),
        resource.project_id,
        resource.id,
      );
      if (!active.current || authorityLost.current || transport.signal.aborted)
        return;
      if (
        value.binding_id !== binding.data.id ||
        value.destination_id !== binding.data.destination_id ||
        value.mount_path !== body.mount_path
      )
        throw new Error("Storage placement mismatch");
      // Refetch under current read authority instead of restoring a denied cache.
      await Promise.race([storage.refetch(), aborted]);
    } catch (cause) {
      if (!active.current || authorityLost.current) return;
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
      window.clearTimeout(deadline);
      transport.signal.removeEventListener("abort", stop);
      lock.current = false;
      if (active.current) setBusy(false);
    }
  }
  async function refreshGeneration() {
    if (busy || paused || accessDenied) return;
    setBusy(true);
    try {
      const latest = await getResource(resource.id);
      if (!active.current) return;
      if (
        latest.id !== resource.id ||
        latest.project_id !== resource.project_id
      )
        throw new Error("Storage resource scope mismatch");
      setReviewedGeneration(latest.generation);
      await storage.refetch();
      await binding.refetch();
    } catch (cause) {
      if (active.current) {
        if (denied(cause)) setAccessDenied(true);
        else
          setError(
            "Unable to refresh current configuration. Nothing was resubmitted.",
          );
      }
    } finally {
      if (active.current) setBusy(false);
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
        onClick={() => {
          void storage.refetch();
          void binding.refetch();
        }}
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
