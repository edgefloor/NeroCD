import type { ReactNode } from "react";
import { Activity, ClipboardList, Folder, Home, LayoutTemplate, Rocket, ScrollText, Server, Settings, ShieldCheck, Workflow, type LucideIcon } from "lucide-react";
import type { NavigationItem, ViewKey } from "@/router/metadata";
import { cn } from "@/lib/utils";

const navigationIcons: Record<ViewKey, LucideIcon> = { home: Home, projects: Folder, runners: Server, deployments: Rocket, runs: Workflow, approvals: ShieldCheck, templates: LayoutTemplate, logs: ScrollText, audit: ClipboardList, operations: Activity, settings: Settings };

export function NavButton({ item, active, onClick, iconOnly = false }: {
  item: NavigationItem;
  active: boolean;
  onClick: () => void;
  iconOnly?: boolean;
}): ReactNode {
  const Icon = navigationIcons[item.key];
  return (
    <a href={item.to} aria-label={iconOnly ? item.label : undefined} title={iconOnly ? item.label : undefined} aria-current={active ? "page" : undefined}
      className={cn("flex min-h-10 w-full items-center gap-2.5 rounded px-2 py-2 text-sm font-normal lg:min-h-8 lg:py-1.5 transition-colors hover:bg-sidebar-panel overflow-hidden",
        active ? "bg-sidebar-panel text-sidebar-foreground" : "text-sidebar-muted")}
      onClick={(event) => {
        if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
        event.preventDefault();
        onClick();
      }}>
      <Icon aria-hidden="true" className="h-4 w-4 shrink-0" />
      <span className="sidebar-label shrink-0 whitespace-nowrap" aria-hidden={iconOnly}>{item.label}</span>
    </a>
  );
}
