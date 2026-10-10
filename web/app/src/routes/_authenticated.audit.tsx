import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { auditEventsQuery } from "@/api";
import { AuditView } from "@/pages/AuditView";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { validateSearch } from "@/router/search";
export const Route = createFileRoute("/_authenticated/audit")({ validateSearch, loader: ({ context }) => context.queryClient.ensureQueryData(auditEventsQuery()), component: AuditRoute });
function AuditRoute() { const { q } = Route.useSearch(); const page = usePage(); const audit = useQuery(auditEventsQuery({ limit: page.limit, offset: page.offset })); if (audit.isError) return <p role="alert">{audit.error.message}</p>; return <><AuditView events={audit.data?.items ?? []} q={q} loading={audit.isPending} /><PageNavigation page={audit.data} label="audit events" loading={audit.isFetching} onPrevious={page.previous} onNext={page.next} /></>; }
