import { useEffect, useRef, useState } from "react";
import { createFileRoute, Outlet, useLocation, useNavigate } from "@tanstack/react-router";
import { Toaster } from "@/components/ui/sonner";
import { Shell } from "@/components/layout/Shell";
import { CommandPalette } from "@/components/CommandPalette";
import { useTheme } from "@/hooks/useApi";
import { useAuthState } from "@/router/context";
import { useQuery } from "@tanstack/react-query";
import { principalQuery } from "@/api";
import { navigationItems } from "@/router/metadata";

export const Route = createFileRoute("/_authenticated")({ component: AuthenticatedLayout });
function AuthenticatedLayout() {
  const auth = useAuthState(); const location = useLocation(); const navigate = useNavigate();
  const redirected = useRef(false);
  const { theme, toggleTheme } = useTheme(); const [paletteOpen, setPaletteOpen] = useState(false);
  const query = (location.search as { q?: string }).q ?? "";
  const principal = useQuery(principalQuery());
  const navigation = navigationItems.filter((item) => !item.adminOnly || (principal.data?.roles ?? []).includes("system_admin"));
  useEffect(() => { if (auth.authenticated === false && location.pathname !== "/sign-in" && !redirected.current) { redirected.current = true; void navigate({ to: "/sign-in", search: { redirect: `${location.pathname}${location.searchStr}` }, replace: true }); } }, [auth.authenticated, location.pathname, location.searchStr, navigate]);
  if (auth.authenticated === null) return <main className="min-h-screen bg-background" aria-label="Checking session" />;
  if (auth.authenticated === false) return null;
  const setQuery = (next: string) => void navigate({ to: location.pathname as never, search: (next ? { q: next } : {}) as never });
  return <><Toaster position="top-right" richColors theme={theme} /><CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} theme={theme} toggleTheme={toggleTheme} onRefresh={() => void navigate({ to: location.pathname as never, replace: true })} onSignOut={() => void auth.signOut()} onSearch={(next) => { setQuery(next); setPaletteOpen(false); }} navigation={navigation} /><Shell query={query} setQuery={setQuery} theme={theme} toggleTheme={toggleTheme} onSignOut={() => void auth.signOut()} onOpenSearch={() => setPaletteOpen(true)}><Outlet /></Shell></>;
}
