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
    <div className="mb-5 flex min-w-0 flex-wrap items-center justify-between gap-3 rounded-xl border border-border/60 bg-card px-4 py-2.5 text-sm shadow-sm">
      <div className="min-w-0 flex-1 basis-48 break-words [overflow-wrap:anywhere]">
        <span className="font-medium text-foreground">Filtered by "{query}"</span>
        <span className="ml-2 text-muted-foreground">{resultCount} matching records</span>
      </div>
      <Button variant="ghost" size="sm" onClick={onClear} className="h-8 shrink-0 rounded-lg">
        <X className="h-4 w-4" /> Clear
      </Button>
    </div>
  );
}
