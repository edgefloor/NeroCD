import { ApiError, principalQuery } from "@/api";
import type { Resource } from "@/api/hosting";
import {
  appendLogRows,
  closeRuntimeLogs,
  displayLogText,
  leaseRemaining,
  logLease,
  newLogOpen,
  openRuntimeLogs,
  readRuntimeLogs,
  renewRuntimeLogs,
  runtimeLogTargetsQuery,
  type LogLease,
  type LogOpen,
  type LogPage,
  type LogRow,
  type LogScope,
  type LogSession,
} from "@/api/hosting-runtime-logs";
import { currentGitStackQuery } from "@/api/hosting-stack-lifecycle";
import { Button } from "@/components/ui/button";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

type Kind = "open" | "read" | "renew" | "close";
type State = {
  session?: LogSession;
  lease?: LogLease;
  opening?: LogOpen;
  rows: LogRow[];
  cursor: number;
  markers: string[];
  following: boolean;
  draining: boolean;
  complete: boolean;
  trimmed: number;
  more: boolean;
  gap: boolean;
  error?: Kind;
  conflict: boolean;
  denied: boolean;
  expired: boolean;
  closeUnconfirmed: boolean;
};
const empty = (): State => ({
  rows: [],
  cursor: 0,
  markers: [],
  following: false,
  draining: false,
  complete: false,
  trimmed: 0,
  more: false,
  gap: false,
  conflict: false,
  denied: false,
  expired: false,
  closeUnconfirmed: false,
});
const terminal = (session: LogSession) =>
  !["waiting", "following", "disconnected"].includes(session.state) ||
  session.ended_at !== null;
