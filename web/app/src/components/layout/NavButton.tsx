import type { ReactNode } from "react";
import type { NavigationItem } from "@/router/metadata";
import { cn } from "@/lib/utils";

export function NavButton({ item, active, onClick }: {
  item: NavigationItem;
  active: boolean;
  onClick: () => void;
}): ReactNode {
  return (
    <a href={item.to} aria-current={active ? "page" : undefined}
      className={cn("flex min-h-10 w-full items-center rounded-lg border-l-2 px-3 py-2 text-sm font-medium transition-colors hover:bg-sidebar-panel",
        active ? "border-foreground bg-sidebar-panel text-foreground" : "border-transparent text-sidebar-muted")}
      onClick={(event) => {
        if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
        event.preventDefault();
        onClick();
      }}>
      {item.label}
    </a>
  );
}
