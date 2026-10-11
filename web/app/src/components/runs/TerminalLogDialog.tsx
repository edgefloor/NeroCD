import { ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { LoaderCircle, X } from "lucide-react";
import { shouldPollRun, type RunLog, type TaskRun } from "@/api";
import { Dialog, DialogClose, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { matchesQuery } from "@/lib/format";
import { formatLoadedRunLogs } from "./logExport";

let pendingOpener: HTMLElement | null = null;

export function rememberTerminalLogOpener(opener: HTMLElement): void {
  pendingOpener = opener;
}

export function TerminalLogDialog({
  run,
  logs,
  loading = false,
  error,
  onRetry,
  retrying,
  following = false,
  hasPrevious = false,
  loadingPrevious = false,
  previousError,
  onLoadPrevious,
  open,
  onOpenChange,
}: {
  run?: TaskRun;
  logs: RunLog[];
  loading?: boolean;
  error?: Error;
  onRetry?: () => void;
  retrying?: boolean;
  following?: boolean;
  hasPrevious?: boolean;
  loadingPrevious?: boolean;
  previousError?: Error;
  onLoadPrevious?: () => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}): ReactNode {
  const orderedLogs = useMemo(() => [...logs].sort((a, b) => a.sequence - b.sequence), [logs]);
  const openerRef = useRef<HTMLElement | null>(null);
  const filterRef = useRef<HTMLInputElement | null>(null);
  const [filter, setFilter] = useState("");
  useEffect(() => setFilter(""), [run?.id, open]);
  const filtering = Boolean(filter.trim());
  const visibleLogs = useMemo(() => orderedLogs.filter((log) => matchesQuery(filter, log.message, log.stream, `#${log.sequence}`)), [orderedLogs, filter]);

  function downloadLoadedOutput() {
    const runID = run?.id ?? "run";
    const blob = new Blob([formatLoadedRunLogs(runID, orderedLogs)], { type: "text/plain;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `${runID.replace(/[^a-zA-Z0-9_-]/g, "_").slice(0, 128)}-loaded.log`;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        aria-describedby={undefined}
        className="min-w-0 grid-cols-1 grid-rows-[auto_minmax(0,1fr)] max-h-[calc(100dvh-2rem)] gap-0 overflow-hidden rounded-lg p-0 sm:max-w-4xl border-border bg-background"
        onOpenAutoFocus={() => {
          openerRef.current = pendingOpener;
          pendingOpener = null;
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          const opener = openerRef.current;
          const runID = opener?.dataset.runLogTrigger ?? run?.id;
          openerRef.current = null;
          requestAnimationFrame(() => {
            if (opener?.isConnected && opener.getClientRects().length > 0) {
              opener.focus();
              return;
            }
            // Loading and responsive layouts can replace or hide the original button.
            Array.from(document.querySelectorAll<HTMLButtonElement>("[data-run-log-trigger]"))
              .find((button) => button.dataset.runLogTrigger === runID && button.getClientRects().length > 0)?.focus();
          });
        }}
      >
        <DialogHeader className="min-w-0 border-b border-border bg-muted px-4 py-3">
          <div className="flex min-w-0 items-center justify-between gap-3">
            <div className="flex min-w-0 flex-1 items-center gap-3">
              <DialogTitle className="min-w-0 truncate font-mono text-sm text-foreground">
                {run ? `run ${run.id}` : "run terminal"}
              </DialogTitle>
              {run ? (
                <span className="shrink-0 rounded bg-muted px-2 py-0.5 font-mono text-xs text-muted-foreground border border-border">
                  {run.status}
                </span>
              ) : null}
            </div>
            <DialogClose asChild>
              <Button variant="ghost" size="icon" className="h-7 w-7 text-muted-foreground hover:text-foreground">
                <X className="h-4 w-4" />
                <span className="sr-only">Close</span>
              </Button>
            </DialogClose>
          </div>
          {orderedLogs.length ? <div className="grid min-w-0 gap-1">
            <div className="flex min-w-0 items-end gap-2">
              <label className="grid min-w-0 flex-1 gap-1 text-xs text-muted-foreground">
                Filter loaded output
                <Input ref={filterRef} type="search" className="h-8" value={filter} onChange={(event) => setFilter(event.target.value)} />
              </label>
              {filter ? <Button variant="outline" size="sm" className="h-8" aria-label="Clear output filter" onClick={() => { setFilter(""); filterRef.current?.focus(); }}>Clear</Button> : null}
            </div>
            {filtering ? <p role="status" className="text-xs text-muted-foreground">{visibleLogs.length} of {orderedLogs.length} loaded entries match.</p> : null}
          </div> : null}
        </DialogHeader>
        <div className="min-w-0 max-h-[70dvh] min-h-0 overflow-auto bg-background p-4 font-mono text-[13px] leading-6 text-foreground">
          {run ? (
            <div className="mb-3 grid min-w-0 gap-1 break-all border-b border-border pb-3 text-xs text-muted-foreground">
              <span>$ nerocd runs inspect {run.id}</span>
              <span>project={run.project_id} template={run.template_id ?? "adhoc"}</span>
              {following ? <span aria-live="polite">Following active run output.</span> : null}
            </div>
          ) : null}
          {orderedLogs.length ? <div className="mb-3 grid gap-1">
            <Button variant="outline" size="sm" className="w-fit max-w-full" onClick={downloadLoadedOutput}>Download loaded output</Button>
            <p className="text-xs text-muted-foreground">Includes all {orderedLogs.length} loaded entries, regardless of the filter.</p>
          </div> : null}
          {hasPrevious ? (
            <div className="mb-3 grid gap-2">
              {previousError ? <p role="alert" className="text-destructive">Unable to load earlier output. Try again.</p> : null}
              <Button type="button" variant="outline" size="sm" disabled={loadingPrevious} onClick={onLoadPrevious}>
                {loadingPrevious ? "Loading earlier output..." : previousError ? "Retry earlier output" : "Load earlier output"}
              </Button>
            </div>
          ) : null}
          {error ? (
            <div className="mb-3 grid gap-3">
              <p className="min-w-0 break-words text-destructive" role="alert">Unable to load run output: {error.message}</p>
              {onRetry ? <Button variant="outline" disabled={retrying} onClick={onRetry}>Retry logs</Button> : null}
            </div>
          ) : null}
          {loading && orderedLogs.length === 0 ? (
            <div className="flex items-center gap-2 text-muted-foreground" role="status">
              <LoaderCircle className="h-4 w-4 animate-spin" />
              Loading run output...
            </div>
          ) : orderedLogs.length === 0 ? error ? null : (
            <div className="text-muted-foreground">
              {shouldPollRun(run) ? "waiting for runner output..." : "No run output was recorded."}
            </div>
          ) : visibleLogs.length === 0 ? (
            <p className="text-muted-foreground">No loaded output matches this filter.</p>
          ) : (
            <div className="grid gap-1">
              {visibleLogs.map((log) => (
                <div key={log.id} className="grid gap-3 md:grid-cols-[5rem_4.5rem_minmax(0,1fr)]">
                  <span className="text-muted-foreground">#{log.sequence}</span>
                  <span className={cn(
                    "uppercase",
                    log.stream === "stdout" ? "text-success" : log.stream === "stderr" ? "text-destructive" : "text-primary"
                  )}>
                    {log.stream}
                  </span>
                  <span className="min-w-0 whitespace-pre-wrap break-words">{log.message}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
