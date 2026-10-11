import { type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { maximumPageOffset } from "@/router/search";
import { Button } from "@/components/ui/button";

export const operatorPageSize = 50;

export type PageMetadata = {
  count: number;
  limit: number;
  offset: number;
  total: number;
};

export function usePage(): {
  limit: number;
  offset: number;
  previous: () => void;
  next: () => void;
} {
  const navigate = useNavigate();
  const { offset = 0 } = useSearch({ strict: false });
  const move = (delta: number) => void navigate({
    to: ".",
    search: (previous) => ({ ...previous, offset: Math.min(maximumPageOffset, Math.max(0, (previous.offset ?? 0) + delta)) || undefined }),
    resetScroll: false,
  });
  return {
    limit: operatorPageSize,
    offset,
    previous: () => move(-operatorPageSize),
    next: () => move(operatorPageSize),
  };
}

export function PageNavigation({ page, label, loading = false, onPrevious, onNext }: {
  page?: PageMetadata;
  label: string;
  loading?: boolean;
  onPrevious: () => void;
  onNext: () => void;
}): ReactNode {
  const first = page && page.count > 0 ? page.offset + 1 : 0;
  const last = page && page.count > 0 ? page.offset + page.count : 0;
  return (
    <nav className="flex flex-wrap items-center justify-between gap-2 border-t px-3 py-3" aria-label={`${label} pages`}>
      <p className="text-sm text-muted-foreground" aria-live="polite">Showing {first}-{last} of {page?.total ?? 0}</p>
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" size="sm" aria-label={`Previous ${label}`} disabled={loading || !page || page.offset === 0} onClick={onPrevious}>Previous</Button>
        <Button type="button" variant="outline" size="sm" aria-label={`Next ${label}`} disabled={loading || !page || page.count === 0 || last >= page.total} onClick={onNext}>Next</Button>
      </div>
    </nav>
  );
}
