import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { auditEventsQuery } from "@/api";
import { AuditView } from "@/pages/AuditView";
import { validateSearch } from "@/router/search";
export const Route = createFileRoute("/_authenticated/audit")({ validateSearch, loader: ({ context }) => context.queryClient.ensureQueryData(auditEventsQuery()), component: AuditRoute });
function AuditRoute() { const { q } = Route.useSearch(); const audit = useQuery(auditEventsQuery()); if (audit.isError) return <p role="alert">{audit.error.message}</p>; return <AuditView events={audit.data ?? []} q={q} loading={audit.isPending} />; }
