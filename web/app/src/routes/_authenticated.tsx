import { useEffect, useRef, useState } from "react";
import { createFileRoute, Outlet, useLocation, useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Toaster } from "@/components/ui/sonner";
import { Shell } from "@/components/layout/Shell";
import { CommandPalette } from "@/components/CommandPalette";
import { useTheme } from "@/hooks/useApi";
import { approvalsQuery, healthQuery, principalQuery, runsQuery } from "@/api";
import { apiSnapshot } from "@/api/compat";
import { useAuthState } from "@/router/context";

type ViewKey = "home" | "runs" | "deployments" | "runners" | "operations" | "approvals" | "projects" | "templates" | "logs" | "audit" | "settings";
const routes: Record<ViewKey, string> = { home: "/", runs: "/runs", deployments: "/deployments", runners: "/runners", operations: "/operations", approvals: "/approvals", projects: "/projects", templates: "/templates", logs: "/logs", audit: "/audit", settings: "/settings" };

export const Route = createFileRoute("/_authenticated")({ component: AuthenticatedLayout });

function viewFor(pathname: string): ViewKey {
  if (pathname.startsWith("/runs")) return "runs";
  if (pathname.startsWith("/deployments")) return "deployments";
  if (pathname.startsWith("/runners")) return "runners";
  if (pathname.startsWith("/operations")) return "operations";
  if (pathname.startsWith("/approvals")) return "approvals";
  if (pathname.startsWith("/projects")) return "projects";
  if (pathname.startsWith("/templates")) return "templates";
  if (pathname.startsWith("/logs")) return "logs";
  if (pathname.startsWith("/audit")) return "audit";
  if (pathname.startsWith("/settings")) return "settings";
  return "home";
}

function AuthenticatedLayout() {
  const auth = useAuthState();
  const location = useLocation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const redirected = useRef(false);
  const { theme, toggleTheme } = useTheme();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const query = (location.search as { q?: string }).q ?? "";
  const principal = useQuery(principalQuery());
  const health = useQuery(healthQuery());
  const runs = useQuery(runsQuery());
  const approvals = useQuery(approvalsQuery());
  const view = viewFor(location.pathname);
  const snapshot = apiSnapshot({ health: health.data, runs: runs.data, approvals: approvals.data });
  useEffect(() => { if (auth.authenticated === false && location.pathname !== "/sign-in" && !redirected.current) { redirected.current = true; void navigate({ to: "/sign-in", search: { redirect: `${location.pathname}${location.searchStr}` }, replace: true }); } }, [auth.authenticated, location.pathname, location.searchStr, navigate]);
  if (auth.authenticated === null) return <main className="min-h-screen bg-background" aria-label="Checking session" />;
  if (auth.authenticated === false) return null;
  const setQuery = (next: string) => void navigate({ to: location.pathname as never, search: (next ? { q: next } : {}) as never });
  const setView = (next: ViewKey) => void navigate({ to: routes[next] as never, search: {} as never });
  const refresh = () => void client.invalidateQueries();
  return <><Toaster position="top-right" richColors theme={theme} /><CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} snapshot={snapshot} view={view} setView={setView} theme={theme} toggleTheme={toggleTheme} onRefresh={refresh} onSignOut={() => void auth.signOut()} onSearch={(next) => { setQuery(next); setPaletteOpen(false); }} /><Shell snapshot={snapshot} view={view} setView={setView} notice={principal.data?.email ?? ""} query={query} setQuery={setQuery} theme={theme} toggleTheme={toggleTheme} onRefresh={refresh} onSignOut={() => void auth.signOut()} onOpenSearch={() => setPaletteOpen(true)}><Outlet /></Shell></>;
}
