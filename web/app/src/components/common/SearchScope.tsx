import { ReactNode } from "react";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";

export function SearchScope({
  query,
  resultCount,
  onClear,
}: {
  query: string;
  resultCount: number;
  onClear: () => void;
}): ReactNode {
  return (
    <div className="mb-4 flex items-center justify-between gap-3 rounded-lg border border-border/80 bg-card/95 px-4 py-2.5 text-sm shadow-sm">
      <div className="min-w-0">
        <span className="font-medium">Filtered by "{query}"</span>
        <span className="ml-2 text-muted-foreground">{resultCount} matching records</span>
      </div>
      <Button variant="ghost" size="sm" onClick={onClear}>
        <X className="h-4 w-4" /> Clear
      </Button>
    </div>
  );
}
