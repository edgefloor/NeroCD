import { HelpDetails } from "@/components/common/HelpDetails";
import { ApiError, principalQuery } from "@/api";
import {
  currentRead,
  getServerReadiness,
  localReadLifetime,
  type ReadinessSnapshot,
} from "@/api/server-readiness";
import { Button } from "@/components/ui/button";
import { useQuery } from "@tanstack/react-query";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { readinessReasons } from "./serverReadinessReasons";
export function ServerReadiness({
  serverID,
  blocked,
}: {
  serverID: string;
  blocked: boolean;
}) {
  const principal = useQuery(principalQuery());
  if (
    blocked ||
    principal.isPending ||
    principal.isFetching ||
    principal.isError ||
    !principal.data
  )
    return (
      <p role="status">
        Server readiness access is pending or unavailable. Evidence is cleared.
      </p>
    );
  if (
    !Array.isArray(principal.data.roles) ||
    typeof principal.data.id !== "string" ||
    typeof principal.data.provider !== "string" ||
    !principal.data.roles.some((role) =>
      ["system_admin", "runner_admin"].includes(role),
    )
  )
    return (
      <p role="alert">
        Runner administrator access is required to read server readiness.
        Evidence is cleared.
      </p>
    );
  return (
    <ReadinessReader
      key={JSON.stringify([
        principal.data.provider,
        principal.data.id,
        serverID,
      ])}
      serverID={serverID}
    />
  );
}
function ReadinessReader(input: { serverID: string }) {
  const viewModel = useReadinessReader(input);
  const {
    snapshot,
    error,
    denied,
    busy,
    documentVisible,
    inViewport,
    node,
    paused,
    load,
    status,
    current,
  } = viewModel;
  return (
    <section
      ref={node}
      aria-label="Server readiness"
      className="min-w-0 space-y-3 border-t pt-3"
    >
      <h4 className="font-medium">Server readiness</h4>
      <p className="text-sm">
        Check whether this host meets image-hosting prerequisites.
      </p>
      <HelpDetails title="What readiness checks">
        <p>
          Linux and matching native amd64 or arm64 architecture, enabled
          execution, Docker API 1.54, and accessible work and journal roots with
          1 GiB free each. Deployment and restore recovery still require their
          own checks.
        </p>
      </HelpDetails>
      <Button
        type="button"
        variant="outline"
        className="h-auto max-w-full whitespace-normal"
        disabled={busy || denied || !documentVisible || !inViewport}
        onClick={() => {
          paused.current = false;
          void load();
        }}
      >
        {error && !denied
          ? "Retry server readiness"
          : "Refresh server readiness"}
      </Button>
      {busy ? <p role="status">Reading server readiness...</p> : null}
      {!documentVisible || !inViewport ? (
        <p>Automatic reads are paused while this section is hidden.</p>
      ) : null}
      {denied ? (
        <p role="alert">
          Server readiness access is unavailable. Verify current access before
          continuing. Evidence is cleared.
        </p>
      ) : error ? (
        <p role="alert">
          {error === "timeout"
            ? "Server readiness read timed out after 5 seconds."
            : "Unable to read server readiness."}{" "}
          Automatic reads are paused until explicit Retry. Retained details are
          historical.
        </p>
      ) : null}
      <p>Server-reported status: {current ? status?.status : "unknown"}</p>
      <ReadinessCurrentDiagnosis {...viewModel} />
      {snapshot ? (
        <p className="break-all text-xs">
          Last successful refresh, local receipt:{" "}
          {new Date(snapshot.wall).toISOString()}
        </p>
      ) : null}
      <ReadinessHistory {...viewModel} />
    </section>
  );
}

