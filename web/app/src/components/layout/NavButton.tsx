import { ReactNode } from "react";
import { type LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

type ViewKey = "home" | "runs" | "deployments" | "runners" | "operations" | "approvals" | "projects" | "templates" | "logs" | "audit" | "settings";

export function NavButton({
  item,
  active,
  pending,
  onClick,
}: {
  item: { key: ViewKey; label: string; icon: LucideIcon };
  active: boolean;
  pending: number;
  onClick: () => void;
}): ReactNode {
  const Icon = item.icon;
  return (
    <button
      className={cn(
        "flex w-full items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-[13px] font-medium transition-colors",
        "hover:bg-sidebar-panel",
        active && "bg-sidebar-panel text-foreground shadow-[inset_0_0_0_1px_rgba(0,0,0,0.04)] dark:shadow-[inset_0_0_0_1px_rgba(255,255,255,0.06)]",
        !active && "text-sidebar-muted"
      )}
      type="button"
      onClick={onClick}
    >
      <Icon className={cn("h-4 w-4 transition-colors", active ? "text-foreground" : "text-sidebar-muted")} />
      <span className="flex-1 text-left">{item.label}</span>
      {item.key === "approvals" && pending > 0 ? (
        <span className="rounded-full bg-warning px-1.5 py-0.5 text-[10px] font-semibold text-warning-foreground">
          {pending}
        </span>
      ) : null}
    </button>
  );
}
