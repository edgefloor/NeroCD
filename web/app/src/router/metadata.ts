import { Activity, FileText, FolderKanban, Home, Layers3, type LucideIcon, Rocket, Settings, ShieldCheck, Terminal, Bot, Gauge } from "lucide-react";

export type NavigationItem = { to: "/" | "/runs" | "/deployments" | "/runners" | "/operations" | "/approvals" | "/projects" | "/templates" | "/logs" | "/audit" | "/settings"; label: string; mobileLabel?: string; icon: LucideIcon; mobile?: boolean; adminOnly?: boolean };

export const navigationItems: NavigationItem[] = [
  { to: "/", label: "Home", icon: Home, mobile: true },
  { to: "/runs", label: "Runs", icon: Activity, mobile: true },
  { to: "/deployments", label: "Deployments", icon: Rocket, mobile: true },
  { to: "/runners", label: "Runners", icon: Bot, mobile: true },
  { to: "/operations", label: "Operations", icon: Gauge, adminOnly: true },
  { to: "/approvals", label: "Approvals", mobileLabel: "Inbox", icon: ShieldCheck, mobile: true },
  { to: "/templates", label: "Templates", icon: Layers3, mobile: true },
  { to: "/logs", label: "Logs", icon: Terminal, mobile: true },
  { to: "/projects", label: "Projects", icon: FolderKanban },
  { to: "/audit", label: "Audit", icon: FileText },
  { to: "/settings", label: "Settings", icon: Settings },
];

export function titleForPath(pathname: string): string {
  return navigationItems.find((item) => item.to === pathname || (item.to !== "/" && pathname.startsWith(`${item.to}/`)))?.label ?? "Runs";
}
