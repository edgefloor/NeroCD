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
      "overflow-hidden transition-colors",
      tone === "success" && "border-success/15 bg-success/[0.02]",
      tone === "warning" && "border-warning/15 bg-warning/[0.02]"
    )}>
      <CardContent className="p-4">
        <div className="mb-3 flex items-center justify-between">
          <span className="text-xs font-medium text-muted-foreground">{label}</span>
          <span className={cn(
            "grid h-7 w-7 place-items-center rounded-lg transition-colors",
            tone === "success" && "bg-success/10 text-success",
            tone === "warning" && "bg-warning/10 text-warning",
            tone === "neutral" && "bg-muted text-muted-foreground"
          )}>
            <Icon className="h-3.5 w-3.5" />
          </span>
        </div>
        <strong className="block text-2xl font-semibold tracking-tight">{value}</strong>
        <small className="text-xs text-muted-foreground mt-0.5 block">{caption}</small>
      </CardContent>
    </Card>
  );
}
