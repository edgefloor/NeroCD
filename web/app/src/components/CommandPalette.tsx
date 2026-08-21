import { ReactNode, useEffect, useRef, useState } from "react";
import { useLocation, useNavigate } from "@tanstack/react-router";
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
  LogOut,
  Moon,
  RefreshCw,
  Search,
  Sun,
} from "lucide-react";
import { navigationItems, type NavigationItem } from "@/router/metadata";

export function CommandPalette({
  open,
  onOpenChange,
  theme,
  toggleTheme,
  onRefresh,
  onSignOut,
  onSearch,
  navigation = navigationItems,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  theme: "light" | "dark";
  toggleTheme: () => void;
  onRefresh: () => void;
  onSignOut: () => void;
  onSearch?: (query: string) => void;
  navigation?: NavigationItem[];
}): ReactNode {
  const [searchTerm, setSearchTerm] = useState("");
  const navigate = useNavigate();
  const location = useLocation();
  const openerRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.key === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        if (!open && document.activeElement instanceof HTMLElement) openerRef.current = document.activeElement;
        onOpenChange(!open);
      }
    };
    document.addEventListener("keydown", down);
    return () => document.removeEventListener("keydown", down);
  }, [open, onOpenChange]);

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      onCloseAutoFocus={(event) => {
        if (!openerRef.current?.isConnected) return;
        event.preventDefault();
        openerRef.current.focus();
      }}
    >
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
          {navigation.map((item) => {
            const Icon = item.icon;
            return (
              <CommandItem
                key={item.to}
                onSelect={() => {
                  void navigate({ to: item.to, search: (previous) => previous });
                  onOpenChange(false);
                }}
              >
                <Icon className="mr-2 h-4 w-4" />
                <span>{item.label}</span>
                {location.pathname === item.to && <span className="ml-auto text-xs text-muted-foreground">Current</span>}
              </CommandItem>
            );
          })}
        </CommandGroup>
        <CommandSeparator />
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
      </CommandList>
    </CommandDialog>
  );
}
