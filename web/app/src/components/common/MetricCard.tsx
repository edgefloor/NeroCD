import { ReactNode } from "react";
import { type LucideIcon } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";

export function MetricCard({
  label,
  value,
  caption,
  icon: Icon,
  tone = "neutral",
}: {
  label: string;
  value: string | number;
  caption: string;
  icon: LucideIcon;
  tone?: "neutral" | "success" | "warning";
}): ReactNode {
  return (
    <Card className={cn(
      "overflow-hidden",
      tone === "success" && "border-success/20",
      tone === "warning" && "border-warning/20"
    )}>
      <CardContent className="p-4">
        <div className="mb-2 flex items-center justify-between">
          <span className="text-[11px] font-medium uppercase tracking-[0.08em] text-muted-foreground">{label}</span>
          <span className={cn(
            "grid h-6 w-6 place-items-center rounded-md border",
            tone === "success" && "border-success/15 bg-success/8 text-success",
            tone === "warning" && "border-warning/15 bg-warning/8 text-warning",
            tone === "neutral" && "border-border/60 bg-transparent text-muted-foreground"
          )}>
            <Icon className="h-3 w-3" />
          </span>
        </div>
        <strong className="block text-2xl font-semibold tracking-tight">{value}</strong>
        <small className="text-xs text-muted-foreground">{caption}</small>
      </CardContent>
    </Card>
  );
}
