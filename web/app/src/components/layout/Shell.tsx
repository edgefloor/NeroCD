import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { Bell, Menu, PanelLeftClose, PanelLeftOpen, Search, UserRound, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
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
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const collapseRef = useRef<HTMLButtonElement>(null);
  const expandRef = useRef<HTMLButtonElement>(null);
  const filterRef = useRef<HTMLInputElement>(null);
  const dialogID = useId();
  const accountDialogID = `${dialogID}-account`;
  const notificationsDialogID = `${dialogID}-notifications`;
  const sidebarID = `${dialogID}-sidebar`;
  const accountTriggerRef = useRef<HTMLButtonElement>(null);
  const notificationsTriggerRef = useRef<HTMLButtonElement>(null);
  const contentRef = useRef<HTMLElement>(null);

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

  const restoreDialogFocus = (event: Event, trigger: HTMLButtonElement | null, id: string) => {
    const visible = trigger?.getClientRects().length ? trigger
      : Array.from(document.querySelectorAll<HTMLButtonElement>(`button[aria-controls="${id}"]`)).find((button) => button.getClientRects().length > 0);
    if (visible) { event.preventDefault(); visible.focus(); }
  };

  const navigation = (label: string) => (
    <nav className="min-h-0 flex-1 space-y-4 overflow-y-auto px-2 py-3" aria-label={label}>
      {navigationGroups.map((group) => {
        const grouped = items.filter((item) => item.group === group);
        if (!grouped.length) return null;
        return <div key={group}><p className="mb-1 px-2 text-xs font-normal text-muted-foreground">{group}</p>
          <div className="space-y-0.5">{grouped.map((item) => <NavButton key={item.key} item={item} active={view === item.key} onClick={() => { setView(item.key); setMobileMenuOpen(false); }} />)}</div>
        </div>;
      })}
    </nav>
  );

  return (
    <div className="mobile-app-shell bg-sidebar text-foreground">
      <a href="#page-content" className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-2 focus:z-50 focus:rounded-lg focus:bg-primary focus:px-4 focus:py-2 focus:text-primary-foreground" onClick={(event) => {
        event.preventDefault();
        contentRef.current?.focus();
        contentRef.current?.scrollIntoView({ block: "start" });
      }}>Skip to page content</a>
      <aside id={sidebarID} className={`fixed inset-y-0 left-0 z-30 hidden w-[264px] flex-col bg-sidebar ${sidebarOpen ? "lg:flex" : ""}`}>
        <div className="flex h-[42px] shrink-0 items-center gap-2.5 px-4">
          <img src="/logo.png" alt="" className="h-7 w-7 object-contain" /><strong className="text-base font-semibold tracking-tight">NeroCD</strong>
          <Button ref={collapseRef} variant="ghost" size="icon-sm" className="ml-auto text-sidebar-muted" aria-label="Collapse sidebar" aria-expanded={sidebarOpen} aria-controls={sidebarID} onClick={() => { setSidebarOpen(false); requestAnimationFrame(() => expandRef.current?.focus()); }}><PanelLeftClose aria-hidden="true" className="size-4" /></Button>
        </div>
        <div className="px-2 pt-3"><Button variant="ghost" className="w-full justify-start gap-2 px-2 font-normal text-sidebar-muted" onClick={onOpenSearch} aria-label="Search navigation"><Search aria-hidden="true" className="h-4 w-4" />Search</Button></div>
        {navigation("Desktop navigation")}
        <div className="shrink-0 border-t border-sidebar-border px-2 py-2">
          <div className="flex items-center gap-1">
            <Button variant="ghost" className="min-w-0 flex-1 justify-start gap-2 px-2 font-normal text-sidebar-muted" aria-haspopup="dialog" aria-expanded={accountOpen} aria-controls={accountDialogID} onClick={(event) => { accountTriggerRef.current = event.currentTarget; setAccountOpen(true); }}><UserRound aria-hidden="true" className="h-4 w-4" />Account</Button>
            <Button variant="ghost" size="icon-sm" className="text-sidebar-muted" aria-label="Notifications" aria-haspopup="dialog" aria-expanded={notificationsOpen} aria-controls={notificationsDialogID} onClick={(event) => { notificationsTriggerRef.current = event.currentTarget; setNotificationsOpen(true); }}><Bell aria-hidden="true" className="h-4 w-4" /></Button>
          </div>
        </div>
      </aside>

      <section className={`shell-workspace min-w-0 ${sidebarOpen ? "lg:ml-[264px]" : ""}`}>
        <header className="mobile-shell-header sticky top-0 z-20 flex h-14 min-w-0 lg:h-[42px] items-center justify-between gap-2 border-b border-border bg-background/95 px-4 lg:px-5">
          <div className="flex min-w-0 items-center gap-2">
            {!sidebarOpen && <Button ref={expandRef} variant="ghost" size="icon-sm" className="hidden lg:inline-flex" aria-label="Expand sidebar" aria-expanded={sidebarOpen} aria-controls={sidebarID} onClick={() => { setSidebarOpen(true); requestAnimationFrame(() => collapseRef.current?.focus()); }}><PanelLeftOpen aria-hidden="true" className="size-4" /></Button>}
            <Dialog open={mobileMenuOpen} onOpenChange={setMobileMenuOpen}>
              <DialogTrigger asChild><Button variant="ghost" size="icon" className="size-11 lg:hidden" aria-label="Open mobile navigation"><Menu aria-hidden="true" className="size-5" /></Button></DialogTrigger>
              <DialogContent showCloseButton={false} aria-modal="true" className="mobile-navigation-drawer inset-y-0 left-0 flex h-dvh w-72 max-w-[calc(100vw-2rem)] translate-x-0 translate-y-0 flex-col gap-0 rounded-none p-0 sm:max-w-72">
                <DialogHeader className="sr-only"><DialogTitle>Mobile navigation</DialogTitle><DialogDescription className="sr-only">Choose a page.</DialogDescription></DialogHeader>
                <div className="flex h-14 shrink-0 items-center justify-between gap-2 px-3">
                  <span className="flex min-w-0 items-center gap-2"><img src="/logo.png" alt="" className="size-6" /><span className="text-sm font-semibold">NeroCD</span></span>
                  <DialogClose asChild><Button variant="ghost" size="icon" className="size-11 shrink-0" aria-label="Close navigation"><X aria-hidden="true" className="size-5" /></Button></DialogClose>
                </div>
                {navigation("Mobile navigation")}
              </DialogContent>
            </Dialog>
            <h1 className="truncate text-sm font-normal">{current.label}</h1>
          </div>
          <div className={`flex shrink-0 items-center gap-1 ${sidebarOpen ? "lg:hidden" : ""}`}>
            <Button variant="ghost" size="icon" aria-label="Search navigation" onClick={onOpenSearch}><Search className="h-4 w-4" /></Button>
            <Dialog open={notificationsOpen} onOpenChange={setNotificationsOpen}>
              <DialogTrigger asChild><Button variant="ghost" size="icon" aria-label="Notifications" aria-controls={notificationsDialogID} onClick={(event) => { notificationsTriggerRef.current = event.currentTarget; }}><Bell className="h-4 w-4" /></Button></DialogTrigger>
              <DialogContent id={notificationsDialogID} aria-modal="true" onCloseAutoFocus={(event) => restoreDialogFocus(event, notificationsTriggerRef.current, notificationsDialogID)} className="flex max-h-[calc(100dvh-2rem)] flex-col overflow-hidden"><DialogHeader className="shrink-0 pr-6"><DialogTitle>Notifications</DialogTitle><DialogDescription>Activity in the loaded runs and approvals.</DialogDescription></DialogHeader>
                <div className="min-h-0 overflow-y-auto"><NotificationPanel snapshot={snapshot} onNavigate={(next) => { setView(next); setNotificationsOpen(false); }} /></div>
              </DialogContent>
            </Dialog>
            <Dialog open={accountOpen} onOpenChange={setAccountOpen}>
              <DialogTrigger asChild><Button variant="ghost" size="icon" aria-label="Account" aria-controls={accountDialogID} onClick={(event) => { accountTriggerRef.current = event.currentTarget; }}><UserRound className="h-4 w-4" /></Button></DialogTrigger>
              <DialogContent id={accountDialogID} aria-modal="true" onCloseAutoFocus={(event) => restoreDialogFocus(event, accountTriggerRef.current, accountDialogID)} className="flex max-h-[calc(100dvh-2rem)] flex-col overflow-hidden"><DialogHeader className="shrink-0 pr-6"><DialogTitle>Account</DialogTitle><DialogDescription className="break-all">{notice || "Current session"}</DialogDescription></DialogHeader>
                <div className="grid min-h-0 gap-2 overflow-y-auto">
                  <Button variant="outline" onClick={() => { onRefresh(); setAccountOpen(false); }}>Refresh data</Button>
                  <Button variant="outline" onClick={toggleTheme}>Switch to {theme === "dark" ? "light" : "dark"} mode</Button>
                  <Button variant="outline" onClick={() => { setAccountOpen(false); onSignOut(); }}>Sign out</Button>
                </div>
              </DialogContent>
            </Dialog>
          </div>
        </header>
        <main id="page-content" ref={contentRef} tabIndex={-1} aria-label={current.label} className="mobile-shell-content scroll-mt-14 px-4 py-5 outline-none lg:scroll-mt-[42px] lg:px-5">
          {current.filterable && filterable && <div className="mb-4 flex min-w-0 items-end gap-2 sm:items-center">
            <label className="flex min-w-0 flex-1 flex-col items-start gap-2 text-sm text-muted-foreground sm:flex-row sm:items-center sm:gap-3" htmlFor="page-filter"><span className="shrink-0">Filter this page</span>
              <input id="page-filter" ref={filterRef} type="search" className="h-9 min-w-0 w-full max-w-sm rounded-md border border-border bg-card px-3 text-foreground" placeholder={`Filter ${current.label.toLowerCase()}`} value={query} onChange={(event) => setQuery(event.target.value)} />
            </label>
            {query && <Button variant="ghost" size="sm" className="shrink-0" onClick={() => { setQuery(""); filterRef.current?.focus(); }}>Clear filter</Button>}
          </div>}
          {children}
        </main>
      </section>
    </div>
  );
}
