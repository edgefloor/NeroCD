import "./styles.css";

import { createRoot } from "react-dom/client";
import { useEffect, useMemo, useState } from "react";
import { Toaster } from "@/components/ui/sonner";
import { toast } from "sonner";

import { type ApiSnapshot } from "./api";
import { useAuth, useSnapshot, useMutate, useTheme, useSearch, type MutateFn } from "./hooks/useApi";
import { filterSnapshot } from "./lib/format";
import { Shell } from "./components/layout/Shell";
import { CommandPalette } from "./components/CommandPalette";
import { SignIn } from "./pages/SignIn";
import { HomeView } from "./pages/HomeView";
import { RunsView } from "./pages/RunsView";
import { ApprovalsView } from "./pages/ApprovalsView";
import { ProjectsView } from "./pages/ProjectsView";
import { TemplatesView } from "./pages/TemplatesView";
import { LogsView } from "./pages/LogsView";
import { AuditView } from "./pages/AuditView";
import { SettingsView } from "./pages/SettingsView";
import { SkeletonCard } from "./components/common/SkeletonCard";

type ViewKey = "home" | "runs" | "approvals" | "projects" | "templates" | "logs" | "audit" | "settings";

function App() {
  const { token, authError, signIn, signOut } = useAuth();
  const { data: snapshot, loading, error, refresh } = useSnapshot(token || null);
  const { busy, notice, mutate, setNotice } = useMutate(refresh);
  const { theme, toggleTheme } = useTheme();
  const { query, setQuery } = useSearch();
  const [view, setView] = useState<ViewKey>("home");
  const [commandPaletteOpen, setCommandPaletteOpen] = useState(false);

  useEffect(() => {
    if (notice) {
      if (notice.startsWith("Error") || notice.includes("Failed") || notice.includes("error")) {
        toast.error(notice);
      } else {
        toast.success(notice);
      }
      setNotice("");
    }
  }, [notice, setNotice]);

  useEffect(() => {
    if (error) {
      toast.error(error);
    }
  }, [error]);

  const filtered = useMemo(() => {
    if (!snapshot) return null;
    return filterSnapshot(snapshot, query);
  }, [snapshot, query]);

  if (!token) {
    return <SignIn error={authError} onSubmit={signIn} />;
  }

  return (
    <>
      <Toaster position="top-right" richColors theme={theme} />
      <CommandPalette
        open={commandPaletteOpen}
        onOpenChange={setCommandPaletteOpen}
        snapshot={snapshot}
        view={view}
        setView={setView}
        theme={theme}
        toggleTheme={toggleTheme}
        onRefresh={() => {
          void refresh();
          toast.info("Refreshing data...");
        }}
        onSignOut={signOut}
        onSearch={(q) => {
          setQuery(q);
          setCommandPaletteOpen(false);
        }}
      />
      <Shell
        snapshot={snapshot}
        view={view}
        setView={setView}
        notice={notice}
        query={query}
        setQuery={setQuery}
        theme={theme}
        toggleTheme={toggleTheme}
        onRefresh={() => {
          void refresh();
          toast.info("Refreshing data...");
        }}
        onSignOut={signOut}
        onOpenSearch={() => setCommandPaletteOpen(true)}
      >
        {!snapshot ? (
          <LoadingView view={view} />
        ) : (
          <Workspace
            view={view}
            snapshot={snapshot}
            filtered={filtered}
            query={query}
            setQuery={setQuery}
            token={token}
            busy={busy}
            mutate={mutate}
            loading={loading}
          />
        )}
      </Shell>
    </>
  );
}

