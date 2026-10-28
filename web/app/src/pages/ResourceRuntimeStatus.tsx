import { ApiError, listProjectMembers, principalQuery } from "@/api";
import type { Resource } from "@/api/hosting";
import {
  getHostingRuntimeStatus,
  runtimeStatusExpiry,
  runtimeStatusView,
  type RuntimeStatusSnapshot,
} from "@/api/hosting-runtime-status";
import { Button } from "@/components/ui/button";
import { useQuery } from "@tanstack/react-query";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
const button = "h-auto max-w-full whitespace-normal";
const containerText: Record<string, string> = {
  running: "Running",
  exited: "Exited",
  missing: "Missing, verified absence",
  unknown: "Unknown",
};
const readinessText: Record<string, string> = {
  ready: "Ready",
  not_ready: "Not ready",
  not_checked: "Not checked",
  unknown: "Unknown",
};
const reasons: Record<string, string> = {
  never_observed: "No observation has been recorded for this target.",
  target_changed: "The settled target changed. Earlier evidence is historical.",
  stale: "The last observation has expired.",
  restore_hold:
    "Restored ownership is held. Observation cannot release the hold.",
  authority_unavailable: "Current observation authority is unavailable.",
  observed: "Observation received for the exact settled target.",
};
const collectionErrors: Record<string, string> = {
  daemon_unreachable: "Docker daemon could not be reached.",
  daemon_mismatch: "Docker daemon identity did not match.",
  target_mismatch: "Container identity did not match the target.",
  inspect_failed: "Exact container inspection failed.",
  readiness_timeout:
    "Readiness could not be confirmed within the sampling deadline.",
};
export function ResourceRuntimeStatus({
  resource,
  blocked,
}: {
  resource: Resource;
  blocked: boolean;
}) {
  const identity = useQuery(principalQuery());
  if (
    blocked ||
    identity.isPending ||
    identity.isFetching ||
    identity.isError ||
    !identity.data
  )
    return (
      <p role={identity.isError ? "alert" : "status"}>
        Runtime status access is pending or unavailable.
      </p>
    );
  const actor = JSON.stringify([identity.data.provider, identity.data.id]);
  return (
    <StatusAccess
      key={JSON.stringify([actor, resource.project_id, resource.id])}
      resource={resource}
      actor={actor}
    />
  );
}
function StatusAccess({
  resource,
  actor,
}: {
  resource: Resource;
  actor: string;
}) {
  const principal = useQuery(principalQuery());
  const members = useQuery({
    queryKey: ["runtimeStatusMembers", actor, resource.project_id],
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    queryFn: async ({ signal }) => {
      const rows = [];
      let offset = 0;
      for (;;) {
        const page = await listProjectMembers(
          { project_id: resource.project_id, limit: 100, offset },
          { signal },
        );
        if (page.items.some((row) => row.project_id !== resource.project_id))
          throw new Error("Membership scope mismatch");
        rows.push(...page.items);
        if (!page.count || page.offset + page.count >= page.total) return rows;
        const next = page.offset + page.count;
        if (next <= offset || next > 100000)
          throw new Error("Membership pagination unavailable");
        offset = next;
      }
    },
  });
  const current =
    principal.data &&
    JSON.stringify([principal.data.provider, principal.data.id]) === actor;
  const ready =
    current &&
    !principal.isFetching &&
    !principal.isError &&
    !members.isPending &&
    !members.isFetching &&
    !members.isError;
  const readable =
    ready &&
    (principal.data.roles.includes("system_admin") ||
      members.data?.some((row) => row.user_id === principal.data.id));
  return (
    <section
      className="min-w-0 space-y-3 border-t pt-3"
      aria-label={`Runtime status for ${resource.name}`}
    >
      <h5 className="font-medium">Runtime status</h5>
      <p className="text-sm">
        Read-only observations of the settled workload. Deployment success
        records an earlier outcome; it does not establish current container
        state or readiness.
      </p>
      <Button
        className={button}
        type="button"
        variant="outline"
        disabled={members.isFetching}
        onClick={() => void members.refetch()}
      >
        Refresh runtime status access
      </Button>
      {!ready ? (
        <p role={members.isError ? "alert" : "status"}>
          Current runtime status access cannot be verified. Evidence is cleared.
        </p>
      ) : !readable ? (
        <p role="alert">
          Project viewer access is required to read runtime status.
        </p>
      ) : (
        <StatusReader
          key={JSON.stringify([actor, resource.project_id, resource.id])}
          resourceID={resource.id}
        />
      )}
    </section>
  );
}
function StatusReader(input: { resourceID: string }) {
  const viewModel = useStatusReader(input);
  const {
    error,
    denied,
    busy,
    documentVisible,
    inViewport,
    node,
    paused,
    load,
    status,
    view,
  } = viewModel;
  return (
    <div ref={node} className="min-w-0 space-y-3">
      <p className="text-xs">
        Observation expires after 30 seconds. The collector targets one bounded
        page per 10-second cycle and bounds each sample to 5 seconds. Large
        fleets can become stale between sweeps.
      </p>
      <Button
        className={button}
        type="button"
        variant="outline"
        disabled={busy || denied || !documentVisible || !inViewport}
        onClick={() => {
          paused.current = false;
          void load();
        }}
      >
        {error && !denied ? "Retry runtime status" : "Refresh runtime status"}
      </Button>
      {busy ? <p role="status">Reading runtime status...</p> : null}
      {!documentVisible || !inViewport ? (
        <p>Automatic status reads are paused while this section is hidden.</p>
      ) : null}
      {denied ? (
        <p role="alert">
          Runtime status access is unavailable. Refresh project access before
          continuing. Evidence is cleared.
        </p>
      ) : error ? (
        <p role="alert">
          {error === "timeout"
            ? "Runtime status read timed out after 10 seconds."
            : "Unable to read current runtime status."}{" "}
          Automatic reads are paused. Retained evidence is historical; retry
          explicitly.
        </p>
      ) : null}
      <RuntimeObservation {...viewModel} />
      <section aria-label="Agent heartbeat" className="space-y-1">
        <h6 className="font-medium">Agent heartbeat</h6>
        <p>
          Agent reachability:{" "}
          {view?.reachable ? "Reachable, recent heartbeat" : "Unknown"}
        </p>
        <p className="text-xs">
          Unknown reachability does not mean the container stopped. Heartbeat
          and workload evidence expire independently.
        </p>
        {status?.last_heartbeat_at ? (
          <p className="break-all">
            Last recorded heartbeat: {status.last_heartbeat_at}
          </p>
        ) : null}
      </section>
      <RuntimePlacement {...viewModel} />
      <RuntimeStatusHistory {...viewModel} />
    </div>
  );
}

