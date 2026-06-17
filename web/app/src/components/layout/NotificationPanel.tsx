import { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import type { ApiSnapshot } from "@/api";

type ViewKey = "home" | "runs" | "approvals" | "projects" | "templates" | "logs" | "audit" | "settings";

export function NotificationPanel({
  snapshot,
  onNavigate,
}: {
  snapshot: ApiSnapshot | null;
  onNavigate: (view: ViewKey) => void;
}): ReactNode {
  const pending = snapshot?.approvals.filter((approval) => approval.status === "pending") ?? [];
  const failed = snapshot?.runs.filter((run) => ["failed", "error"].includes(run.status)) ?? [];

  return (
    <div className="absolute right-0 top-11 z-50 w-80 rounded-xl border border-border bg-popover p-2 text-popover-foreground shadow-xl">
      <div className="px-2 py-1.5">
        <strong className="text-sm">Notifications</strong>
        <p className="text-xs text-muted-foreground">
          {pending.length} approvals · {failed.length} failed runs
        </p>
      </div>
      <div className="mt-1 grid gap-1">
        <button
          className="rounded-lg px-2 py-2 text-left hover:bg-muted"
          type="button"
          onClick={() => onNavigate("approvals")}
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
          onClick={() => onNavigate("runs")}
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