function LoadingView({ view }: { view: ViewKey }) {
  switch (view) {
    case "runs":
      return (
        <section>
          <SkeletonCard rows={5} />
        </section>
      );
    case "approvals":
      return (
        <section className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
          <SkeletonCard rows={5} />
          <SkeletonCard rows={5} />
        </section>
      );
    case "projects":
      return (
        <section className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_380px]">
          <div className="grid gap-3">
            <SkeletonCard rows={2} />
            <SkeletonCard rows={2} />
          </div>
          <SkeletonCard rows={4} />
        </section>
      );
    case "templates":
      return (
        <section className="grid gap-4 xl:grid-cols-[380px_minmax(0,1fr)]">
          <SkeletonCard rows={4} />
          <SkeletonCard rows={5} />
        </section>
      );
    case "logs":
      return <SkeletonCard rows={8} />;
    case "audit":
      return <SkeletonCard rows={8} />;
    case "settings":
      return (
        <section className="grid gap-4 lg:grid-cols-2">
          <SkeletonCard rows={4} />
          <SkeletonCard rows={4} />
        </section>
      );
    case "home":
    default:
      return (
        <div className="grid gap-5">
          <SkeletonCard rows={1} />
          <section className="grid grid-cols-2 gap-3 xl:grid-cols-4">
            <SkeletonCard rows={1} />
            <SkeletonCard rows={1} />
            <SkeletonCard rows={1} />
            <SkeletonCard rows={1} />
          </section>
          <section className="grid gap-5 xl:grid-cols-[minmax(0,1.35fr)_minmax(340px,0.9fr)]">
            <SkeletonCard rows={5} />
            <SkeletonCard rows={3} />
          </section>
          <section className="grid gap-5 xl:grid-cols-[1fr_1fr]">
            <SkeletonCard rows={5} />
            <SkeletonCard rows={4} />
          </section>
        </div>
      );
  }
}

function Workspace({
  view,
  snapshot,
  filtered,
  query,
  setQuery,
  token,
  busy,
  mutate,
  loading,
}: {
  view: ViewKey;
  snapshot: ApiSnapshot;
  filtered: ApiSnapshot | null;
  query: string;
  setQuery: (query: string) => void;
  token: string;
  busy: string;
  mutate: MutateFn;
  loading: boolean;
}) {
  const workSnapshot = filtered ?? snapshot;

  const searchBanner =
    query ? (
      <div className="mb-5 flex items-center justify-between gap-3 rounded-lg border border-border/80 bg-card px-4 py-2.5 text-sm shadow-sm">
        <div className="min-w-0">
          <span className="font-medium">Filtered by "{query}"</span>
          <span className="ml-2 text-muted-foreground">
            {filtered
              ? filtered.projects.length +
                filtered.projectMembers.length +
                filtered.templates.length +
                filtered.runs.length +
                filtered.approvals.length +
                filtered.logs.length +
                filtered.repositories.length +
                filtered.auditEvents.length +
                filtered.capabilities.length
              : 0}{" "}
            matching records
          </span>
        </div>
        <button className="text-sm text-muted-foreground hover:text-foreground" onClick={() => setQuery("")}>
          Clear
        </button>
      </div>
    ) : null;

  switch (view) {
    case "runs":
      return (
        <>
          {searchBanner}
          <RunsView snapshot={workSnapshot} token={token} busy={busy} mutate={mutate} loading={loading} />
        </>
      );
    case "approvals":
      return (
        <>
          {searchBanner}
          <ApprovalsView snapshot={workSnapshot} token={token} busy={busy} mutate={mutate} loading={loading} />
        </>
      );
    case "projects":
      return (
        <>
          {searchBanner}
          <ProjectsView snapshot={workSnapshot} token={token} busy={busy} mutate={mutate} loading={loading} />
        </>
      );
    case "templates":
      return (
        <>
          {searchBanner}
          <TemplatesView snapshot={workSnapshot} token={token} busy={busy} mutate={mutate} loading={loading} />
        </>
      );
    case "logs":
      return (
        <>
          {searchBanner}
          <LogsView logs={workSnapshot.logs} loading={loading} />
        </>
      );
    case "audit":
      return (
        <>
          {searchBanner}
          <AuditView snapshot={workSnapshot} />
        </>
      );
    case "settings":
      return (
        <>
          {searchBanner}
          <SettingsView snapshot={workSnapshot} token={token} busy={busy} mutate={mutate} loading={loading} />
        </>
      );
    case "home":
    default:
      return (
        <HomeView
          snapshot={snapshot}
          workSnapshot={workSnapshot}
          query={query}
          onClearQuery={() => setQuery("")}
          token={token}
          busy={busy}
          mutate={mutate}
          loading={loading}
        />
      );
  }
}

const rootElement = document.querySelector<HTMLDivElement>("#app");
if (!rootElement) {
  throw new Error("missing #app root");
}

createRoot(rootElement).render(<App />);
