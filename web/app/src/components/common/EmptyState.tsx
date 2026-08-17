import { ReactNode } from "react";
import { Box, Plus, ArrowRight, type LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/button";

interface EmptyStateProps {
  title?: string;
  description?: string;
  icon?: LucideIcon;
  action?: {
    label: string;
    onClick: () => void;
  };
  children?: ReactNode;
}

export function EmptyState({
  title = "No data yet",
  description = "Nothing to show.",
  icon: Icon = Box,
  action,
  children,
}: EmptyStateProps): ReactNode {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-10 text-center px-4">
      <div className="grid h-10 w-10 place-items-center rounded-lg border border-border/60 bg-transparent">
        <Icon className="h-5 w-5 text-muted-foreground" />
      </div>
      <div className="space-y-1">
        <p className="text-sm font-medium text-foreground">{title}</p>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      {action ? (
        <Button 
          size="sm" 
          onClick={action.onClick}
          className="mt-1"
        >
          <Plus className="h-4 w-4 mr-1.5" />
          {action.label}
        </Button>
      ) : null}
      {children}
    </div>
  );
}
