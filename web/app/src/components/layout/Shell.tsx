import { ReactNode, useEffect, useRef, useState } from "react";
import {
  Activity,
  Bell,
  FileText,
  FolderKanban,
  Home,
  Layers3,
  LogOut,
  Menu,
  Moon,
  RefreshCw,
  Search,
  Settings,
  ShieldCheck,
  Sun,
  Terminal,
  X,
  Rocket,
  Bot,
  Gauge,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import type { ApiSnapshot } from "@/api";
import { NavButton } from "./NavButton";
import { NotificationPanel } from "./NotificationPanel";
import { cn } from "@/lib/utils";

type ViewKey = "home" | "runs" | "deployments" | "runners" | "operations" | "approvals" | "projects" | "templates" | "logs" | "audit" | "settings";

const viewPaths: Record<ViewKey, string> = {
  home: "/", runs: "/runs", deployments: "/deployments", runners: "/runners", operations: "/operations", approvals: "/approvals", projects: "/projects", templates: "/templates", logs: "/logs", audit: "/audit", settings: "/settings",
};

const navItems: Array<{ key: ViewKey; label: string; mobileLabel?: string; icon: typeof Home; mobile?: boolean; configure?: boolean }> = [
  { key: "home", label: "Home", icon: Home, mobile: true },
  { key: "runs", label: "Runs", icon: Activity, mobile: true },
  { key: "deployments", label: "Deployments", icon: Rocket, mobile: true },
  { key: "runners", label: "Runners", icon: Bot, mobile: true },
  { key: "operations", label: "Operations", icon: Gauge, configure: true },
  { key: "approvals", label: "Approvals", mobileLabel: "Inbox", icon: ShieldCheck, mobile: true },
  { key: "templates", label: "Templates", icon: Layers3, mobile: true },
  { key: "logs", label: "Logs", icon: Terminal, mobile: true },
  { key: "projects", label: "Projects", icon: FolderKanban, configure: true },
  { key: "audit", label: "Audit", icon: FileText, configure: true },
  { key: "settings", label: "Settings", icon: Settings, configure: true },
];

export function Shell({
  snapshot,
  view,
  setView,
  notice,
  query,
  setQuery,
  theme,
  toggleTheme,
  onRefresh,
  onSignOut,
  onOpenSearch,
  children,
}: {
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
  children: ReactNode;
}): ReactNode {
  const pending = snapshot?.approvals.filter((approval) => approval.status === "pending").length ?? 0;
  const failed = snapshot?.runs.filter((run) => ["failed", "error"].includes(run.status)).length ?? 0;
  const title = navItems.find((item) => item.key === view)?.label ?? "Home";
  const [notificationsOpen, setNotificationsOpen] = useState(false);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const notifRef = useRef<HTMLDivElement>(null);

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
        setNotificationsOpen(false);
        setMobileMenuOpen(false);
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  // Close notification panel on click outside
  useEffect(() => {
    if (!notificationsOpen) return;
    function onPointerDown(event: PointerEvent): void {
      if (
        notifRef.current &&
        event.target instanceof Node &&
        !notifRef.current.contains(event.target)
      ) {
        setNotificationsOpen(false);
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
        <div className="flex h-14 items-center gap-2.5 px-4 border-b border-sidebar-border">
          <img
            src="/logo.png"
            alt="NeroCD"
            className="h-7 w-7 object-contain"
          />
          <strong className="text-base font-semibold tracking-tight">NeroCD</strong>
        </div>
        <nav className="flex-1 py-3 px-2.5 space-y-0.5">
          {navItems.map((item) => (
            <NavButton key={item.key} item={{ ...item, href: viewPaths[item.key] }} active={view === item.key} pending={pending} onClick={() => setView(item.key)} />
          ))}
        </nav>
        <div className="border-t border-sidebar-border p-3">
          <div className="flex items-center justify-between px-2.5 py-1.5 text-xs text-sidebar-muted">
            <span className="text-[11px] font-medium uppercase tracking-wide">System</span>
            <div className="flex items-center gap-1.5">
              <span className={cn(
                "h-1.5 w-1.5 rounded-full",
                snapshot?.health.status === "ok" ? "bg-success" : "bg-warning"
              )} />
              <span className="text-[10px] uppercase tracking-wide font-medium">{snapshot?.health.status ?? "unknown"}</span>
            </div>
          </div>
        </div>
      </aside>

      {/* Mobile Sidebar Overlay */}
      {mobileMenuOpen && (
        <>
          <div 
            className="fixed inset-0 z-40 bg-black/40 lg:hidden"
            onClick={() => setMobileMenuOpen(false)}
          />
          <aside className="fixed inset-y-0 left-0 z-50 w-60 border-r border-sidebar-border bg-sidebar flex flex-col lg:hidden" role="dialog" aria-label="Mobile navigation">
            <div className="flex h-14 items-center justify-between px-4 border-b border-sidebar-border">
              <div className="flex items-center gap-2.5">
                <img
                  src="/logo.png"
                  alt="NeroCD"
                  className="h-7 w-7 object-contain"
                />
                <strong className="text-base font-semibold tracking-tight">NeroCD</strong>
              </div>
              <Button variant="ghost" size="icon" className="h-8 w-8 rounded-full" onClick={() => setMobileMenuOpen(false)}>
                <X className="h-4 w-4" />
              </Button>
            </div>
            <nav className="flex-1 py-3 px-2.5 space-y-0.5">
              {navItems.map((item) => (
                <NavButton key={item.key} item={{ ...item, href: viewPaths[item.key] }} active={view === item.key} pending={pending} onClick={() => { setView(item.key); setMobileMenuOpen(false); }} />
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
            <Button variant="ghost" size="icon" className="h-8 w-8 rounded-full lg:hidden" aria-label="Open mobile navigation" onClick={() => setMobileMenuOpen(true)}>
              <Menu className="h-4 w-4" />
            </Button>
            <h1 className="text-base font-semibold tracking-tight text-foreground whitespace-nowrap">{title}</h1>
          </div>
          <div className="flex items-center gap-1 shrink-0">
            {notice ? (
              <span className="hidden max-w-xs truncate rounded-lg bg-secondary px-2.5 py-1 text-[11px] text-secondary-foreground md:inline">
                {notice}
              </span>
            ) : null}
            <label className="hidden h-8 min-w-56 items-center gap-2 rounded-xl border border-border bg-card px-3 text-sm text-muted-foreground md:flex">
              <Search className="h-3.5 w-3.5" />
              <input
                ref={searchRef}
                className="min-w-0 flex-1 bg-transparent text-foreground outline-none placeholder:text-muted-foreground/70 text-sm"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search"
                type="search"
              />
              <kbd className="rounded-md border border-border/80 bg-background px-1.5 text-[9px] font-mono text-muted-foreground">/</kbd>
            </label>
            <div className="relative" ref={notifRef}>
              <Button variant="ghost" size="icon" className="h-8 w-8 rounded-full relative" aria-label="Notifications" onClick={() => setNotificationsOpen((open) => !open)}>
                <Bell className="h-4 w-4" />
                {pending + failed > 0 ? (
                  <span className="absolute right-0.5 top-0.5 h-4 min-w-4 rounded-full bg-warning px-1 text-[9px] font-semibold text-warning-foreground flex items-center justify-center">
                    {pending + failed}
                  </span>
                ) : null}
              </Button>
              {notificationsOpen ? (
                <NotificationPanel snapshot={snapshot} onNavigate={(next) => { setView(next); setNotificationsOpen(false); }} />
              ) : null}
            </div>
            <Button variant="ghost" size="icon" className="h-8 w-8 rounded-full lg:hidden" aria-label="Search" onClick={() => onOpenSearch?.()}>
              <Search className="h-4 w-4" />
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8 rounded-full hidden sm:flex" onClick={onRefresh} aria-label="Refresh">
              <RefreshCw className="h-4 w-4" />
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8 rounded-full hidden sm:flex" onClick={toggleTheme} aria-label={`Switch to ${theme === "dark" ? "light" : "dark"} mode`}>
              {theme === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8 rounded-full hidden sm:flex" onClick={onSignOut} aria-label="Sign out">
              <LogOut className="h-4 w-4" />
            </Button>
          </div>
        </header>
        
        {/* Page Content */}
        <div className="mx-auto max-w-[1400px] px-4 py-6 pb-24 lg:px-6 lg:pb-8">{children}</div>
      </section>

      {/* Mobile Bottom Navigation */}
      <nav className="fixed inset-x-0 bottom-0 z-40 border-t border-border bg-background/95 backdrop-blur-sm px-2 pb-[calc(env(safe-area-inset-bottom)+0.25rem)] pt-1 lg:hidden">
        <div className="mx-auto grid max-w-md grid-cols-5 gap-0.5">
          {navItems
            .filter((item) => item.mobile)
            .map((item) => {
              const Icon = item.icon;
              const isActive = view === item.key;
              return (
                <button
                  key={item.key}
                  className={cn(
                    "relative flex h-12 min-w-0 flex-col items-center justify-center gap-0.5 rounded-xl text-[10px] font-medium text-muted-foreground transition-colors",
                    "hover:bg-muted/50",
                    isActive && "text-foreground",
                  )}
                  type="button"
                  aria-current={isActive ? "page" : undefined}
                  onClick={() => setView(item.key)}
                >
                  <Icon className="h-4 w-4" />
                  <span className="mobile-nav-text block max-w-full truncate px-1">{item.mobileLabel ?? item.label}</span>
                  {item.key === "approvals" && pending > 0 ? (
                    <b className="absolute right-2 top-1 h-3.5 min-w-3.5 rounded-full bg-warning px-1 text-[8px] font-bold text-warning-foreground flex items-center justify-center">
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
