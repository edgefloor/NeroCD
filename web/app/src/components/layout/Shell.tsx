import { type ReactNode, useEffect, useRef, useState } from "react";
import { Bell, Search, UserRound } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import type { ApiSnapshot } from "@/api";
import { navigationForRoles, navigationForView, navigationGroups, type ViewKey } from "@/router/metadata";
import { NavButton } from "./NavButton";
import { NotificationPanel } from "./NotificationPanel";

export function Shell({ snapshot, view, setView, notice, query, setQuery, theme, toggleTheme, onRefresh, onSignOut, onOpenSearch, roles = [], filterable = true, children }: {
  snapshot: ApiSnapshot | null;
  view: ViewKey;
  setView: (view: ViewKey) => void;
  notice: string;
  query: string;
  setQuery: (query: string) => void;
  theme: "light" | "dark";
  toggleTheme: () => void;
  onRefresh: () => void;
  onSignOut: () => void;
  onOpenSearch?: () => void;
  roles?: readonly string[];
  filterable?: boolean;
  children: ReactNode;
}): ReactNode {
  const current = navigationForView(view);
  const items = navigationForRoles(roles);
  const [notificationsOpen, setNotificationsOpen] = useState(false);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const [accountOpen, setAccountOpen] = useState(false);
  const filterRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const breakpoint = window.matchMedia("(min-width: 1024px)");
    const closeOnDesktop = () => { if (breakpoint.matches) setMobileMenuOpen(false); };
    breakpoint.addEventListener("change", closeOnDesktop);
    return () => breakpoint.removeEventListener("change", closeOnDesktop);
  }, []);

  useEffect(() => {
    const focusFilter = (event: KeyboardEvent) => {
      const target = event.target;
      if (event.key !== "/" || event.metaKey || event.ctrlKey || event.altKey || !(target instanceof HTMLElement)) return;
      if (target.closest("input, textarea, select, [contenteditable='true'], [role='dialog']")) return;
      if (current.filterable && filterable && !mobileMenuOpen && !accountOpen && !notificationsOpen) {
        event.preventDefault();
        filterRef.current?.focus();
      }
    };
    window.addEventListener("keydown", focusFilter);
    return () => window.removeEventListener("keydown", focusFilter);
  }, [current.filterable, filterable, mobileMenuOpen, accountOpen, notificationsOpen]);

  const navigation = (label: string) => (
    <nav className="min-h-0 flex-1 space-y-4 overflow-y-auto px-3 py-4" aria-label={label}>
      {navigationGroups.map((group) => {
        const grouped = items.filter((item) => item.group === group);
        if (!grouped.length) return null;
        return <div key={group}><p className="mb-1 px-3 text-xs font-medium text-muted-foreground">{group}</p>
          <div className="space-y-0.5">{grouped.map((item) => <NavButton key={item.key} item={item} active={view === item.key} onClick={() => { setView(item.key); setMobileMenuOpen(false); }} />)}</div>
        </div>;
      })}
    </nav>
  );

  return (
    <main className="mobile-app-shell bg-background text-foreground">
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-60 flex-col border-r border-sidebar-border bg-sidebar lg:flex">
        <div className="flex h-14 shrink-0 items-center gap-2.5 border-b border-sidebar-border px-4">
          <img src="/logo.png" alt="" className="h-7 w-7 object-contain" /><strong className="text-base font-semibold tracking-tight">NeroCD</strong>
        </div>
        {navigation("Desktop navigation")}
        <div className="flex shrink-0 items-center justify-between border-t border-sidebar-border px-6 py-3 text-xs text-sidebar-muted"><span>System</span><span>{snapshot?.health.status ?? "unknown"}</span></div>
      </aside>

      <section className="min-w-0 lg:pl-60">
        <header className="mobile-shell-header sticky top-0 z-20 flex h-14 min-w-0 items-center justify-between gap-2 border-b border-border bg-background/95 px-4 backdrop-blur-sm lg:px-6">
          <div className="flex min-w-0 items-center gap-2">
            <Dialog open={mobileMenuOpen} onOpenChange={setMobileMenuOpen}>
              <DialogTrigger asChild><Button variant="ghost" size="sm" className="lg:hidden" aria-label="Open mobile navigation">Menu</Button></DialogTrigger>
              <DialogContent aria-modal="true" className="mobile-navigation-drawer inset-y-0 left-0 flex h-dvh w-72 max-w-[calc(100vw-2rem)] translate-x-0 translate-y-0 flex-col gap-0 rounded-none p-0 sm:max-w-72">
                <DialogHeader className="shrink-0 border-b border-border px-6 py-4 pr-12"><DialogTitle>Mobile navigation</DialogTitle><DialogDescription className="sr-only">Choose a page.</DialogDescription></DialogHeader>
                {navigation("Mobile navigation")}
              </DialogContent>
            </Dialog>
            <h1 className="truncate text-base font-semibold tracking-tight">{current.label}</h1>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <Button variant="ghost" size="icon" aria-label="Search navigation" onClick={onOpenSearch}><Search className="h-4 w-4" /></Button>
            <Dialog open={notificationsOpen} onOpenChange={setNotificationsOpen}>
              <DialogTrigger asChild><Button variant="ghost" size="icon" aria-label="Notifications"><Bell className="h-4 w-4" /></Button></DialogTrigger>
              <DialogContent aria-modal="true"><DialogHeader><DialogTitle>Notifications</DialogTitle><DialogDescription>Activity in the loaded runs and approvals.</DialogDescription></DialogHeader>
                <NotificationPanel snapshot={snapshot} onNavigate={(next) => { setView(next); setNotificationsOpen(false); }} />
              </DialogContent>
            </Dialog>
            <Dialog open={accountOpen} onOpenChange={setAccountOpen}>
              <DialogTrigger asChild><Button variant="ghost" size="icon" aria-label="Account"><UserRound className="h-4 w-4" /></Button></DialogTrigger>
              <DialogContent aria-modal="true"><DialogHeader><DialogTitle>Account</DialogTitle><DialogDescription className="break-all">{notice || "Current session"}</DialogDescription></DialogHeader>
                <div className="grid gap-2">
                  <Button variant="outline" onClick={() => { onRefresh(); setAccountOpen(false); }}>Refresh data</Button>
                  <Button variant="outline" onClick={toggleTheme}>Switch to {theme === "dark" ? "light" : "dark"} mode</Button>
                  <Button variant="outline" onClick={() => { setAccountOpen(false); onSignOut(); }}>Sign out</Button>
                </div>
              </DialogContent>
            </Dialog>
          </div>
        </header>
        <div className="mobile-shell-content mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
          {current.filterable && filterable && <div className="mb-5 flex min-w-0 items-center gap-2">
            <label className="flex min-w-0 flex-1 items-center gap-3 text-sm text-muted-foreground" htmlFor="page-filter"><span className="shrink-0">Filter this page</span>
              <input id="page-filter" ref={filterRef} type="search" className="h-9 min-w-0 w-full max-w-sm rounded-lg border border-border bg-card px-3 text-foreground" placeholder={`Filter ${current.label.toLowerCase()}`} value={query} onChange={(event) => setQuery(event.target.value)} />
            </label>
            {query && <Button variant="ghost" size="sm" onClick={() => setQuery("")}>Clear filter</Button>}
          </div>}
          {children}
        </div>
      </section>
    </main>
  );
}
