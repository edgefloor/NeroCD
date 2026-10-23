import { useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { allProjectMembersQuery, ApiError, principalQuery } from "@/api";
import type { Resource } from "@/api/hosting";
import { requestSwitch, switchKeys, switchOperationQuery, switchOptionsQuery, switchResolved, type SwitchInput, type SwitchOperation, type SwitchOptions, type SwitchScope } from "@/api/hosting-route-switch";
import { canMaintainProject } from "@/lib/permissions";
import { Button } from "@/components/ui/button";
const button = "h-auto max-w-full whitespace-normal break-all";
const reasons: Record<SwitchOptions["reason"], string> = {
  available: "The server reports eligible choices. Submission rechecks all permissions, versions and loaded generation.", unbound: "Bind this application to an authorized destination first.", proxy_unavailable: "Current proxy or release evidence is unavailable. Ask the server operator to verify proxy configuration.", busy: "Proxy work or ownership is unresolved. Review the current operation; another switch must wait.", restore_hold: "Restore ownership is held. Refreshing or retrying cannot release it.", runner_ineligible: "The runner needs verified-switch capability and explicit --proxy-verified-switch configuration.", no_loaded_generation: "No committed loaded generation is available. Complete ordinary route setup first.", stale_routes: "Desired routes differ from the loaded generation. Settle ordinary route work before switching releases.", no_routes: "No enabled existing routes are available. Create and settle a route first.", no_targets: "No eligible healthy or retained-running release is recorded for this binding and daemon.",
};
function denied(error: unknown) { return error instanceof ApiError && [401, 403, 404].includes(error.status); }
export function ResourceRouteSwitch({ resource, writable, blocked }: { resource: Resource; writable: boolean; blocked: boolean }) {
  const identity = useQuery(principalQuery());
  const actor = identity.data ? JSON.stringify([identity.data.provider, identity.data.id]) : "";
  const members = useQuery({ queryFn: ({ signal }) => allProjectMembersQuery().queryFn!({ signal } as never), queryKey: ["routeSwitchMembers", actor, resource.project_id], enabled: Boolean(actor), retry: false, gcTime: 0, staleTime: 10_000 });
  const [deniedScope, setDeniedScope] = useState("");
  const scope = JSON.stringify([actor, resource.project_id, resource.id]);
  if (blocked || identity.isPending || identity.isFetching || identity.isError || !identity.data || members.isPending || members.isFetching || members.isError) return <p role="status">Route switch access is pending or unavailable. Previous choices, receipts and requests are cleared.</p>;
  const readable = identity.data.roles.includes("system_admin") || members.data?.some((member) => member.project_id === resource.project_id && member.user_id === identity.data.id);
  if (!readable || deniedScope === scope) return <p role="alert">Route switch permission is unavailable. Reload to verify current project access. Previous choices and requests are cleared.</p>;
  const canWrite = writable && canMaintainProject(identity.data, members.data ?? [], resource.project_id);
  return <SwitchPanel key={JSON.stringify([scope, canWrite, identity.data.roles])} scope={{ actor, project: resource.project_id, resource: resource.id }} name={resource.name} writable={canWrite} onDenied={() => setDeniedScope(scope)} />;
}
function RecordedSwitch({ operation, unavailable, fetching }: { operation: SwitchOperation; unavailable: boolean; fetching: boolean }) {
  return <section aria-label="Recorded route switch operation" className="min-w-0 space-y-2 rounded-lg border p-3 text-sm">
    <h6 className="font-medium">Recorded route switch</h6><p className="break-all">Operation: {operation.id}</p>
    <p>Current status fetch: {unavailable ? "unknown, refresh failed" : fetching ? "reading, current state unconfirmed" : "loaded"}</p>
    <p>Recorded operation state: {operation.state}</p><p>Historical engine outcome: {operation.engine_outcome ?? "not recorded"}</p>
    {!switchResolved(operation) ? <p role="status">This operation is pending or unresolved. A separate switch is paused.</p> : null}
    {operation.metadata_conflict ? <p role="alert">Metadata conflict: the original engine outcome is preserved, but desired metadata could not be restored by exact ownership checks. Keep the ownership hold. This operator workflow has no reconciliation or unlock action.</p> : null}
    {operation.mutation_hold ? <p role="status">Mutation ownership remains held. A recorded rollback does not clear a metadata conflict.</p> : null}
    {operation.restore_ownership_held ? <p role="alert">Restore ownership is held. There is no operator unlock action.</p> : null}
    {operation.engine_outcome === "uncertain" ? <p role="alert">Engine outcome uncertain. Do not assume either release is serving or that rollback completed.</p> : null}
    <p className="text-xs">Engine outcomes and HTTP/HTTPS observations are historical evidence. This read does not perform a fresh TLS probe, restore application data, or release ownership.</p>
    <p>Reviewed loaded generation: {operation.switch?.expected_loaded_generation}</p>
    <ul className="space-y-1 break-all">{operation.switch?.routes.map((route) => <li key={route.route_id}>{route.route_id}: {route.before_release_operation_id} → {route.after_release_operation_id}</li>)}</ul>
  </section>;
}
function SwitchPanel({ scope, name, writable, onDenied }: { scope: SwitchScope; name: string; writable: boolean; onDenied: () => void }) {
  const client = useQueryClient(), id = useId();
  const [after, setAfter] = useState(""), [target, setTarget] = useState(""), [routes, setRoutes] = useState<string[]>([]), [selected, setSelected] = useState("");
  const [intent, setIntent] = useState<{ body: SwitchInput; routes: SwitchOptions["routes"]; target: SwitchOptions["targets"][number] }>();
  const [confirmed, setConfirmed] = useState(false), [attempted, setAttempted] = useState(false), [conflict, setConflict] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const active = useRef(false), sending = useRef(false), request = useRef<AbortController | null>(null);
  const choices = useQuery({ ...switchOptionsQuery(scope, after), refetchInterval: (query) => query.state.status === "error" ? false : 5000 });
  const operation = useQuery({ ...switchOperationQuery(scope, selected), enabled: Boolean(selected), refetchInterval: (query) => query.state.status === "error" || query.state.data && switchResolved(query.state.data) ? false : 5000 });
  useEffect(() => { active.current = true; return () => { active.current = false; const current = request.current; request.current = null; current?.abort(); }; }, []);
  useEffect(() => { if (denied(choices.error) || denied(operation.error)) onDenied(); }, [choices.error, operation.error, onDenied]);
  useEffect(() => { if (!choices.isError && choices.data?.pending_operation_id) setSelected(choices.data.pending_operation_id); }, [choices.data, choices.isError]);
  const data = choices.data;
  const held = Boolean(selected && (operation.isPending || operation.isError || !operation.data || !switchResolved(operation.data)));
  const canBegin = writable && !busy && !choices.isPending && !choices.isFetching && !choices.isError && data?.available && !data.pending_operation_id && !held;
  function clearReview() { setIntent(undefined); setConfirmed(false); setAttempted(false); setConflict(false); setError(""); setTarget(""); setRoutes([]); }
  function review() {
    const selectedTarget = data?.targets.find((item) => item.operation_id === target);
    const chosen = data?.routes.filter((route) => routes.includes(route.id));
    if (!canBegin || intent || !data?.loaded_generation || !selectedTarget || !chosen?.length || chosen.length !== routes.length) return;
    setIntent({ body: { idempotency_key: crypto.randomUUID(), expected_loaded_generation: data.loaded_generation, target_release_operation_id: selectedTarget.operation_id, routes: chosen.map((route) => ({ route_id: route.id, expected_version: route.version, expected_selected_release_operation_id: route.selected_release_operation_id })) }, routes: chosen.map((route) => ({ ...route })), target: { ...selectedTarget } }); setConfirmed(false); setError("");
  }
  async function submit() {
    if (!intent || !writable || !confirmed || busy || sending.current || conflict || !attempted && !canBegin) return;
    const controller = new AbortController(); request.current = controller; sending.current = true; setBusy(true); setAttempted(true); setError("");
    try {
      const result = await requestSwitch(scope, intent.body, controller.signal);
      if (!active.current || request.current !== controller || controller.signal.aborted) return;
      client.setQueryData(switchKeys.operation(scope, result.id), result); setSelected(result.id); clearReview();
      await client.invalidateQueries({ queryKey: ["hostingRoutes", scope.actor, scope.project, scope.resource] }); await choices.refetch();
    } catch (cause) {
      if (!active.current || request.current !== controller || controller.signal.aborted) return;
      if (denied(cause)) { onDenied(); return; }
      if (cause instanceof ApiError && cause.status === 409) { setConflict(true); setConfirmed(false); setError("Switch conflict. Refresh the authoritative choices, review the new versions and generation, and confirm a new request. Nothing was resubmitted automatically."); await choices.refetch(); }
      else setError("The response was not confirmed. This request may have been accepted. Only an identical retry can recover its operation; the key, target, generation and route versions remain frozen.");
    } finally { if (request.current === controller) { request.current = null; sending.current = false; if (active.current) setBusy(false); } }
  }
  async function refreshReview() {
    if (busy || attempted && !conflict) return;
    const result = await choices.refetch();
    if (!active.current) return;
    if (!result.isError && result.data?.available && !result.data.pending_operation_id && !held) clearReview();
    else setError("Current choices are unavailable or ownership is still held. No separate switch was enabled.");
  }
  return <section className="min-w-0 space-y-3 border-t pt-3" aria-label={`Release switching for ${name}`}>
    <h5 className="font-medium">Switch existing routes to a release</h5>
    <p className="text-xs">Select existing enabled routes and a server-qualified healthy or retained-running release. This changes routing only. It does not deploy or restart the release, roll back application data, or establish current HTTPS health.</p>
    <Button className={button} type="button" variant="outline" disabled={choices.isFetching || busy} onClick={() => { void choices.refetch(); if (selected) void operation.refetch(); }}>Refresh route switch status</Button>
    {choices.isPending ? <p role="status">Loading authoritative route switch choices...</p> : null}
    {choices.isError ? <p role="alert">Unable to refresh switch choices. Current availability is unknown. Retained choices are historical; new requests are paused.</p> : null}
    {data ? <><p>Server-reported switch availability: {choices.isError ? "unknown" : data.available ? "available" : "unavailable"}</p><p>{reasons[data.reason]}</p><p>Loaded generation from scoped read: {data.loaded_generation ?? "not available"}</p></> : null}
    {selected ? <><p className="break-all">Recovered or requested switch: {selected}</p>{operation.isError ? <p role="alert">Unable to fetch this exact switch. Current state is unknown; separate switches remain paused.</p> : null}{operation.data ? <RecordedSwitch operation={operation.data} unavailable={operation.isError} fetching={operation.isFetching} /> : <p role="status">Reading exact route switch operation...</p>}</> : null}
    {!intent && data ? <div className="space-y-3">
      <fieldset disabled={!canBegin}><legend>Select existing routes</legend>{data.routes.map((route) => <label key={route.id} className="flex items-start gap-2 break-all text-sm"><input type="checkbox" checked={routes.includes(route.id)} onChange={(event) => setRoutes((current) => event.target.checked ? [...current, route.id] : current.filter((item) => item !== route.id))} /><span>{route.hostname}{route.path_prefix} · {route.id} · version {route.version} · selected {route.selected_release_operation_id}</span></label>)}</fieldset>
      <label htmlFor={`${id}-target`}>Eligible target release</label><select id={`${id}-target`} className="h-10 w-full min-w-0 rounded-xl border bg-card px-2 text-sm" disabled={!canBegin} value={target} onChange={(event) => setTarget(event.target.value)}><option value="">Select an eligible release</option>{data.targets.map((item) => <option key={item.operation_id} value={item.operation_id}>{item.operation_id} · {item.kind} · {item.resolved_image_identity || "image identity unavailable"}{item.source_generation ? ` · source generation ${item.source_generation}` : ""}</option>)}</select>
      {data.targets.length === 0 ? <p>No target on this page. An empty continuation page does not erase earlier eligible targets.</p> : null}
      <div className="flex flex-wrap gap-2"><Button className={button} type="button" variant="outline" disabled={!after || busy || choices.isFetching} onClick={() => { setAfter(""); setTarget(""); setRoutes([]); }}>First eligible targets page</Button><Button className={button} type="button" variant="outline" disabled={!data.next_after_operation_id || busy || choices.isFetching || choices.isError} onClick={() => { setAfter(data.next_after_operation_id!); setTarget(""); setRoutes([]); }}>Next eligible targets page</Button></div>
      {writable ? <Button className={button} type="button" disabled={!canBegin || !target || routes.length === 0} onClick={review}>Review release switch</Button> : <p>Project maintainer access is required to switch releases.</p>}
    </div> : null}
    {intent ? <section aria-label="Review exact release switch" className="min-w-0 space-y-3 rounded-lg border p-3">
      <p>Reviewed loaded generation: {intent.body.expected_loaded_generation}</p><p className="break-all">Target: {intent.target.operation_id} · {intent.target.kind} · {intent.target.resolved_image_identity || "image identity unavailable"}</p>
      <ul className="space-y-2 break-all">{intent.routes.map((route) => <li key={route.id}>{route.hostname}{route.path_prefix} · {route.id} · version {route.version}: {route.selected_release_operation_id} → {intent.body.target_release_operation_id}</li>)}</ul>
      <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={confirmed} disabled={busy || conflict || attempted} onChange={(event) => setConfirmed(event.target.checked)} /><span>I confirm this exact routing change and understand it does not roll back application data.</span></label>
      <Button className={button} type="button" disabled={!confirmed || busy || conflict || !writable || !attempted && !canBegin} onClick={() => void submit()}>{busy ? "Requesting release switch..." : attempted ? "Retry identical release switch" : "Confirm release switch"}</Button>
      <p className="break-all text-xs">Frozen in-memory request key: {intent.body.idempotency_key}. It is retained only for this reviewed intent.</p>
      <Button className={button} type="button" variant="outline" disabled={busy || attempted && !conflict} onClick={() => void refreshReview()}>{conflict ? "Refresh choices for a new review" : "Discard review and refresh choices"}</Button>
    </section> : null}
    {error ? <p role="alert">{error}</p> : null}
  </section>;
}
