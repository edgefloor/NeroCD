import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { allProjectMembersQuery, principalQuery, ApiError } from "@/api";
import { applyRecovery, planRecovery, recoveryID, recoveryKey, recoveryQuery, type RecoveryApply, type RecoveryInput, type RecoveryPlan, type RecoveryResolution, type RecoveryReview, type RecoveryScope } from "@/api/hosting-proxy-recovery";
import { proxyKeys } from "@/api/hosting-proxy";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
const button = "h-auto max-w-full whitespace-normal break-all";
const denied = (error: unknown) => error instanceof ApiError && [401, 403, 404].includes(error.status);
type Props = { serverID: string; projectID: string; proxyID: string; operationID: string; blocked: boolean };
export function HostingProxyRecovery(props: Props) {
  const principal = useQuery(principalQuery()), actor = principal.data ? JSON.stringify([principal.data.provider, principal.data.id]) : "";
  const members = useQuery({ queryKey: ["proxyRecoveryMembers", actor, props.projectID], queryFn: ({ signal }) => allProjectMembersQuery().queryFn!({ signal } as never), enabled: Boolean(actor), retry: false, gcTime: 0, staleTime: 10_000 });
  const navigate = useNavigate();
  const verified = !principal.isPending && !principal.isFetching && !principal.isError && Boolean(principal.data) && !members.isPending && !members.isFetching && !members.isError;
  const authorized = principal.data?.roles.includes("system_admin") || principal.data?.roles.includes("runner_admin") && members.data?.some(member => member.project_id === props.projectID && member.user_id === principal.data?.id && member.role === "owner");
  useEffect(() => { if (verified && !authorized) void navigate({ to: ".", replace: true, resetScroll: false, search: previous => ({ ...previous, proxy_recovery_scope: undefined, proxy_recovery_operation: undefined }) }); }, [verified, authorized, navigate]);
  if (props.blocked || principal.isPending || principal.isFetching || principal.isError || !principal.data || members.isPending || members.isFetching || members.isError) return <p role="status">Metadata recovery access is pending or unavailable. Previous reviews and requests are cleared.</p>;
  const p = principal.data;
  const allowed = p.roles.includes("system_admin") || p.roles.includes("runner_admin") && members.data?.some(member => member.project_id === props.projectID && member.user_id === p.id && member.role === "owner");
  if (!allowed) return <p>Metadata recovery requires server administrator and control-project owner access. System administrators retain their override.</p>;
  const scope = { actor, actorID: p.id, server: props.serverID, project: props.projectID, proxy: props.proxyID };
  return <RecoveryLocator key={JSON.stringify([scope, p.roles])} scope={scope} provider={p.provider} defaultOperation={props.operationID} />;
}
function RecoveryLocator({ scope, provider, defaultOperation }: { scope: Omit<RecoveryScope, "operation">; provider: string; defaultOperation: string }) {
  const search = useSearch({ strict: false }), navigate = useNavigate();
  const locatorScope = JSON.stringify([provider, scope.actorID, scope.project, scope.proxy]);
  const [operation, setOperation] = useState(() => search.proxy_recovery_scope === locatorScope && recoveryID(search.proxy_recovery_operation) ? search.proxy_recovery_operation : defaultOperation);
  useEffect(() => { void navigate({ to: ".", replace: true, resetScroll: false, search: previous => ({ ...previous, proxy_recovery_scope: recoveryID(operation) ? locatorScope : undefined, proxy_recovery_operation: recoveryID(operation) ? operation : undefined }) }); }, [operation, locatorScope, navigate]);
  return <section aria-label="Proxy metadata recovery" className="min-w-0 space-y-3 border-t pt-3">
    <h5 className="font-medium">Recover a completed metadata conflict</h5>
    <p className="text-xs">Repair controller release pointers only after an accepted rollback or refusal. Disabled routes and revoked grants remain unchanged. This does not operate Docker, restore application data or secrets, perform a fresh TLS probe, or complete queued routing convergence.</p>
    {recoveryID(operation) ? <RecoveryPanel key={operation} scope={{ ...scope, operation }} currentOperation={defaultOperation} onSelectOperation={setOperation} /> : <><OperationNavigation selected={operation} current={defaultOperation} blocked={false} onSelect={setOperation} /><p>Select a held proxy operation to read its recovery status.</p></>}
  </section>;
}
function OperationNavigation({ selected, current, blocked, onSelect }: { selected: string; current: string; blocked: boolean; onSelect: (operation: string) => void }) {
  const [draft, setDraft] = useState(selected);
  return <div className="min-w-0 space-y-2">
    {recoveryID(current) && current !== selected ? <><p className="break-all">Current proxy operation: {current}. Recovery eligibility requires a fresh authorized read.</p><Button className={button} type="button" disabled={blocked} onClick={() => onSelect(current)}>Read current proxy operation</Button></> : null}
    {blocked ? <p role="status">Recover the identical pending request or explicitly clear its reviewed plan before changing operations.</p> : null}
    <details><summary>Look up an exact operation</summary><form aria-label="Look up metadata recovery operation" className="space-y-2" onSubmit={event => { event.preventDefault(); if (!blocked && recoveryID(draft)) onSelect(draft); }}><label htmlFor="proxy-recovery-operation">Original proxy operation ID</label><Input id="proxy-recovery-operation" maxLength={128} value={draft} disabled={blocked} onChange={event => setDraft(event.target.value)} /><Button className={button} disabled={blocked || !recoveryID(draft)} type="submit">Read exact metadata recovery</Button></form></details>
  </div>;
}
function ReviewFacts({ review }: { review: RecoveryReview }) {
  return <div className="min-w-0 space-y-2 text-sm"><dl className="space-y-1 break-all"><dt>Original operation</dt><dd>{review.operation_id}</dd><dt>Historical engine outcome</dt><dd>{review.engine_outcome}</dd><dt>Accepted completion receipt</dt><dd>{review.complete_receipt_id}</dd><dt>Completion receipt hash</dt><dd>{review.complete_receipt_hash}</dd><dt>Loaded generation</dt><dd>{review.loaded_generation}</dd><dt>Desired generation</dt><dd>{review.desired_generation}</dd><dt>Committed predecessor</dt><dd>{review.prior_operation_id}</dd><dt>Predecessor hash</dt><dd>{review.prior_snapshot_hash}</dd><dt>Review facts hash</dt><dd>{review.facts_hash}</dd></dl>
    <h6>Selected metadata repairs</h6><ul className="space-y-2 break-all">{review.routes.map(route => <li key={route.route_id}>{route.prior_target.hostname}{route.prior_target.path_prefix} · {route.route_id} · version {route.version}: {route.selected_release_operation_id} → {route.prior_target.release_operation_id}. Enabled: {route.enabled ? "yes" : "no"}; authorized: {route.authorized ? "yes" : "no"}. These flags remain unchanged.</li>)}</ul>
    <h6>Server-derived safe convergence</h6>{review.convergence_routes.length ? <ul className="space-y-1 break-all">{review.convergence_routes.map(route => <li key={route.route_id}>{route.hostname}{route.path_prefix} · route {route.route_id} · project {route.project_id} · application {route.resource_id} · binding {route.binding_id} · release {route.release_operation_id} · loopback port {route.host_port}</li>)}</ul> : <p>No routes are authorized in the queued convergence.</p>}
    <p>Loaded generation remains {review.loaded_generation} until ordinary runner work completes. A resolution does not prove that convergence completed or HTTPS is currently healthy.</p>
  </div>;
}
function Resolution({ value }: { value: RecoveryResolution }) {
  return <section aria-label="Recorded metadata recovery resolution" className="space-y-2 rounded-lg border p-3 text-sm break-all"><p>Recorded metadata resolution: {value.id}</p><p>Original operation: {value.operation_id}</p><p>Historical engine outcome: {value.engine_outcome}</p><p>Original completion receipt: {value.complete_receipt_id} · {value.complete_receipt_hash}</p><p>Reviewed plan: {value.plan_id} · {value.plan_hash}</p><p>Recorded actor: {value.actor_id}</p><p>Queued convergence operation: {value.convergence_operation_id}</p><p>Recorded at: {value.created_at}</p><ul>{value.routes.map(route => <li key={route.route_id}>{route.route_id}: {route.before_release_operation_id} (version {route.before_version}) → {route.after_release_operation_id} (version {route.after_version}); enabled remains {route.enabled ? "yes" : "no"}.</li>)}</ul><p>This receipt records metadata recovery and one queued convergence. It does not establish fresh TLS health, completed convergence, data rollback or restored grants.</p></section>;
}
function RecoveryPanel({ scope, currentOperation, onSelectOperation }: { scope: RecoveryScope; currentOperation: string; onSelectOperation: (operation: string) => void }) {
  const client = useQueryClient();
  const [permissionDenied, setPermissionDenied] = useState(false);
  const status = useQuery({ ...recoveryQuery(scope), enabled: !permissionDenied });
  const [intent, setIntent] = useState<{ body: RecoveryInput; review: RecoveryReview }>(), [plan, setPlan] = useState<RecoveryPlan>(), [apply, setApply] = useState<RecoveryApply>(), [resolution, setResolution] = useState<RecoveryResolution>();
  const [confirmed, setConfirmed] = useState(false), [busy, setBusy] = useState(false), [conflict, setConflict] = useState(false), [error, setError] = useState(""), [clock, setClock] = useState(Date.now());
  const active = useRef(false), sending = useRef(false), request = useRef<AbortController | null>(null);
  useEffect(() => { active.current = true; return () => { active.current = false; const controller = request.current; request.current = null; controller?.abort(); }; }, []);
  useEffect(() => { if (plan) { const timer = setInterval(() => setClock(Date.now()), 1000); return () => clearInterval(timer); } }, [plan]);
  const clear = () => { setIntent(undefined); setPlan(undefined); setApply(undefined); setConfirmed(false); setConflict(false); setError(""); };
  const losePermission = () => { const controller = request.current; request.current = null; controller?.abort(); clear(); setResolution(undefined); setPermissionDenied(true); };
  useEffect(() => { if (denied(status.error)) losePermission(); }, [status.error]);
  useEffect(() => { if (permissionDenied) client.removeQueries({ queryKey: recoveryKey(scope) }); }, [permissionDenied, client, scope]);
  async function mutate(kind: "plan" | "apply") {
    if (sending.current || permissionDenied || conflict || status.isError || status.isFetching || kind === "apply" && (!plan || !confirmed)) return;
    let frozen = intent;
    if (kind === "plan" && !frozen) {
      const review = status.data?.review; if (!status.data?.eligible || !review) return;
      frozen = { review: structuredClone(review), body: { idempotency_key: crypto.randomUUID(), expected_loaded_generation: review.loaded_generation, expected_desired_generation: review.desired_generation, expected_complete_receipt_id: review.complete_receipt_id, expected_complete_receipt_hash: review.complete_receipt_hash } }; setIntent(frozen);
    }
    const applyBody = apply ?? { idempotency_key: crypto.randomUUID(), plan_hash: plan?.plan_hash ?? "" };
    if (kind === "apply") setApply(applyBody);
    const controller = new AbortController(); request.current = controller; sending.current = true; setBusy(true); setError("");
    try {
      if (kind === "plan") { const result = await planRecovery(scope, frozen!.body, frozen!.review, controller.signal); if (active.current && request.current === controller && !controller.signal.aborted) setPlan(result); }
      else { const result = await applyRecovery(scope, plan!, applyBody, controller.signal); if (active.current && request.current === controller && !controller.signal.aborted) { setResolution(result); clear(); await status.refetch(); await client.invalidateQueries({ queryKey: proxyKeys.server(scope.actor, scope.server) }); } }
    } catch (cause) {
      if (!active.current || request.current !== controller || controller.signal.aborted) return;
      if (denied(cause)) losePermission();
      else if (cause instanceof ApiError && cause.status === 409) { setConflict(true); setConfirmed(false); setError("Recovery conflict. Read fresh facts, request a new server plan with a new key, review it and confirm again. Nothing was resubmitted automatically."); }
      else setError(`The ${kind} response was not confirmed. It may have been accepted. Only retry the identical frozen ${kind} request; retries do not renew its original expiry.`);
    } finally { if (request.current === controller) { request.current = null; sending.current = false; if (active.current) setBusy(false); } }
  }
  if (permissionDenied || denied(status.error)) return <p role="alert">Metadata recovery permission is unavailable. Reviews and requests are cleared. Reload to verify current access.</p>;
  return <div className="min-w-0 space-y-3">
    <OperationNavigation selected={scope.operation} current={currentOperation} blocked={busy || Boolean(intent && !conflict)} onSelect={onSelectOperation} />
    <p className="break-all">Recovery operation: {scope.operation}</p><Button className={button} variant="outline" disabled={busy || status.isFetching} onClick={() => void status.refetch()}>Read metadata recovery status</Button>
    {status.isPending ? <p role="status">Reading recovery eligibility...</p> : null}{status.isError ? <p role="alert">Recovery status is unknown because the read failed. New mutations are paused; retained evidence is historical.</p> : null}
    {status.data?.reason === "unsupported_or_unfinished" ? <p role="status">Recovery is unsupported or unfinished. Missing accepted completion, uncertain engine outcome, changed predecessor, restore ownership, foreign authority or competing work cannot be unlocked here.</p> : null}
    {status.data?.review && !intent ? <><p>Server reports a completed metadata conflict eligible for review.</p><ReviewFacts review={status.data.review} /><Button className={button} disabled={busy || status.isError || status.isFetching || conflict} onClick={() => void mutate("plan")}>Request server recovery plan</Button></> : null}
    {intent && !plan ? <><p>Plan request is frozen in memory; no apply request has been made.</p><Button className={button} disabled={busy || status.isError || status.isFetching || conflict} onClick={() => void mutate("plan")}>Retry identical recovery plan request</Button></> : null}
    {plan ? <section aria-label="Review exact metadata recovery plan" className="min-w-0 space-y-3 rounded-lg border p-3"><p className="break-all">Plan: {plan.id}</p><p className="break-all">Plan hash: {plan.plan_hash}</p><p>Original database creation: {plan.created_at}</p><p>Original database expiry: {plan.expires_at}</p><p>{clock >= Date.parse(plan.expires_at) ? "The local clock is at or beyond the recorded expiry." : "The local clock is before the recorded expiry."} Clock display is informative; the server decides expiry. Retries never renew this plan.</p><ReviewFacts review={plan.review} /><label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={confirmed} disabled={busy || Boolean(apply) || conflict} onChange={event => setConfirmed(event.target.checked)} /><span>I confirm this exact metadata repair and one queued convergence; route flags and grants remain unchanged.</span></label><Button className={button} disabled={!confirmed || busy || conflict || status.isError || status.isFetching} onClick={() => void mutate("apply")}>{apply ? "Retry identical recovery apply request" : "Apply reviewed metadata recovery"}</Button></section> : null}
    {intent ? <Button className={button} variant="outline" disabled={busy || status.isFetching || Boolean(apply) && !conflict || !plan && !conflict} onClick={async () => { const result = await status.refetch(); if (active.current && !result.isError) clear(); }}>Read fresh facts for a new review</Button> : null}
    {resolution || status.data?.resolution ? <Resolution value={resolution ?? status.data!.resolution!} /> : null}
    {error ? <p role="alert">{error}</p> : null}
  </div>;
}
