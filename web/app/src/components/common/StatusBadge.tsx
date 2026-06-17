import { ReactNode } from "react";
import { CheckCircle2, CircleDot, AlertTriangle, Box } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export function statusTone(status: string): "good" | "pending" | "danger" | "neutral" {
  if (["ok", "active", "succeeded", "scaffolded"].includes(status)) {
    return "good";
  }
  if (["waiting_approval", "planned", "queued", "running", "pending"].includes(status)) {
    return "pending";
  }
  if (["failed", "error", "canceled", "revoked", "archived"].includes(status)) {
    return "danger";
  }
  return "neutral";
}

export function statusLabel(status: string): string {
  const labels: Record<string, string> = {
    waiting_approval: "Waiting",
    succeeded: "Succeeded",
    queued: "Queued",
    running: "Running",
    failed: "Failed",
    error: "Error",
    pending: "Pending",
    approved: "Approved",
    planned: "Planned",
    scaffolded: "Scaffolded",
    ok: "Ok",
  };
  return labels[status] ?? status.replaceAll("_", " ");
}

export function StatusBadge({ status, className }: { status: string; className?: string }): ReactNode {
  const tone = statusTone(status);
  const variant = tone === "good" ? "success" : tone === "pending" ? "warning" : tone === "danger" ? "destructive" : "secondary";
  const Icon = tone === "good" ? CheckCircle2 : tone === "pending" ? CircleDot : tone === "danger" ? AlertTriangle : Box;
  
  return (
    <Badge 
      variant={variant} 
      className={cn(
        "justify-start gap-1.5 whitespace-nowrap capitalize text-xs font-medium",
        className
      )}
    >
      <Icon className="h-3 w-3 shrink-0" />
      {statusLabel(status)}
    </Badge>
  );
}
