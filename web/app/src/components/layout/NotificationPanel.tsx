import { ReactNode, RefObject } from "react";
import { Badge } from "@/components/ui/badge";
import type { Approval, TaskRun } from "@/api";

export function NotificationPanel({
  approvals,
  runs,
  onNavigate,
  panelRef,
  initialFocusRef,
}: {
  approvals: Approval[];
  runs: TaskRun[];
  onNavigate: (to: "/approvals" | "/runs") => void;
  panelRef: RefObject<HTMLDivElement | null>;
  initialFocusRef: RefObject<HTMLButtonElement | null>;
}): ReactNode {
  const pending = approvals.filter((approval) => approval.status === "pending");
  const failed = runs.filter((run) => ["failed", "error"].includes(run.status));

  return (
    <div id="notification-panel" ref={panelRef} role="dialog" aria-label="Notifications" className="absolute right-0 top-11 z-50 w-80 rounded-xl border border-border bg-popover p-2 text-popover-foreground shadow-xl">
      <div className="px-2 py-1.5">
        <strong className="text-sm">Notifications</strong>
        <p className="text-xs text-muted-foreground">
          {pending.length} approvals · {failed.length} failed runs
        </p>
      </div>
      <div className="mt-1 grid gap-1">
        <button
          ref={initialFocusRef}
          className="rounded-lg px-2 py-2 text-left hover:bg-muted"
          type="button"
          onClick={() => onNavigate("/approvals")}
        >
          <div className="flex items-center justify-between gap-3">
            <span className="text-sm font-medium">Approvals waiting</span>
            <Badge variant={pending.length ? "warning" : "secondary"}>{pending.length}</Badge>
          </div>
          <p className="mt-1 text-xs text-muted-foreground">Review approve/reject decisions.</p>
        </button>
        <button
          className="rounded-lg px-2 py-2 text-left hover:bg-muted"
          type="button"
          onClick={() => onNavigate("/runs")}
        >
          <div className="flex items-center justify-between gap-3">
            <span className="text-sm font-medium">Runs failed</span>
            <Badge variant={failed.length ? "destructive" : "secondary"}>{failed.length}</Badge>
          </div>
          <p className="mt-1 text-xs text-muted-foreground">Jump to the execution queue.</p>
        </button>
      </div>
    </div>
  );
}
