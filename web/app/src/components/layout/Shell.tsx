import { ReactNode, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useLocation, useNavigate } from "@tanstack/react-router";
import {
  Bell,
  LogOut,
  Menu,
  Moon,
  RefreshCw,
  Search,
  Sun,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { approvalsQuery, healthQuery, principalQuery, runsQuery } from "@/api";
import { NavButton } from "./NavButton";
import { NotificationPanel } from "./NotificationPanel";
import { cn } from "@/lib/utils";
import { navigationItems, titleForPath } from "@/router/metadata";



export function Shell({
  query,
  setQuery,
  theme,
  toggleTheme,
  onSignOut,
  onOpenSearch,
  children,
}: {
  query: string;
  setQuery: (query: string) => void;
  theme: "light" | "dark";
  toggleTheme: () => void;
  onSignOut: () => void;
  onOpenSearch?: () => void;
  children: ReactNode;
}): ReactNode {
  const health = useQuery(healthQuery());
  const principal = useQuery(principalQuery());
  const runs = useQuery({ ...runsQuery(), refetchInterval: (query) => query.state.data?.some((run) => !["succeeded", "failed", "canceled", "rejected"].includes(run.status)) ? 3000 : false });
  const approvals = useQuery(approvalsQuery());
  const queryClient = useQueryClient();
  const pending = approvals.data?.filter((approval) => approval.status === "pending").length ?? 0;
  const failed = runs.data?.filter((run) => ["failed", "error"].includes(run.status)).length ?? 0;
  const location = useLocation();
  const navigate = useNavigate();
  const title = titleForPath(location.pathname);
  const navItems = navigationItems.filter((item) => !item.adminOnly || (principal.data?.roles ?? []).includes("system_admin"));
  const [notificationsOpen, setNotificationsOpen] = useState(false);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const notifRef = useRef<HTMLDivElement>(null);
  const notificationTriggerRef = useRef<HTMLButtonElement>(null);
  const notificationInitialFocusRef = useRef<HTMLButtonElement>(null);
  const mobileMenuTriggerRef = useRef<HTMLButtonElement>(null);
  const mobileMenuCloseRef = useRef<HTMLButtonElement>(null);

  function closeNotifications(restoreFocus = true): void {
    setNotificationsOpen(false);
    if (restoreFocus) requestAnimationFrame(() => notificationTriggerRef.current?.focus());
  }

  function closeMobileMenu(restoreFocus = true): void {
    setMobileMenuOpen(false);
    if (restoreFocus) requestAnimationFrame(() => mobileMenuTriggerRef.current?.focus());
  }

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent): void {
      if (
        event.key === "/" &&
        event.target instanceof HTMLElement &&
        !["INPUT", "TEXTAREA", "SELECT"].includes(event.target.tagName)
      ) {
        event.preventDefault();
        searchRef.current?.focus();
      }
      if (event.key === "Escape") {
        if (notificationsOpen) closeNotifications();
        if (mobileMenuOpen) closeMobileMenu();
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [mobileMenuOpen, notificationsOpen]);

  useEffect(() => {
    if (notificationsOpen) requestAnimationFrame(() => notificationInitialFocusRef.current?.focus());
  }, [notificationsOpen]);

  useEffect(() => {
    if (mobileMenuOpen) requestAnimationFrame(() => mobileMenuCloseRef.current?.focus());
  }, [mobileMenuOpen]);

  // Close notification panel on click outside
  useEffect(() => {
    if (!notificationsOpen) return;
    function onPointerDown(event: PointerEvent): void {
      if (
        notifRef.current &&
        event.target instanceof Node &&
        !notifRef.current.contains(event.target)
      ) {
        closeNotifications();
      }
    }
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [notificationsOpen]);

  // Prevent body scroll when mobile menu is open
  useEffect(() => {
    if (mobileMenuOpen) {
      document.body.style.overflow = "hidden";
    } else {
      document.body.style.overflow = "";
    }
    return () => {
      document.body.style.overflow = "";
    };
  }, [mobileMenuOpen]);

  return (
    <main className="min-h-screen bg-background text-foreground">
      {/* Desktop Sidebar */}
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-60 border-r border-sidebar-border bg-sidebar lg:flex lg:flex-col">
        <div className="flex h-14 items-center gap-2 px-3 border-b border-sidebar-border">
          <img
            src="/logo.png"
            alt="NeroCD"
            className="h-8 w-8 object-contain -m-0.5"
          />
          <strong className="text-base font-semibold tracking-tight">NeroCD</strong>
        </div>
        <nav className="flex-1 py-3 px-2 space-y-0.5">
          {navItems.map((item) => (
            <NavButton key={item.to} item={item} pending={pending} />
          ))}
        </nav>
        <div className="border-t border-sidebar-border p-3">
          <div className="flex items-center justify-between px-2 py-1.5 text-xs text-sidebar-muted">
            <span className="text-[11px]">System</span>
            <div className="flex items-center gap-1.5">
              <span className={cn(
                "h-1.5 w-1.5 rounded-full",
                health.data?.status === "ok" ? "bg-success" : "bg-warning"
              )} />
              <span className="text-[10px] uppercase tracking-wide">{health.data?.status ?? "unknown"}</span>
            </div>
          </div>
        </div>
      </aside>

      {/* Mobile Sidebar Overlay */}
      {mobileMenuOpen && (
        <>
          <div
            data-slot="mobile-navigation-overlay"
            aria-hidden="true"
            className="fixed inset-0 z-40 bg-black/50 lg:hidden"
            onClick={() => closeMobileMenu()}
          />
          <aside
            id="mobile-navigation"
            role="dialog"
            aria-modal="true"
            aria-label="Mobile navigation"
            className="fixed inset-y-0 left-0 z-50 w-60 border-r border-sidebar-border bg-sidebar flex flex-col lg:hidden"
            onKeyDown={(event) => {
              if (event.key !== "Tab") return;
              const focusable = event.currentTarget.querySelectorAll<HTMLElement>('a[href], button:not([disabled])');
              if (focusable.length === 0) return;
              const first = focusable[0];
              const last = focusable[focusable.length - 1];
              if (!first || !last) return;
              if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
              if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
            }}
          >
            <div className="flex h-14 items-center justify-between px-3 border-b border-sidebar-border">
              <div className="flex items-center gap-2">
                <img
                  src="/logo.png"
                  alt="NeroCD"
                  className="h-8 w-8 object-contain -m-0.5"
                />
                <strong className="text-base font-semibold tracking-tight">NeroCD</strong>
              </div>
              <Button ref={mobileMenuCloseRef} variant="ghost" size="icon" className="h-8 w-8" aria-label="Close mobile navigation" onClick={() => closeMobileMenu()}>
                <X className="h-4 w-4" />
              </Button>
            </div>
            <nav className="flex-1 py-3 px-2 space-y-0.5">
              {navItems.map((item) => (
                <NavButton key={item.to} item={item} pending={pending} onNavigate={() => closeMobileMenu()} />
              ))}
            </nav>
          </aside>
        </>
      )}

      {/* Main Content Area */}
      <section className="min-w-0 lg:pl-60">
        {/* Header */}
        <header className="sticky top-0 z-20 flex h-14 items-center justify-between border-b border-border bg-background/95 px-4 backdrop-blur-sm lg:px-6">
          <div className="flex items-center gap-3 shrink-0">
            <Button ref={mobileMenuTriggerRef} variant="ghost" size="icon" className="h-8 w-8 lg:hidden" aria-label="Open mobile navigation" aria-expanded={mobileMenuOpen} aria-controls="mobile-navigation" onClick={() => setMobileMenuOpen(true)}>
              <Menu className="h-4 w-4" />
            </Button>
            <h1 className="text-lg font-semibold tracking-tight text-foreground whitespace-nowrap">{title}</h1>
          </div>
          <div className="flex items-center gap-1.5 shrink-0">
            <label className="hidden h-8 min-w-56 items-center gap-2 rounded-lg border border-border/80 bg-card px-2.5 text-sm text-muted-foreground shadow-sm md:flex">
              <Search className="h-3.5 w-3.5" />
              <input
                ref={searchRef}
                className="min-w-0 flex-1 bg-transparent text-foreground outline-none placeholder:text-muted-foreground/60 text-sm"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search"
                type="search"
              />
              <kbd className="rounded border border-border/70 bg-background px-1 text-[9px] font-mono text-muted-foreground">/</kbd>
            </label>
            <div className="relative" ref={notifRef}>
              <Button ref={notificationTriggerRef} variant="ghost" size="icon" className="h-8 w-8" aria-label="Notifications" aria-expanded={notificationsOpen} aria-controls="notification-panel" onClick={() => setNotificationsOpen((open) => !open)}>
                <Bell className="h-4 w-4" />
                {pending + failed > 0 ? (
                  <span className="absolute -right-0.5 -top-0.5 h-4 min-w-4 rounded-full bg-warning px-1 text-[9px] font-semibold text-warning-foreground flex items-center justify-center">
                    {pending + failed}
                  </span>
                ) : null}
              </Button>
              {notificationsOpen ? (
                <NotificationPanel approvals={approvals.data ?? []} runs={runs.data ?? []} panelRef={notifRef} initialFocusRef={notificationInitialFocusRef} onNavigate={(to) => { void navigate({ to, search: (previous) => previous }); closeNotifications(); }} />
              ) : null}
            </div>
            <Button variant="ghost" size="icon" className="h-8 w-8 lg:hidden" aria-label="Search" onClick={() => onOpenSearch?.()}>
              <Search className="h-4 w-4" />
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8 hidden sm:flex" onClick={() => void queryClient.invalidateQueries({ predicate: (query) => ["health", "runs", "approvals"].includes(String(query.queryKey[0])) })} aria-label="Refresh">
              <RefreshCw className="h-4 w-4" />
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8 hidden sm:flex" onClick={toggleTheme} aria-label={`Switch to ${theme === "dark" ? "light" : "dark"} mode`}>
              {theme === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8 hidden sm:flex" onClick={onSignOut} aria-label="Sign out">
              <LogOut className="h-4 w-4" />
            </Button>
          </div>
        </header>
        
        {/* Page Content */}
        <div className="mx-auto max-w-[1400px] px-4 py-6 pb-24 lg:px-8 lg:py-8 lg:pb-8">{children}</div>
      </section>

      {/* Mobile Bottom Navigation */}
      <nav className="fixed inset-x-0 bottom-0 z-40 border-t border-border bg-background px-2 pb-[calc(env(safe-area-inset-bottom)+0.25rem)] pt-1 lg:hidden">
        <div className="mx-auto grid max-w-md grid-cols-5 gap-1">
          {navItems
            .filter((item) => item.mobile)
            .map((item) => {
              const Icon = item.icon;
              const isActive = location.pathname === item.to || (item.to === "/runs" && location.pathname.startsWith("/runs/"));
              return (
                <button
                  key={item.to}
                  className={cn(
                    "relative flex h-14 min-w-0 flex-col items-center justify-center gap-0.5 rounded-lg text-[10px] font-medium text-muted-foreground transition-colors",
                    "hover:bg-muted/70",
                    isActive && "bg-muted text-foreground",
                  )}
                  type="button"
                  aria-current={isActive ? "page" : undefined}
                  onClick={() => void navigate({ to: item.to, search: (previous) => previous })}
                >
                  <Icon className={cn("h-4 w-4", isActive && "text-foreground")} />
                  <span className="mobile-nav-text block max-w-full truncate px-1">{item.mobileLabel ?? item.label}</span>
                  {item.to === "/approvals" && pending > 0 ? (
                    <b className="absolute right-1.5 top-1.5 h-3.5 min-w-3.5 rounded-full bg-warning px-1 text-[8px] font-bold text-warning-foreground flex items-center justify-center">
                      {pending}
                    </b>
                  ) : null}
                </button>
              );
            })}
        </div>
      </nav>
    </main>
  );
}
