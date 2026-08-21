import { ReactNode, useMemo, useRef } from "react";
import { X } from "lucide-react";
import type { RunLog, TaskRun } from "@/api";
import { Dialog, DialogClose, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

let pendingOpener: HTMLElement | null = null;

export function rememberTerminalLogOpener(opener: HTMLElement): void {
  pendingOpener = opener;
}

export function TerminalLogDialog({
  run,
  logs,
  open,
  onOpenChange,
}: {
  run?: TaskRun;
  logs: RunLog[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
}): ReactNode {
  const orderedLogs = useMemo(() => [...logs].sort((a, b) => a.sequence - b.sequence), [logs]);
  const openerRef = useRef<HTMLElement | null>(null);
  if (open && openerRef.current === null && pendingOpener?.isConnected) {
    openerRef.current = pendingOpener;
    pendingOpener = null;
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="max-h-[calc(100dvh-2rem)] gap-0 overflow-hidden rounded-lg p-0 sm:max-w-4xl border-border bg-background"
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          requestAnimationFrame(() => {
            if (openerRef.current?.isConnected) {
              openerRef.current.focus();
              return;
            }
            document.querySelector<HTMLButtonElement>(`[data-run-log-trigger="${run?.id}"]`)?.focus();
          });
        }}
      >
        <DialogHeader className="border-b border-border bg-muted px-4 py-3">
          <div className="flex min-w-0 items-center justify-between gap-3">
            <div className="flex min-w-0 items-center gap-3">
              <DialogTitle className="min-w-0 truncate font-mono text-sm text-foreground">
                {run ? `run ${run.id}` : "run terminal"}
              </DialogTitle>
              {run ? (
                <span className="rounded bg-muted px-2 py-0.5 font-mono text-xs text-muted-foreground border border-border">
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
        </DialogHeader>
        <div className="max-h-[70dvh] min-h-[360px] overflow-auto bg-background p-4 font-mono text-[13px] leading-6 text-foreground">
          {run ? (
            <div className="mb-3 grid gap-1 border-b border-border pb-3 text-xs text-muted-foreground">
              <span>$ nerocd runs inspect {run.id}</span>
              <span>project={run.project_id} template={run.template_id ?? "adhoc"}</span>
            </div>
          ) : null}
          {orderedLogs.length === 0 ? (
            <div className="text-muted-foreground">
              <span className="text-success">system</span> waiting for runner output...
            </div>
          ) : (
            <div className="grid gap-1">
              {orderedLogs.map((log) => (
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
