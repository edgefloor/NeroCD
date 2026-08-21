import { type ReactNode } from "react";
import { FileText, ArrowRight } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { AuditEvent } from "@/api";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { matchesQuery } from "@/lib/format";
export function AuditView({ events, q, loading }: { events: AuditEvent[]; q?: string; loading?: boolean }): ReactNode { if (loading) return <SkeletonTable rows={8} />; const visibleEvents = events.filter((event) => matchesQuery(q, event.id, event.action, event.target_id, JSON.stringify(event.metadata))); return <Card><CardHeader><CardTitle>Audit trail</CardTitle></CardHeader><CardContent>{!visibleEvents.length ? <EmptyState title="No audit events" icon={FileText} /> : <div role="list" aria-label="Audit events" className="grid gap-2">{visibleEvents.map((event) => <div key={event.id} role="listitem" className="grid gap-1 rounded border p-3 md:grid-cols-[180px_1fr_1fr]"><strong><ArrowRight className="mr-1 inline h-4 w-4" />{event.action}</strong><span>{event.target_id}</span><code className="truncate">{JSON.stringify(event.metadata)}</code></div>)}</div>}</CardContent></Card>; }
