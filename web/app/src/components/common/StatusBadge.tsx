import { ReactNode } from "react";
import { CheckCircle2, CircleDot, AlertTriangle, Box } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

import { statusLabel, statusTone } from "./status";

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
      <Icon aria-hidden="true" className="h-3 w-3 shrink-0" />
      {statusLabel(status)}
    </Badge>
  );
}
