import { useEffect, useRef, useState } from "react";
import { createFileRoute, Outlet, useLocation, useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Toaster } from "@/components/ui/sonner";
import { Shell } from "@/components/layout/Shell";
import { SessionCheck } from "@/components/common/SessionCheck";
import { CommandPalette } from "@/components/CommandPalette";
import { useTheme } from "@/hooks/useTheme";
import { approvalsQuery, healthQuery, principalQuery, runsQuery } from "@/api";
import { apiSnapshot } from "@/api/compat";
import { useAuthState } from "@/router/context";

import { navigationForView, viewForPath, type ViewKey } from "@/router/metadata";

export const Route = createFileRoute("/_authenticated")({ component: AuthenticatedLayout });

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
  const view = viewForPath(location.pathname);
  const filterable = Boolean(navigationForView(view).filterable && location.pathname.replace(/\/$/, "") === navigationForView(view).to.replace(/\/$/, ""));
  const snapshot = apiSnapshot({ health: health.data, runs: runs.data, approvals: approvals.data });
  useEffect(() => { if (auth.authenticated === false && location.pathname !== "/sign-in" && !redirected.current) { redirected.current = true; void navigate({ to: "/sign-in", search: { redirect: `${location.pathname}${location.searchStr}` }, replace: true }); } }, [auth.authenticated, location.pathname, location.searchStr, navigate]);
  if (auth.authenticated === null) return <SessionCheck failed={Boolean(auth.authError)} />;
  if (auth.authenticated === false) return null;
  const setQuery = (next: string) => void navigate({ to: location.pathname as never, search: (previous) => ({ ...previous, q: next || undefined }) as never, replace: true });
  const setView = (next: ViewKey) => void navigate({ to: navigationForView(next).to, search: {} as never });
  const refresh = () => void client.invalidateQueries();
  return <><Toaster position="top-right" richColors theme={theme} /><CommandPalette roles={principal.data?.roles ?? []} open={paletteOpen} onOpenChange={setPaletteOpen} snapshot={snapshot} view={view} setView={setView} theme={theme} toggleTheme={toggleTheme} onRefresh={refresh} onSignOut={() => void auth.signOut()} onSearch={filterable ? (next) => { setQuery(next); setPaletteOpen(false); } : undefined} /><Shell filterable={filterable} roles={principal.data?.roles ?? []} snapshot={snapshot} view={view} setView={setView} notice={principal.data?.email ?? ""} query={query} setQuery={setQuery} theme={theme} toggleTheme={toggleTheme} onRefresh={refresh} onSignOut={() => void auth.signOut()} onOpenSearch={() => setPaletteOpen(true)}><Outlet /></Shell></>;
}
