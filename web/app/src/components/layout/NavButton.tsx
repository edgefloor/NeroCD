import { ReactNode } from "react";
import { Link, useLocation } from "@tanstack/react-router";
import { cn } from "@/lib/utils";
import type { NavigationItem } from "@/router/metadata";

export function NavButton({
  item,
  pending,
  onNavigate,
}: {
  item: Pick<NavigationItem, "to" | "label" | "icon">;
  pending: number;
  onNavigate?: () => void;
}): ReactNode {
  const Icon = item.icon;
  const location = useLocation();
  const active = location.pathname === item.to || ((item.to === "/runs" || item.to === "/deployments" || item.to === "/runners" || item.to === "/operations") && location.pathname.startsWith(`${item.to}/`));
  return (
    <Link
      to={item.to}
      search={(previous) => previous}
      aria-current={active ? "page" : undefined}
      onClick={onNavigate}
      className={cn(
        "relative flex w-full items-center gap-2.5 rounded-md px-2.5 py-1.5 text-[13px] font-medium transition-colors",
        "hover:bg-sidebar-panel",
        active && "bg-sidebar-panel text-foreground",
        !active && "text-sidebar-muted"
      )}
    >
      {active && (
        <span className="absolute left-0 top-1/2 -translate-y-1/2 h-5 w-[2px] rounded-full bg-primary" />
      )}
      <Icon className={cn("h-4 w-4", active ? "text-foreground" : "text-sidebar-muted")} />
      <span className="flex-1 text-left">{item.label}</span>
      {item.to === "/approvals" && pending > 0 ? (
        <span className="rounded-full bg-warning px-1.5 py-0.5 text-[10px] font-semibold text-warning-foreground">
          {pending}
        </span>
      ) : null}
    </Link>
  );
}
