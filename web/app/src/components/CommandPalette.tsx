import { ReactNode, useEffect, useEffectEvent, useState } from "react";
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import {
  Activity,
  FileText,
  FolderKanban,
  Home,
  Layers3,
  LogOut,
  Moon,
  RefreshCw,
  Search,
  Settings,
  ShieldCheck,
  Sun,
  Terminal,
} from "lucide-react";
import type { ApiSnapshot } from "@/api";

type ViewKey = "home" | "runs" | "approvals" | "projects" | "templates" | "logs" | "audit" | "settings";

const navItems: Array<{ key: ViewKey; label: string; icon: typeof Home }> = [
  { key: "home", label: "Home", icon: Home },
  { key: "runs", label: "Runs", icon: Activity },
  { key: "approvals", label: "Approvals", icon: ShieldCheck },
  { key: "projects", label: "Projects", icon: FolderKanban },
  { key: "templates", label: "Templates", icon: Layers3 },
  { key: "logs", label: "Logs", icon: Terminal },
  { key: "audit", label: "Audit", icon: FileText },
  { key: "settings", label: "Settings", icon: Settings },
];

export function CommandPalette({
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
  const [searchTerm, setSearchTerm] = useState("");
  const onKeyOpenChange = useEffectEvent(onOpenChange);

  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.key === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        onKeyOpenChange(!open);
      }
    };
    document.addEventListener("keydown", down);
    return () => document.removeEventListener("keydown", down);
  }, [open]);

  return (
    <CommandDialog open={open} onOpenChange={onOpenChange}>
      <CommandInput
        placeholder="Type a command or search..."
        value={searchTerm}
        onValueChange={setSearchTerm}
      />
      <CommandList>
        <CommandEmpty>
          {onSearch && searchTerm ? (
            <CommandItem
              onSelect={() => {
                onSearch(searchTerm);
              }}
            >
              <Search className="mr-2 h-4 w-4" />
              <span>Search for "{searchTerm}"</span>
            </CommandItem>
          ) : (
            "No results found."
          )}
        </CommandEmpty>
        <CommandGroup heading="Navigation">
          {navItems.map((item) => {
            const Icon = item.icon;
            return (
              <CommandItem
                key={item.key}
                onSelect={() => {
                  setView(item.key);
                  onOpenChange(false);
                }}
              >
                <Icon className="mr-2 h-4 w-4" />
                <span>{item.label}</span>
                {view === item.key && <span className="ml-auto text-xs text-muted-foreground">Current</span>}
              </CommandItem>
            );
          })}
        </CommandGroup>
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
      </CommandList>
    </CommandDialog>
  );
}
