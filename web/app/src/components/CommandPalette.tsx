import { ReactNode, useEffect, useEffectEvent, useRef, useState } from "react";
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { LogOut, Moon, RefreshCw, Search, Sun } from "lucide-react";
import type { ApiSnapshot } from "@/api";
import { navigationForRoles, navigationGroups, type ViewKey } from "@/router/metadata";

export function CommandPalette({
  roles = [],
  open,
  onOpenChange,
  snapshot,
  view,
  setView,
  theme,
  toggleTheme,
  onRefresh,
  onSignOut,
  onSearch,
}: {
  roles?: readonly string[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  snapshot: ApiSnapshot | null;
  view: ViewKey;
  setView: (view: ViewKey) => void;
  theme: "light" | "dark";
  toggleTheme: () => void;
  onRefresh: () => void;
  onSignOut: () => void;
  onSearch?: (query: string) => void;
}): ReactNode {
  const onKeyOpenChange = useEffectEvent(onOpenChange);
  const returnFocus = useRef<HTMLElement | null>(null);
  const rememberFocus = useEffectEvent((event: Event) => {
    if (open) return;
    const target = event.type === "pointerdown" && event.target instanceof Element
      ? event.target.closest<HTMLElement>("button,a,input,textarea,select,[tabindex]") : document.activeElement;
    if (target instanceof HTMLElement) returnFocus.current = target;
  });
  useEffect(() => {
    if (open) return;
    const remember = (event: Event) => rememberFocus(event);
    document.addEventListener("pointerdown", remember, true);
    document.addEventListener("focusin", remember, true);
    return () => { document.removeEventListener("pointerdown", remember, true); document.removeEventListener("focusin", remember, true); };
  }, [open]);

  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.key === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        if (!open && document.activeElement instanceof HTMLElement) returnFocus.current = document.activeElement;
        onKeyOpenChange(!open);
      }
    };
    document.addEventListener("keydown", down);
    return () => document.removeEventListener("keydown", down);
  }, [open]);

  return (
    <CommandDialog open={open} onOpenChange={onOpenChange} onCloseAutoFocus={(event) => { event.preventDefault(); if (returnFocus.current?.isConnected) returnFocus.current.focus(); }}>
      <CommandPaletteContents key={String(open)} roles={roles} snapshot={snapshot} view={view} setView={setView}
        theme={theme} toggleTheme={toggleTheme} onRefresh={onRefresh} onSignOut={onSignOut}
        onOpenChange={onOpenChange} onSearch={onSearch} />
    </CommandDialog>
  );
}

function CommandPaletteContents({ roles = [], snapshot, view, setView, theme, toggleTheme, onRefresh, onSignOut, onOpenChange, onSearch }: Omit<Parameters<typeof CommandPalette>[0], "open">) {
  const [searchTerm, setSearchTerm] = useState("");
  const navItems = navigationForRoles(roles);
  return <>
      <CommandInput
        placeholder="Find a page or action..."
        value={searchTerm}
        onValueChange={setSearchTerm}
      />
      <CommandList>
        <CommandEmpty>No matching pages or actions.</CommandEmpty>
        {navigationGroups.map((group) => <CommandGroup key={group} heading={group}>
          {navItems.filter((item) => item.group === group).map((item) => {
            return (
              <CommandItem
                key={item.key}
                value={item.label}
                keywords={[item.key]}
                onSelect={() => {
                  setView(item.key);
                  onOpenChange(false);
                }}
              >
                <span>{item.label}</span>
                {view === item.key && <span className="ml-auto text-xs text-muted-foreground">Current</span>}
              </CommandItem>
            );
          })}
        </CommandGroup>)}
        <CommandSeparator />
        {snapshot && (
          <CommandGroup heading="Quick Actions">
            <CommandItem
              onSelect={() => {
                onRefresh();
                onOpenChange(false);
              }}
            >
              <RefreshCw className="mr-2 h-4 w-4" />
              <span>Refresh data</span>
            </CommandItem>
            <CommandItem
              onSelect={() => {
                toggleTheme();
                onOpenChange(false);
              }}
            >
              {theme === "dark" ? <Sun className="mr-2 h-4 w-4" /> : <Moon className="mr-2 h-4 w-4" />}
              <span>Switch to {theme === "dark" ? "light" : "dark"} mode</span>
            </CommandItem>
            <CommandItem
              onSelect={() => {
                onSignOut();
                onOpenChange(false);
              }}
            >
              <LogOut className="mr-2 h-4 w-4" />
              <span>Sign out</span>
            </CommandItem>
          </CommandGroup>
        )}
        {onSearch && searchTerm.trim() && <CommandGroup heading="Current page" forceMount>
          <CommandItem forceMount value={`filter-page-${searchTerm}`} onSelect={() => onSearch(searchTerm)}>
            <Search className="mr-2 h-4 w-4" /><span>Filter this page for "{searchTerm}"</span>
          </CommandItem>
        </CommandGroup>}
      </CommandList>
  </>;
}
