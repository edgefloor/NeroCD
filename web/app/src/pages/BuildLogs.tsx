import { ApiError } from "@/api/errors";
import {
  getHostingBuildLogs,
  type HostingBuildLogItem,
  type HostingBuildLogPage,
} from "@/api/hosting-builds";
import { Button } from "@/components/ui/button";
import { useCallback, useEffect, useRef, useState } from "react";

const buttonClass = "h-auto max-w-full whitespace-normal break-all";
const integer = (value: unknown): value is number =>
  Number.isInteger(value) && Number(value) >= 0 && Number(value) <= 2147483647;
function validatePage(page: HostingBuildLogPage, cursor: number) {
  if (
    !page ||
    !Array.isArray(page.items) ||
    page.items.length > 200 ||
    !integer(page.next_sequence) ||
    page.next_sequence < cursor ||
    !integer(page.earliest_available_sequence) ||
    !integer(page.deleted_through_sequence) ||
    ![page.gap, page.terminal, page.truncated, page.history_complete].every(
      (value) => typeof value === "boolean",
    ) ||
    (page.truncated && page.next_sequence <= cursor)
  )
    throw new Error("Invalid output page");
  let previous = cursor;
  for (const item of page.items) {
    if (
      !item ||
      !integer(item.sequence) ||
      item.sequence <= previous ||
      item.sequence > page.next_sequence ||
      !["", "build", "buildkit", "railpack"].includes(item.component) ||
      typeof item.stream !== "string" ||
      typeof item.message !== "string" ||
      typeof item.created_at !== "string" ||
      (item.source_timestamp !== undefined &&
        typeof item.source_timestamp !== "string") ||
      (item.message_truncated !== undefined &&
        typeof item.message_truncated !== "boolean")
    )
      throw new Error("Invalid output row");
    previous = item.sequence;
  }
  if (page.items.length && previous !== page.next_sequence)
    throw new Error("Invalid output cursor");
  if (
    !page.items.length &&
    page.next_sequence > cursor &&
    page.next_sequence > page.deleted_through_sequence
  )
    throw new Error("Unverified output cursor");
}
type Scope = {
  actor: string;
  project: string;
  resource: string;
  build: string;
  blocked: boolean;
};
export function BuildLogs(props: Scope) {
  // Unmounting removes plaintext synchronously from the rendered tree, including
  // when a parent loses authority but retains old metadata in its query cache.
  if (!props.blocked)
    return (
      <Output
        key={JSON.stringify([
          props.actor,
          props.project,
          props.resource,
          props.build,
        ])}
        build={props.build}
      />
    );
  return null;
}
function Output({ build }: { build: string }) {
  const [open, setOpen] = useState(false);
  return (
    <section className="min-w-0 space-y-2" aria-label="Persisted build output">
      {open ? (
        <>
          <Button
            className={buttonClass}
            type="button"
            variant="outline"
            onClick={() => setOpen(false)}
          >
            Close build output
          </Button>
          <OpenOutput build={build} />
        </>
      ) : (
        <Button
          className={buttonClass}
          type="button"
          variant="outline"
          onClick={() => setOpen(true)}
        >
          Show build output
        </Button>
      )}
    </section>
  );
}
function OpenOutput({ build }: { build: string }) {
  const [output, setOutput] = useState<{
    rows: HostingBuildLogItem[];
    trimmed: boolean;
  }>({ rows: [], trimmed: false });
  const { rows, trimmed } = output;
  const [page, setPage] = useState<HostingBuildLogPage>();
  const [gap, setGap] = useState(false);
  const [error, setError] = useState(""),
    [denied, setDenied] = useState(false);
  const [revision, setRevision] = useState(0);
  const cursor = useRef(0),
    inflight = useRef<AbortController | null>(null),
    active = useRef(false),
    paused = useRef(false);
  const busy = Boolean(inflight.current);
  const load = useCallback(async () => {
    if (
      !active.current ||
      inflight.current ||
      paused.current ||
      document.visibilityState === "hidden"
    )
      return;
    const controller = new AbortController();
    inflight.current = controller;
    setRevision((value) => value + 1);
    try {
      const next = await getHostingBuildLogs(build, cursor.current, {
        signal: controller.signal,
      });
      if (!active.current || controller.signal.aborted) return;
      validatePage(next, cursor.current);
      cursor.current = next.next_sequence;
      // Retain a bounded window of complete rows, counting all retained strings
      // as UTF-8 rather than UTF-16 code units.
      setOutput((old) => {
        const window = [
          ...old.rows,
          ...next.items.map(
            ({
              sequence,
              component,
              stream,
              message,
              created_at,
              source_timestamp,
              message_truncated,
            }) => ({
              sequence,
              component,
              stream,
              message,
              created_at,
              source_timestamp,
              message_truncated,
            }),
          ),
        ];
        const encoder = new TextEncoder();
        const sizes = window.map(
          (item) => encoder.encode(JSON.stringify(item)).byteLength,
        );
        let bytes = sizes.reduce((sum, size) => sum + size, 0),
          removed = 0;
        while (window.length - removed > 500 || bytes > 1024 * 1024)
          bytes -= sizes[removed++]!;
        return {
          rows: window.slice(removed),
          trimmed: old.trimmed || removed > 0,
        };
      });
      setGap((old) => old || next.gap);
      setPage({ ...next, items: [] });
      setError("");
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      paused.current = true;
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) {
        cursor.current = 0;
        setOutput({ rows: [], trimmed: false });
        setPage(undefined);
        setGap(false);
        setDenied(true);
        setError(
          "Build output is unavailable under current access. Close output and refresh build access.",
        );
      } else
        setError(
          "Unable to read build output. The retained cursor is unchanged. Retry to resume.",
        );
    } finally {
      if (inflight.current === controller) inflight.current = null;
      if (active.current && !controller.signal.aborted) {
        setRevision((old) => old + 1);
      }
    }
  }, [build]);
  useEffect(() => {
    active.current = true;
    void load();
    const revalidate = () => {
      if (document.visibilityState !== "hidden") void load();
    };
    window.addEventListener("focus", revalidate);
    document.addEventListener("visibilitychange", revalidate);
    return () => {
      active.current = false;
      inflight.current?.abort();
      window.removeEventListener("focus", revalidate);
      document.removeEventListener("visibilitychange", revalidate);
    };
  }, [load]);
  useEffect(() => {
    if (busy || error || (page?.terminal && !page.truncated)) return;
    const timer = window.setTimeout(() => void load(), 3000);
    return () => window.clearTimeout(timer);
  }, [busy, error, load, page, revision]);

  return (
    <div className="min-w-0 space-y-2">
      <p className="text-xs">
        Persisted build output, ordered by server sequence. This is build
        output, not application runtime logs.
      </p>
      <Button
        className={buttonClass}
        type="button"
        variant="outline"
        disabled={busy || denied}
        onClick={() => {
          paused.current = false;
          void load();
        }}
      >
        {error && !denied ? "Retry build output" : "Refresh build output"}
      </Button>
      {busy ? <p role="status">Reading build output...</p> : null}
      {error ? <p role="alert">{error}</p> : null}
      {page && !page.history_complete ? (
        <p role="status">
          Build history is incomplete. Older output may never have been
          recorded.
        </p>
      ) : null}
      {gap ? (
        <p role="status">
          A server history gap was reported. Requested output was removed or its
          deletion history is unknown.
        </p>
      ) : null}
      {page?.truncated ? (
        <p role="status">
          More retained server output is available. The next poll or refresh
          reads one page.
        </p>
      ) : null}
      {trimmed ? (
        <p role="status">
          The local output window was trimmed to 500 rows and 1 MiB. Close and
          reopen to read from the beginning.
        </p>
      ) : null}
      {page && !rows.length ? <p>No retained output in this window.</p> : null}
      {page?.terminal && !page.truncated ? (
        <p role="status">
          Recorded output reached the terminal build. Automatic output polling
          stopped.
        </p>
      ) : null}
      <ol aria-label="Build output rows" className="min-w-0 space-y-2">
        {rows.map((row) => (
          <li key={row.sequence} className="min-w-0 rounded border p-2 text-xs">
            <p className="break-all">
              Sequence {row.sequence} · component{" "}
              {row.component || "unspecified"} · stream {row.stream} · recorded{" "}
              {row.created_at}
              {row.source_timestamp
                ? ` · source time ${row.source_timestamp}`
                : ""}
            </p>
            <pre className="min-w-0 whitespace-pre-wrap break-all font-mono">
              {row.message}
            </pre>
            {row.message_truncated ? (
              <p>Server shortened this message.</p>
            ) : null}
          </li>
        ))}
      </ol>
    </div>
  );
}