const buttonClass = "h-auto max-w-full whitespace-normal";
export function ResourceRuntimeLogs({
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
      <p role="status">
        Runtime log access is pending or unavailable. Output is cleared.
      </p>
    );
  const key = JSON.stringify([
    identity.data.provider,
    identity.data.id,
    resource.project_id,
    resource.id,
  ]);
  const scope = {
    actor: identity.data.id,
    project: resource.project_id,
    resource: resource.id,
  };
  if (!(resource.source.kind === "git"))
    return (
      <LogOutput
        key={key}
        scope={scope}
        name={resource.name}
        compose={resource.source.kind === "compose"}
      />
    );
  return (
    <GitLogOutput
      key={key}
      scope={scope}
      name={resource.name}
      actor={JSON.stringify([identity.data.provider, identity.data.id])}
    />
  );
}
function GitLogOutput({
  scope,
  name,
  actor,
}: {
  scope: LogScope;
  name: string;
  actor: string;
}) {
  const current = useQuery(currentGitStackQuery({ ...scope, actor }));
  if (current.isPending || current.isFetching || current.isError)
    return (
      <section
        aria-label={`Runtime logs for ${name}`}
        className="min-w-0 space-y-2 border-t pt-3"
      >
        <p role={current.isError ? "alert" : "status"}>
          Recorded deployment provenance is pending or unavailable. Runtime log
          output is cleared.
        </p>
        <Button
          variant="outline"
          disabled={current.isFetching}
          onClick={() => void current.refetch()}
        >
          Refresh runtime log deployment evidence
        </Button>
      </section>
    );
  return (
    <div className="space-y-2">
      <Button
        variant="outline"
        disabled={current.isFetching}
        onClick={() => void current.refetch()}
      >
        Refresh runtime log deployment evidence
      </Button>
      <LogOutput
        key={current.data.operation?.id ?? "single-image"}
        scope={scope}
        name={name}
        compose={Boolean(current.data.operation)}
        expectedOperationID={current.data.operation?.id}
      />
    </div>
  );
}
function LogOutput({
  scope,
  name,
  compose,
  expectedOperationID,
}: {
  scope: LogScope;
  name: string;
  compose: boolean;
  expectedOperationID?: string;
}) {
  const [shown, setShown] = useState(true);
  return (
    <section
      aria-label={`Runtime logs for ${name}`}
      className="min-w-0 space-y-3 border-t pt-3"
    >
      <h5 className="font-medium">Runtime logs</h5>
      <p className="text-sm">
        Sensitive application output may contain secrets. Follow only when
        needed. Output stays in this mounted view and is cleared when hidden or
        access changes.
      </p>
      {shown ? (
        compose ? (
          <StackLogReader
            scope={scope}
            hide={() => setShown(false)}
            expectedOperationID={expectedOperationID}
          />
        ) : (
          <LogReader scope={scope} hide={() => setShown(false)} />
        )
      ) : (
        <>
          <p>
            Runtime log output is hidden and cleared. Collection may remain
            admitted until its demand expires; hiding does not guarantee server
            closure.
          </p>
          <Button
            className={buttonClass}
            variant="outline"
            onClick={() => setShown(true)}
          >
            Show runtime log controls
          </Button>
        </>
      )}
    </section>
  );
}
type Selection = { component: string; expected_operation_id: string };
function StackLogReader({
  scope,
  hide,
  expectedOperationID,
}: {
  scope: LogScope;
  hide: () => void;
  expectedOperationID?: string;
}) {
  const targets = useQuery(runtimeLogTargetsQuery(scope));
  const [selected, select] = useState("");
  const ready =
    targets.isSuccess &&
    !targets.isFetching &&
    (!expectedOperationID || targets.data.operation_id === expectedOperationID);
  const operationID = ready ? targets.data.operation_id : undefined;
  const choices = ready ? targets.data.components : [];
  const component = choices.find((item) => item.component === selected);
  return (
    <div className="min-w-0 space-y-3">
      <label className="block space-y-1">
        <span>Compose component</span>
        <select
          className="block max-w-full rounded border bg-background p-2"
          value={component?.component ?? ""}
          onChange={(event) => select(event.target.value)}
        >
          <option value="">Select a component</option>
          {choices.map((item) => (
            <option key={item.component} value={item.component}>
              {item.component}
              {item.running
                ? " · running"
                : ` · stopped, exit ${item.exit_code}`}
            </option>
          ))}
        </select>
      </label>
      <Button
        className={buttonClass}
        variant="outline"
        disabled={targets.isFetching}
        onClick={() => {
          select("");
          void targets.refetch();
        }}
      >
        Refresh component targets
      </Button>
      {component && operationID ? (
        <LogReader
          key={JSON.stringify([operationID, component.component])}
          scope={scope}
          hide={hide}
          selection={{
            component: component.component,
            expected_operation_id: operationID,
          }}
        />
      ) : (
        <>
          <p role="status">
            {ready
              ? "Select a component, then choose Follow runtime logs. Selection alone opens no demand."
              : "Settled Compose component evidence is pending or unavailable. Output is cleared."}
          </p>
          <Button className={buttonClass} variant="outline" onClick={hide}>
            Hide output
          </Button>
        </>
      )}
    </div>
  );
}
function closeDetached(scope: LogScope, session: LogSession) {
  const controller = new AbortController(),
    timer = window.setTimeout(() => controller.abort(), 5000);
  void closeRuntimeLogs(scope, session, controller.signal)
    .catch(() => undefined)
    .finally(() => window.clearTimeout(timer));
}
function LogReader(input: {
  scope: LogScope;
  hide: () => void;
  selection?: Selection;
}) {
  const viewModel = useLogReader(input);
  const { node, visible, current, busy } = viewModel;
  return (
    <div ref={node} className="min-w-0 space-y-3">
      <RuntimeLogControls {...viewModel} />
      {busy ? (
        <p role="status">Reading or updating runtime log demand...</p>
      ) : null}
      {current.denied ? (
        <p role="alert">
          Runtime log access is unavailable. Sensitive output and session state
          are cleared.
        </p>
      ) : null}
      {current.expired ? (
        <p role="status">
          Runtime log cache expired. Output is cleared. Follow explicitly to
          request a new session.
        </p>
      ) : null}
      {current.error ? (
        <p role="alert">
          {current.conflict
            ? "The runtime log request conflicted with current server state. No quota or target cause is assumed."
            : "Runtime log request paused, failed, timed out, or returned invalid evidence."}{" "}
          Automatic reads and renewals are paused. Retained rows are historical.
          Retry explicitly.
          {current.opening
            ? " Open outcome may be unknown; Retry reuses the exact original request and deadline."
            : ""}
        </p>
      ) : null}
      {!visible.current ? (
        <p>
          Log reads and renewals are paused while hidden. Return requires
          explicit Resume or Follow.
        </p>
      ) : null}
      {current.closeUnconfirmed ? (
        <p>
          Close is unconfirmed. Renewal is disabled; already admitted collection
          may remain until its demand expires.
        </p>
      ) : null}
      <RuntimeLogSession {...viewModel} />
    </div>
  );
}

