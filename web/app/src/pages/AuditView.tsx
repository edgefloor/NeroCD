import { ReactNode } from "react";
import { FileText, ArrowRight } from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import type { ApiSnapshot } from "@/api";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";

export function AuditView({ snapshot, loading }: { snapshot: ApiSnapshot; loading?: boolean }): ReactNode {
  if (loading) {
    return (
      <Card>
        <CardHeader className="border-b py-3">
          <CardTitle className="text-base font-semibold">Audit trail</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <SkeletonTable rows={8} />
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader className="border-b py-3">
         <CardTitle className="text-base font-semibold">Audit trail</CardTitle>
      </CardHeader>
      <CardContent className="grid gap-2 p-3">
        {snapshot.auditEvents.length === 0 ? (
            <EmptyState title="No audit events" icon={FileText} />
        ) : (
          snapshot.auditEvents.map((event) => (
            <div key={event.id} className="grid gap-1 rounded-lg border border-border bg-card p-3 md:grid-cols-[180px_1fr_1fr] md:items-center">
              <div className="flex items-center gap-2">
                <ArrowRight className="h-4 w-4 text-muted-foreground" />
                <strong className="text-sm font-medium">{event.action}</strong>
              </div>
              <span className="text-sm text-muted-foreground">{event.target_id}</span>
              <code className="truncate text-xs text-muted-foreground bg-muted px-2 py-1 rounded font-mono">{JSON.stringify(event.metadata)}</code>
            </div>
          ))
        )}
      </CardContent>
    </Card>
  );
}
