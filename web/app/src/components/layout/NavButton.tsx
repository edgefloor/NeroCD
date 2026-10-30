import type { ReactNode } from "react";
import { Activity, ClipboardList, Folder, Home, LayoutTemplate, Rocket, ScrollText, Server, Settings, ShieldCheck, Workflow, type LucideIcon } from "lucide-react";
import type { NavigationItem, ViewKey } from "@/router/metadata";
import { cn } from "@/lib/utils";

const navigationIcons: Record<ViewKey, LucideIcon> = { home: Home, projects: Folder, runners: Server, deployments: Rocket, runs: Workflow, approvals: ShieldCheck, templates: LayoutTemplate, logs: ScrollText, audit: ClipboardList, operations: Activity, settings: Settings };

export function NavButton({ item, active, onClick }: {
  item: NavigationItem;
  active: boolean;
  onClick: () => void;
}): ReactNode {
  const Icon = navigationIcons[item.key];
  return (
    <a href={item.to} aria-current={active ? "page" : undefined}
      className={cn("flex min-h-10 w-full items-center gap-2.5 rounded-md border-l-2 border-transparent px-3 py-2 text-sm font-normal lg:min-h-9 transition-colors hover:bg-sidebar-panel",
        active ? "border-sidebar-primary bg-sidebar-panel text-sidebar-primary" : "text-sidebar-muted")}
      onClick={(event) => {
        if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
        event.preventDefault();
        onClick();
      }}>
      <Icon aria-hidden="true" className="h-4 w-4 shrink-0" />
      {item.label}
    </a>
  );
}