function useLogReader({
  scope,
  hide,
  selection,
}: {
  scope: LogScope;
  hide: () => void;
  selection?: Selection;
}) {
  const [initialState] = useState(empty);
  const state = useRef<State>(initialState),
    active = useRef(false),
    node = useRef<HTMLDivElement>(null);
  const attempts = useRef<
    Partial<Record<Kind, { controller: AbortController; timer: number }>>
  >({});
  const visible = useRef(false),
    documentVisible = useRef(document.visibilityState !== "hidden"),
    viewport = useRef(typeof IntersectionObserver === "undefined");
  const lastRead = useRef(-Infinity),
    lastRenew = useRef(0),
    [, render] = useState(0);
  const update = () => {
    if (active.current) render((value) => value + 1);
  };
  const abortAll = () => {
    for (const kind of Object.keys(attempts.current) as Kind[]) {
      const attempt = attempts.current[kind];
      delete attempts.current[kind];
      if (attempt) {
        window.clearTimeout(attempt.timer);
        attempt.controller.abort();
      }
    }
  };
  const remaining = () =>
    state.current.lease ? leaseRemaining(state.current.lease) : 0;
  const pause = (kind: Kind, cause?: unknown) => {
    abortAll();
    const current = state.current;
    current.following = false;
    current.draining = false;
    if (cause instanceof ApiError && [401, 403, 404].includes(cause.status))
      state.current = { ...empty(), denied: true };
    else if (cause instanceof ApiError && cause.status === 410)
      state.current = { ...empty(), expired: true };
    else {
      current.error = kind;
      current.conflict = cause instanceof ApiError && cause.status === 409;
    }
    update();
  };
  async function run<T>(
    kind: Kind,
    work: (signal: AbortSignal) => Promise<T>,
    accept: (result: T, duration: number) => void,
  ) {
    if (!active.current || attempts.current[kind] || !visible.current) return;
    const started = performance.now(),
      attempt = { controller: new AbortController(), timer: 0 };
    attempts.current[kind] = attempt;
    attempt.timer = window.setTimeout(() => {
      if (attempts.current[kind] !== attempt || !active.current) return;
      // Remove ownership before abort, including transports that ignore abort.
      delete attempts.current[kind];
      window.clearTimeout(attempt.timer);
      attempt.controller.abort();
      pause(kind);
    }, 5000);
    update();
    try {
      const result = await work(attempt.controller.signal);
      if (
        !active.current ||
        attempts.current[kind] !== attempt ||
        attempt.controller.signal.aborted
      ) {
        if (!active.current && selection && kind === "open")
          closeDetached(scope, result as LogSession);
        return;
      }
      accept(result, Math.max(0, performance.now() - started));
    } catch (cause) {
      if (
        active.current &&
        attempts.current[kind] === attempt &&
        !attempt.controller.signal.aborted
      )
        pause(kind, cause);
    } finally {
      if (attempts.current[kind] === attempt) {
        delete attempts.current[kind];
        window.clearTimeout(attempt.timer);
        update();
      }
    }
  }
  const acceptSession = (
    session: LogSession,
    duration: number,
    renewed = false,
  ) => {
    const current = state.current;
    current.session = session;
    current.lease = logLease(
      session,
      duration,
      renewed ? undefined : current.lease,
    );
    if (terminal(session)) {
      current.following = false;
      current.draining = true;
      current.lease.remaining = 0;
    }
  };
  const read = () => {
    const current = state.current,
      session = current.session;
    if (
      !session ||
      performance.now() - lastRead.current < 3000 ||
      attempts.current.read
    )
      return;
    const after = current.cursor;
    lastRead.current = performance.now();
    void run(
      "read",
      (signal) => readRuntimeLogs(scope, session, after, signal),
      (page: LogPage, duration) => {
        acceptSession(page.session, duration);
        const latest = state.current;
        const bounded = appendLogRows(latest.rows, page.items);
        latest.rows = bounded.rows;
        latest.trimmed += bounded.dropped;
        latest.cursor = page.next_sequence;
        latest.markers = [...new Set([...latest.markers, ...page.markers])];
        latest.gap ||= page.gap;
        latest.more = page.truncated;
        latest.complete = terminal(page.session) && !page.truncated;
        if (latest.complete) latest.draining = false;
      },
    );
  };
  const open = (retry = false) => {
    abortAll();
    if (!retry) {
      state.current = empty();
      state.current.opening = newLogOpen(Date.now(), selection);
      lastRead.current = -Infinity;
    }
    const body = state.current.opening;
    if (!body) return;
    state.current.error = undefined;
    state.current.conflict = false;
    void run(
      "open",
      (signal) => openRuntimeLogs(scope, body, signal),
      (session, duration) => {
        acceptSession(session, duration, true);
        state.current.opening = undefined;
        state.current.following = !terminal(session) && remaining() > 0;
        lastRenew.current = performance.now();
        read();
      },
    );
  };
  const resume = () => {
    const current = state.current;
    current.error = undefined;
    current.conflict = false;
    current.following = Boolean(
      current.session && !terminal(current.session) && remaining() > 0,
    );
    current.draining = Boolean(
      current.session &&
        (terminal(current.session) || remaining() <= 0) &&
        !current.complete,
    );
    // Resuming does not renew immediately or manufacture a fresh demand lease.
    lastRenew.current = performance.now();
    read();
    update();
  };
  const stop = () => {
    const current = state.current;
    current.following = false;
    current.draining = false;
    abortAll();
    current.error = undefined;
    current.closeUnconfirmed = Boolean(current.session || current.opening);
    if (current.session) {
      const original = current.session;
      void run(
        "close",
        (signal) => closeRuntimeLogs(scope, original, signal),
        (session, duration) => {
          acceptSession(session, duration);
          state.current.closeUnconfirmed = false;
          state.current.draining = !state.current.complete;
        },
      );
    }
    update();
  };
  useEffect(() => {
    active.current = true;
    const visibility = () => {
      visible.current = documentVisible.current && viewport.current;
      if (!visible.current) {
        state.current.following = false;
        state.current.draining = false;
        if (attempts.current.open && state.current.opening)
          state.current.error = "open";
        abortAll();
      }
      update();
    };
    const documentChanged = () => {
      documentVisible.current = document.visibilityState !== "hidden";
      visibility();
    };
    document.addEventListener("visibilitychange", documentChanged);
    const observer =
      typeof IntersectionObserver === "undefined"
        ? undefined
        : new IntersectionObserver(([entry]) => {
            viewport.current = Boolean(entry?.isIntersecting);
            visibility();
          });
    if (node.current) observer?.observe(node.current);
    visibility();
    let timer: number;
    const cycle = () => {
      if (!active.current) return;
      const current = state.current,
        live = remaining();
      if (current.following && live <= 0) {
        current.following = false;
        current.draining = false;
        abortAll();
      }
      if (visible.current && !current.error && !current.denied) {
        if (current.following || current.draining) read();
        if (
          current.following &&
          current.session &&
          live > 0 &&
          performance.now() - lastRenew.current >= 10_000 &&
          !attempts.current.renew
        ) {
          const original = current.session;
          lastRenew.current = performance.now();
          void run(
            "renew",
            (signal) => renewRuntimeLogs(scope, original, signal),
            (session, duration) => acceptSession(session, duration, true),
          );
        }
      }
      update();
      timer = window.setTimeout(
        cycle,
        Math.max(1, Math.min(1000, remaining() || 1000)),
      );
    };
    timer = window.setTimeout(cycle, 1000);
    return () => {
      active.current = false;
      abortAll();
      if (selection && state.current.session)
        closeDetached(scope, state.current.session);
      state.current = empty();
      window.clearTimeout(timer);
      observer?.disconnect();
      document.removeEventListener("visibilitychange", documentChanged);
    };
    // Controller lifetime is exactly the actor/project/resource keyed mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const current = state.current,
    busy = Object.keys(attempts.current).length > 0,
    live = remaining() > 0;
  return {
    hide,
    state,
    node,
    visible,
    read,
    open,
    resume,
    stop,
    current,
    busy,
    live,
  };
}

type RuntimeLogControlsModel = Pick<
  ReturnType<typeof useLogReader>,
  "busy" | "visible" | "current" | "live" | "open" | "resume" | "stop" | "hide"
>;
function RuntimeLogControls(viewModel: RuntimeLogControlsModel) {
  const { busy, visible, current, live, open, resume, stop, hide } = viewModel;

  return (
    <div className="flex flex-wrap gap-2">
      <Button
        className={buttonClass}
        variant="outline"
        disabled={
          busy ||
          !visible.current ||
          current.denied ||
          current.following ||
          Boolean(current.session && live && !terminal(current.session))
        }
        onClick={() => open()}
      >
        Follow runtime logs
      </Button>
      <RuntimeLogResumption {...viewModel} />{" "}
      {current.error ? (
        <Button
          className={buttonClass}
          variant="outline"
          disabled={busy || !visible.current}
          onClick={() =>
            current.error === "open"
              ? open(true)
              : current.error === "close"
                ? stop()
                : resume()
          }
        >
          Retry runtime logs
        </Button>
      ) : null}
      {(current.session && !terminal(current.session)) ||
      current.opening ||
      current.closeUnconfirmed ? (
        <Button
          className={buttonClass}
          variant="outline"
          disabled={!visible.current}
          onClick={stop}
        >
          Stop following
        </Button>
      ) : null}
      <Button className={buttonClass} variant="outline" onClick={hide}>
        Hide output
      </Button>
    </div>
  );
}

type RuntimeLogSessionModel = Pick<
  ReturnType<typeof useLogReader>,
  "current" | "live"
>;
function RuntimeLogSession(viewModel: RuntimeLogSessionModel) {
  const { current, live } = viewModel;

  if (!current.session)
    return (
      <p>
        {current.opening
          ? "A Follow attempt has no confirmed session response yet. Already admitted demand may still exist."
          : "No runtime log demand is opened until you choose Follow runtime logs."}
      </p>
    );
  return (
    <>
      <p>
        Session state: {current.session.state}.{" "}
        {current.following && live && !current.error
          ? "Following requested while visible."
          : "Historical output; no live-follow claim."}
      </p>
      {!live ? (
        <p>
          Demand expired or ended. No renewal is automatic; a new Follow is
          required for new demand.
        </p>
      ) : null}
      {current.session.state === "waiting" ? (
        <p>Waiting for a compatible authorized collector.</p>
      ) : null}
      {current.session.state === "disconnected" ? (
        <p>
          Collector disconnected. Cached rows do not prove current collection.
        </p>
      ) : null}
      {current.session.state === "target_changed" ? (
        <p>
          The target changed. This session remains attached to its old target;
          start a new Follow explicitly.
        </p>
      ) : null}
      {current.session.state === "unsupported_driver" ? (
        <p>
          The collector reported an unsupported log driver. Retained history may
          still be read.
        </p>
      ) : null}
      <RuntimeLogIdentity {...viewModel} />
      <RuntimeLogMarkers {...viewModel} /> <RuntimeLogRows {...viewModel} />
    </>
  );
}

type RuntimeLogMarkersModel = Pick<ReturnType<typeof useLogReader>, "current">;
function RuntimeLogMarkers(viewModel: RuntimeLogMarkersModel) {
  const { current } = viewModel;

  return (
    <>
      {current.markers.includes("source_gap") ? (
        <p>
          Source gap: collection coverage is incomplete, independently of
          retained server rows.
        </p>
      ) : null}
      {current.markers.includes("prefix_evicted") ? (
        <p>
          Retention prefix removed: earlier server sequences are no longer
          available.
        </p>
      ) : null}
      {current.gap && !current.markers.includes("source_gap") ? (
        <p>A server sequence gap was reported.</p>
      ) : null}
      {current.more ? (
        <p>
          Page truncated: more bounded pages remain; this is separate from
          message clipping.
        </p>
      ) : null}
      {current.trimmed > 0 ? (
        <p>
          Local buffer trimmed {current.trimmed} complete rows to stay within
          500 rows and 1 MiB UTF-8.
        </p>
      ) : null}
      {current.complete ? (
        <p>Closed session history is fully drained.</p>
      ) : null}
      {!current.rows.length ? (
        <p>No retained runtime log rows have been received.</p>
      ) : null}
    </>
  );
}

type RuntimeLogRowsModel = Pick<ReturnType<typeof useLogReader>, "current">;
function RuntimeLogRows(viewModel: RuntimeLogRowsModel) {
  const { current } = viewModel;

  return (
    <ol aria-label="Sensitive runtime log rows" className="min-w-0 space-y-2">
      {current.rows.map((row) => (
        <li key={row.sequence} className="min-w-0 rounded border p-2">
          <p className="break-all text-xs">
            Server sequence {row.sequence} · {row.component} · {row.stream} ·
            Source time {row.source_timestamp ?? "unavailable"} · Received{" "}
            {row.received_at}
          </p>
          {row.message_truncated ? (
            <p className="text-xs">Message clipped by producer or server.</p>
          ) : null}
          <pre className="whitespace-pre-wrap break-all font-mono text-xs">
            {displayLogText(row.message)}
          </pre>
        </li>
      ))}
    </ol>
  );
}

type RuntimeLogIdentityModel = Pick<ReturnType<typeof useLogReader>, "current">;
function RuntimeLogIdentity(viewModel: RuntimeLogIdentityModel) {
  const { current } = viewModel;

  if (!current.session) return null;
  return (
    <dl className="break-all text-xs">
      <dt>Immutable session</dt>
      <dd>{current.session.id}</dd>
      <dt>Original component / container name</dt>
      <dd>
        {current.session.target.component ?? "application"} /{" "}
        {current.session.target.container_name ?? "not recorded"}
      </dd>
      <dt>Original operation / container</dt>
      <dd>
        {current.session.target.operation_id} /{" "}
        {current.session.target.container_id}
      </dd>
      <dt>Original runner / daemon / image</dt>
      <dd>
        {current.session.target.runner_id} / {current.session.target.daemon_id}{" "}
        / {current.session.target.image_id}
      </dd>
    </dl>
  );
}

type RuntimeLogResumptionModel = Pick<
  ReturnType<typeof useLogReader>,
  "current" | "live" | "busy" | "visible" | "resume"
>;
function RuntimeLogResumption(viewModel: RuntimeLogResumptionModel) {
  const { current, live, busy, visible, resume } = viewModel;

  return (
    <>
      {current.session &&
      !current.following &&
      live &&
      !terminal(current.session) &&
      !current.error ? (
        <Button
          className={buttonClass}
          variant="outline"
          disabled={busy || !visible.current}
          onClick={resume}
        >
          Resume runtime logs
        </Button>
      ) : null}
      {current.session &&
      !current.following &&
      (!live || terminal(current.session)) &&
      !current.complete &&
      !current.error ? (
        <Button
          className={buttonClass}
          variant="outline"
          disabled={busy || !visible.current}
          onClick={resume}
        >
          Read retained history
        </Button>
      ) : null}
    </>
  );
}