function useStatusReader({ resourceID }: { resourceID: string }) {
  const [snapshot, setSnapshot] = useState<RuntimeStatusSnapshot>();
  const [error, setError] = useState<"timeout" | "request">(),
    [denied, setDenied] = useState(false),
    [revision, setRevision] = useState(0);
  const [documentVisible, setDocumentVisible] = useState(
      document.visibilityState !== "hidden",
    ),
    [inViewport, setInViewport] = useState(
      typeof IntersectionObserver === "undefined",
    );
  const node = useRef<HTMLDivElement>(null),
    active = useRef(false),
    request = useRef<{ controller: AbortController; timer: number } | null>(
      null,
    ),
    paused = useRef(false);
  const busy = Boolean(request.current);
  const visible = useRef(false);
  useLayoutEffect(() => {
    visible.current = documentVisible && inViewport;
  }, [documentVisible, inViewport]);
  const cancelRead = useCallback(() => {
    const attempt = request.current;
    if (!attempt) return;
    request.current = null;
    window.clearTimeout(attempt.timer);
    attempt.controller.abort();
    if (active.current) {
      setRevision((value) => value + 1);
    }
  }, []);
  const load = useCallback(async () => {
    if (
      !active.current ||
      request.current ||
      paused.current ||
      !visible.current
    )
      return;
    const controller = new AbortController(),
      started = performance.now();
    const attempt = { controller, timer: 0 };
    request.current = attempt;
    setRevision((value) => value + 1);
    attempt.timer = window.setTimeout(() => {
      if (!active.current || request.current !== attempt) return;
      // Retire the attempt before aborting: the transport may ignore cancellation
      // or settle later, after an explicit retry has already started.
      request.current = null;
      window.clearTimeout(attempt.timer);
      controller.abort();
      paused.current = true;
      setError("timeout");
      setRevision((value) => value + 1);
    }, 10_000);
    try {
      const status = await getHostingRuntimeStatus(resourceID, {
        signal: controller.signal,
      });
      if (
        !active.current ||
        request.current !== attempt ||
        controller.signal.aborted
      )
        return;
      setSnapshot({
        status,
        receivedMonotonic: performance.now(),
        receivedWall: Date.now(),
        requestDuration: Math.max(0, performance.now() - started),
      });
      setError(undefined);
    } catch (cause) {
      if (
        !active.current ||
        request.current !== attempt ||
        controller.signal.aborted
      )
        return;
      paused.current = true;
      setError("request");
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) {
        setSnapshot(undefined);
        setDenied(true);
      }
    } finally {
      if (request.current === attempt) {
        request.current = null;
        window.clearTimeout(attempt.timer);
        if (active.current) {
          setRevision((value) => value + 1);
        }
      }
    }
  }, [resourceID]);
  useEffect(() => {
    active.current = true;
    const visibility = () => {
      setDocumentVisible(document.visibilityState !== "hidden");
      setRevision((value) => value + 1);
      if (document.visibilityState === "hidden") cancelRead();
    };
    document.addEventListener("visibilitychange", visibility);
    const observer =
      typeof IntersectionObserver === "undefined"
        ? undefined
        : new IntersectionObserver(([entry]) => {
            const shown = Boolean(entry?.isIntersecting);
            setInViewport(shown);
            if (!shown) cancelRead();
          });
    if (node.current) observer?.observe(node.current);
    return () => {
      active.current = false;
      cancelRead();
      observer?.disconnect();
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [cancelRead]);
  useEffect(() => {
    if (!documentVisible || !inViewport) return;
    if (!snapshot && !error && !busy) {
      void load();
      return;
    }
    if (busy || error || denied) return;
    const timer = window.setTimeout(() => void load(), 10_000);
    return () => window.clearTimeout(timer);
  }, [
    documentVisible,
    inViewport,
    snapshot,
    error,
    denied,
    busy,
    revision,
    load,
  ]);
  useEffect(() => {
    if (!snapshot || !documentVisible || !inViewport) return;
    const remaining = runtimeStatusExpiry(snapshot);
    if (remaining === undefined) return;
    const timer = window.setTimeout(
      () => setRevision((value) => value + 1),
      Math.max(1, remaining),
    );
    return () => window.clearTimeout(timer);
  }, [snapshot, documentVisible, inViewport, revision]);
  const status = snapshot?.status,
    view = snapshot
      ? runtimeStatusView(
          snapshot,
          performance.now(),
          Date.now(),
          Boolean(error),
        )
      : undefined;
  const last = status?.last_observation;

  return {
    error,
    denied,
    busy,
    documentVisible,
    inViewport,
    node,
    paused,
    load,
    status,
    view,
    last,
  };
}

type RuntimeObservationModel = Pick<
  ReturnType<typeof useStatusReader>,
  "view" | "status"
>;
function RuntimeObservation(viewModel: RuntimeObservationModel) {
  const { view, status } = viewModel;

  return (
    <section aria-label="Current runtime observation" className="space-y-1">
      <h6 className="font-medium">Current runtime observation</h6>
      <p>Container state: {containerText[view?.container ?? "unknown"]}</p>
      <p>Readiness: {readinessText[view?.readiness ?? "unknown"]}</p>
      <p>
        Evidence:{" "}
        {view?.fresh
          ? "Fresh observation of the exact current target"
          : "Unknown current state"}
      </p>
      {view?.fresh && view.exitCode !== undefined ? (
        <p>Verified exit code: {view.exitCode}</p>
      ) : null}
      {status ? (
        <p>
          {reasons[status.reason]}
          {!view?.fresh && status.freshness === "fresh"
            ? " Local evidence age or target identity prevents a current-state claim."
            : ""}
        </p>
      ) : null}
      {view?.collectionError ? (
        <p>{collectionErrors[view.collectionError]}</p>
      ) : null}
    </section>
  );
}

type RuntimePlacementModel = Pick<ReturnType<typeof useStatusReader>, "status">;
function RuntimePlacement(viewModel: RuntimePlacementModel) {
  const { status } = viewModel;

  if (!status) return null;
  return (
    <>
      <section aria-label="Saved status placement" className="space-y-1">
        <h6 className="font-medium">Saved placement</h6>
        <dl className="break-all text-sm">
          <dt>Binding</dt>
          <dd>{status.binding_id || "Not bound"}</dd>
          <dt>Destination</dt>
          <dd>{status.destination_id || "Unknown"}</dd>
          <dt>Server / runner</dt>
          <dd>
            {status.server_id || "Unknown"} / {status.runner_id || "Unknown"}
          </dd>
        </dl>
        <p className="text-xs">
          Desired placement is not evidence of a running workload.
        </p>
      </section>
      <section aria-label="Settled deployment target" className="space-y-1">
        <h6 className="font-medium">Settled deployment target</h6>
        <dl className="break-all text-sm">
          <dt>Current target operation</dt>
          <dd>{status.current_operation_id || "No settled target"}</dd>
          <dt>Current target container</dt>
          <dd>{status.current_container_id || "No settled target"}</dd>
        </dl>
        <p className="text-xs">
          This target belongs to the saved healthy deployment record. It does
          not assert current readiness. Review deployment outcomes separately in
          deployment history.
        </p>
      </section>
      {status.mutation_hold || status.restore_ownership_held ? (
        <p role="status">
          Recorded mutation or restore hold. Read-only observation cannot settle
          deployment or release ownership.
        </p>
      ) : null}
    </>
  );
}

type RuntimeStatusHistoryModel = Pick<
  ReturnType<typeof useStatusReader>,
  "last"
>;
function RuntimeStatusHistory(viewModel: RuntimeStatusHistoryModel) {
  const { last } = viewModel;

  return (
    <section aria-label="Historical runtime evidence" className="space-y-1">
      <h6 className="font-medium">Historical runtime evidence</h6>
      {last ? (
        <>
          <p className="text-xs">
            Last accepted evidence, retained for history. It may belong to an
            earlier target and is not a current-state claim.
          </p>
          <dl className="break-all text-sm">
            <dt>Historical operation</dt>
            <dd>{last.operation_id}</dd>
            <dt>Historical container</dt>
            <dd>{last.container_id}</dd>
            <dt>Recorded observation time</dt>
            <dd>{last.observed_at}</dd>
            <dt>Recorded container state</dt>
            <dd>{containerText[last.observation.container_state]}</dd>
            <dt>Recorded readiness</dt>
            <dd>{readinessText[last.observation.readiness]}</dd>
            {last.observation.exit_code !== undefined ? (
              <>
                <dt>Recorded exit code</dt>
                <dd>{last.observation.exit_code}</dd>
              </>
            ) : null}
          </dl>
        </>
      ) : (
        <p>No historical runtime evidence is available.</p>
      )}
    </section>
  );
}