function useReadinessReader({ serverID }: { serverID: string }) {
  const [snapshot, setSnapshot] = useState<ReadinessSnapshot>();
  const [error, setError] = useState<"timeout" | "request">(),
    [denied, setDenied] = useState(false),
    [revision, setRevision] = useState(0);
  const [documentVisible, setDocumentVisible] = useState(
      document.visibilityState !== "hidden",
    ),
    [inViewport, setInViewport] = useState(
      typeof IntersectionObserver === "undefined",
    );
  const node = useRef<HTMLElement>(null),
    active = useRef(false),
    paused = useRef(false),
    request = useRef<{ controller: AbortController; timer: number } | null>(
      null,
    );
  const busy = Boolean(request.current);
  const visible = useRef(false);
  useLayoutEffect(() => {
    visible.current = documentVisible && inViewport;
  }, [documentVisible, inViewport]);
  const cancel = useCallback(() => {
    const attempt = request.current;
    if (attempt) {
      request.current = null;
      window.clearTimeout(attempt.timer);
      attempt.controller.abort();
    }
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
      attempt = { controller, timer: 0 };
    request.current = attempt;
    setRevision((value) => value + 1);
    attempt.timer = window.setTimeout(() => {
      if (!active.current || request.current !== attempt) return;
      request.current = null;
      window.clearTimeout(attempt.timer);
      controller.abort();
      paused.current = true;
      setError("timeout");
      setRevision((value) => value + 1);
    }, 5000);
    try {
      const status = await getServerReadiness(serverID, {
        signal: controller.signal,
      });
      if (
        !active.current ||
        request.current !== attempt ||
        controller.signal.aborted
      )
        return;
      setSnapshot({ status, monotonic: performance.now(), wall: Date.now() });
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
  }, [serverID]);
  useEffect(() => {
    active.current = true;
    const visibility = () => {
      setDocumentVisible(document.visibilityState !== "hidden");
      setRevision((value) => value + 1);
      if (document.visibilityState === "hidden") cancel();
    };
    document.addEventListener("visibilitychange", visibility);
    const observer =
      typeof IntersectionObserver === "undefined"
        ? undefined
        : new IntersectionObserver(([entry]) => {
            const shown = Boolean(entry?.isIntersecting);
            setInViewport(shown);
            if (!shown) cancel();
          });
    if (node.current) observer?.observe(node.current);
    return () => {
      active.current = false;
      cancel();
      observer?.disconnect();
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [cancel]);
  useEffect(() => {
    if (!documentVisible || !inViewport || busy || error || denied) return;
    if (!snapshot) {
      void load();
      return;
    }
    const delay = Math.max(
      0,
      5000 -
        Math.max(
          performance.now() - snapshot.monotonic,
          Date.now() - snapshot.wall,
        ),
    );
    const timer = window.setTimeout(() => void load(), delay);
    return () => window.clearTimeout(timer);
  }, [
    documentVisible,
    inViewport,
    busy,
    error,
    denied,
    snapshot,
    revision,
    load,
  ]);
  useEffect(() => {
    if (!snapshot || !currentRead(snapshot)) return;
    const timer = window.setTimeout(
      () => setRevision((value) => value + 1),
      Math.max(
        1,
        localReadLifetime -
          Math.max(
            performance.now() - snapshot.monotonic,
            Date.now() - snapshot.wall,
          ),
      ),
    );
    return () => window.clearTimeout(timer);
  }, [snapshot, revision]);
  const status = snapshot?.status;
  const current = Boolean(
    snapshot &&
    !error &&
    documentVisible &&
    inViewport &&
    currentRead(snapshot),
  );
  const report = status?.report;

  return {
    snapshot,
    error,
    denied,
    busy,
    documentVisible,
    inViewport,
    node,
    paused,
    load,
    status,
    current,
    report,
  };
}

type ReadinessHistoryModel = Pick<
  ReturnType<typeof useReadinessReader>,
  "status" | "report"
>;
function ReadinessHistory(viewModel: ReadinessHistoryModel) {
  const { status, report } = viewModel;

  if (!status) return null;
  return (
    <section
      aria-label="Historical server readiness details"
      className="min-w-0 space-y-2 break-words text-sm"
    >
      <h5 className="font-medium">Recorded diagnosis and sample</h5>
      <p>
        Historical details from the last successful read. These details do not
        establish current readiness.
      </p>
      <p>Recorded server status: {status.status}</p>
      <p>{readinessReasons[status.reason]}</p>
      {status.observed_at ? (
        <p className="break-all">
          Server observed_at timestamp: {status.observed_at}
        </p>
      ) : null}
      {status.sample_id ? (
        <p className="break-all">Sample: {status.sample_id}</p>
      ) : null}
      {report ? (
        <dl className="grid min-w-0 grid-cols-1 gap-1 break-all">
          <dt>Agent OS / native architecture</dt>
          <dd>
            {report.os} / {report.architecture}
          </dd>
          <dt>Execution / configuration</dt>
          <dd>
            {report.execution} / {report.configuration}
          </dd>
          <dt>Docker / API</dt>
          <dd>
            {report.docker} / {report.docker_api_version || "unknown"}
          </dd>
          <dt>Daemon OS / native architecture</dt>
          <dd>
            {report.daemon_os} / {report.daemon_architecture}
          </dd>
          <dt>Docker CLI</dt>
          <dd>{report.cli}</dd>
          <dt>Work root / free bytes</dt>
          <dd>
            {report.work_root} / {report.work_free_bytes}
          </dd>
          <dt>Journal root / free bytes</dt>
          <dd>
            {report.journal_root} / {report.journal_free_bytes}
          </dd>
          <dt>Collection result</dt>
          <dd>{report.error || "No collection error"}</dd>
        </dl>
      ) : (
        <p>No accepted sample is available.</p>
      )}
    </section>
  );
}

type ReadinessCurrentDiagnosisModel = Pick<
  ReturnType<typeof useReadinessReader>,
  "current" | "status"
>;
function ReadinessCurrentDiagnosis(viewModel: ReadinessCurrentDiagnosisModel) {
  const { current, status } = viewModel;

  if (!(current && status))
    return (
      <p>
        Current diagnosis is unavailable. The local display expires 5 seconds
        after receipt. Client time does not establish the sample's server
        freshness.
      </p>
    );
  return <p>{readinessReasons[status.reason]}</p>;
}
