export type ViewKey = "home" | "runs" | "deployments" | "runners" | "operations" | "approvals" | "projects" | "templates" | "logs" | "audit" | "settings";
export type NavigationGroup = "Hosting" | "Advanced" | "Observe" | "Account";
export type NavigationPath = "/" | `/${Exclude<ViewKey, "home">}`;
export type NavigationItem = { key: ViewKey; to: NavigationPath; label: string; group: NavigationGroup; adminOnly?: boolean; filterable?: boolean };

export const navigationGroups: NavigationGroup[] = ["Hosting", "Advanced", "Observe", "Account"];
export const navigationItems: NavigationItem[] = [
  { key: "home", to: "/", label: "Home", group: "Hosting", filterable: true },
  { key: "deployments", to: "/deployments", label: "Deployments", group: "Advanced", filterable: true },
  { key: "runs", to: "/runs", label: "Runs", group: "Advanced", filterable: true },
  { key: "approvals", to: "/approvals", label: "Approvals", group: "Advanced", filterable: true },
  { key: "projects", to: "/projects", label: "Projects", group: "Hosting", filterable: true },
  { key: "templates", to: "/templates", label: "Templates", group: "Advanced", filterable: true },
  { key: "runners", to: "/runners", label: "Servers and runners", group: "Hosting", filterable: true },
  { key: "logs", to: "/logs", label: "Logs", group: "Observe", filterable: true },
  { key: "audit", to: "/audit", label: "Audit", group: "Observe", filterable: true },
  { key: "operations", to: "/operations", label: "Operations", group: "Observe", adminOnly: true },
  { key: "settings", to: "/settings", label: "Settings", group: "Account" },
];

export function navigationForRoles(roles: readonly string[]): NavigationItem[] {
  return navigationItems.filter((item) => !item.adminOnly || roles.includes("system_admin"));
}

export function navigationForView(view: ViewKey): NavigationItem {
  return navigationItems.find((item) => item.key === view)!;
}

export function viewForPath(pathname: string): ViewKey {
  return navigationItems.find((item) => item.to !== "/" && (pathname === item.to || pathname.startsWith(`${item.to}/`)))?.key ?? "home";
}
