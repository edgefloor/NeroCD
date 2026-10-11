import { type ReactNode } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { AuditEvent } from "@/api";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { matchesQuery } from "@/lib/format";

export function AuditView({ events, q, loading }: { events: AuditEvent[]; q?: string; loading?: boolean }): ReactNode {
  if (loading) return <SkeletonTable rows={8} />;
  const filtering = Boolean(q?.trim());
  const visibleEvents = events.filter((event) => matchesQuery(q, event.id, event.actor_id, event.action, event.target_id, event.created_at, JSON.stringify(event.metadata)));
  return <Card className="min-w-0">
    <CardHeader><CardTitle>Audit trail</CardTitle></CardHeader>
    <CardContent className="grid min-w-0 gap-3">
      {filtering ? <p role="status" className="text-sm text-muted-foreground">{visibleEvents.length} of {events.length} loaded audit events match this filter.</p> : null}
      {!visibleEvents.length ? <p className="text-sm text-muted-foreground">{filtering ? "No audit events match this filter on the loaded page." : "No audit events on this page."}</p> : <div role="list" aria-label="Audit events" className="grid min-w-0 gap-3">
        {visibleEvents.map((event) => <div key={event.id} role="listitem" className="grid min-w-0 gap-3 rounded border p-3">
          <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-2">
            <strong className="min-w-0 break-all">{event.action}</strong>
            <time dateTime={event.created_at} className="min-w-0 break-words text-xs text-muted-foreground">{event.created_at}</time>
          </div>
          <dl className="grid min-w-0 gap-3 text-sm md:grid-cols-2">
            <div className="min-w-0"><dt className="text-xs text-muted-foreground">Actor</dt><dd className="break-all font-mono">{event.actor_id || "Not recorded"}</dd></div>
            <div className="min-w-0"><dt className="text-xs text-muted-foreground">Target</dt><dd className="break-all font-mono">{event.target_id || "Not recorded"}</dd></div>
          </dl>
          <details className="min-w-0 text-sm">
            <summary className="cursor-pointer rounded focus-visible:outline-2 focus-visible:outline-ring">Event details</summary>
            <dl className="mt-3 min-w-0"><dt className="text-xs text-muted-foreground">Event ID</dt><dd className="break-all font-mono">{event.id}</dd></dl>
            <pre className="mt-3 min-w-0 whitespace-pre-wrap break-words rounded bg-muted p-3 text-xs">{JSON.stringify(event.metadata, null, 2)}</pre>
          </details>
        </div>)}
      </div>}
    </CardContent>
  </Card>;
}
