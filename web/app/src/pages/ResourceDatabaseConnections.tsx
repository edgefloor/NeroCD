import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { allProjectMembersQuery, ApiError, principalQuery, queryKeys, type Principal, type ProjectMember } from "@/api";
import { getResource, type Resource } from "@/api/hosting";
import { getHostingRuntimeConfiguration } from "@/api/hosting-runtime-config";
import { getResourceBinding } from "@/api/destinations";
import { databaseRequestID, boundedDatabaseRequest, databaseDenied } from "@/api/hosting-databases";
import { connectDatabase, connectionError, connectionKey, connectionOptionsQuery, connectionQuery, deployConnectedApplication, maintainConnectedDatabase, type ConnectedDeploymentInput, type DatabaseConnectionInput, type DatabaseConnectionScope, type DatabaseMaintenanceInput } from "@/api/hosting-database-connections";
import { canOwnProject } from "@/lib/permissions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ResourceDeployments } from "./ResourceDeployments";

const states: Record<string, string> = { binding_saved: "Connection saved", capture_requested: "Network observation requested", capture_waiting: "Waiting for network observation", upgrade_requested: "Database network update requested", upgrade_waiting: "Waiting for database network update", application_requested: "Application deployment requested", active: "Connection workflow completed", held: "Connection requires review" };
export function ResourceDatabaseConnections({ resource, writable, blocked }: { resource: Resource; writable: boolean; blocked: boolean }) {
  const identity = useQuery(principalQuery());
  const members = useQuery({ ...allProjectMembersQuery(), staleTime: 10_000 });
  const [denied, setDenied] = useState("");
  const actor = identity.data ? JSON.stringify([identity.data.provider, identity.data.id]) : "";
  const scopeID = JSON.stringify([actor, resource.project_id, resource.id]);
  if (!identity.data || !members.data) return <p role="status">Database connection access is pending or unavailable. Refresh project access to continue.</p>;
  if (denied === scopeID || databaseDenied(identity.error) || databaseDenied(members.error)) return <p role="alert">Database connection permission is unavailable. Reload to verify access. Retained requests and drafts have been cleared.</p>;
  const owner = writable && canOwnProject(identity.data, members.data ?? [], resource.project_id);
  return <ConnectionSession key={JSON.stringify([scopeID, owner])} resource={resource} owner={owner} legacyWritable={writable} accessPaused={blocked || identity.isFetching || identity.isError || members.isFetching || members.isError} scope={{ actor, project: resource.project_id, resource: resource.id }} onDenied={() => setDenied(scopeID)} />;
}
function ConnectionSession({ resource, owner, legacyWritable, accessPaused, scope, onDenied }: { resource: Resource; owner: boolean; legacyWritable: boolean; accessPaused: boolean; scope: DatabaseConnectionScope; onDenied: () => void }) {
  const client = useQueryClient();
  const id = useId();
  const active = useRef(true);
  const pending = useRef<AbortController | null>(null);
  const submitting = useRef(false);
  useEffect(() => { active.current = true; return () => { active.current = false; pending.current?.abort(); }; }, []);
  const status = useQuery({ ...connectionQuery(scope), enabled: resource.source.kind === "image", refetchInterval: (query) => {
    const value = query.state.data;
    return !query.state.error && value && Object.hasOwn(states, value.state) && (value.state !== "active" || Boolean(value.hold_operation_id)) && value.state !== "held" && query.state.dataUpdateCount < 24 ? 5000 : false;
  } });
  const options = useQuery({ ...connectionOptionsQuery(scope), enabled: resource.source.kind === "image" && status.data === null && !status.isError });
  const [database, setDatabase] = useState("");
  const [port, setPort] = useState("8080");
  const [readiness, setReadiness] = useState("/");
  const [timeout, setTimeoutValue] = useState("30");
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [conflict, setConflict] = useState(false);
  type Attempt = { kind: "connect"; body: DatabaseConnectionInput } | { kind: "deploy"; body: ConnectedDeploymentInput } | { kind: "maintain"; database: string; body: DatabaseMaintenanceInput };
  const [attempt, setAttempt] = useState<Attempt>();
  const [reviewed, setReviewed] = useState(false);
  const [reviewResource, setReviewResource] = useState<Resource>();
  const [reviewBase, setReviewBase] = useState<Resource>();
  const connection = status.data;
  const known = Boolean(connection && Object.hasOwn(states, connection.state));
  const selected = options.data?.items.find((item) => item.resource_id === database);
  const paused = accessPaused || busy || status.isPending || status.isError || status.isFetching || (connection && !known);
  const denied = databaseDenied(status.error) || databaseDenied(options.error);
  useEffect(() => { if (denied) onDenied(); }, [denied, onDenied]);
  function currentAuthority() {
    const principal = client.getQueryData<Principal>(queryKeys.principal);
    const membership = client.getQueryData<ProjectMember[]>(queryKeys.allProjectMembers);
    const identityState = client.getQueryState(queryKeys.principal);
    const membershipState = client.getQueryState(queryKeys.allProjectMembers);
    return active.current && owner && principal && JSON.stringify([principal.provider, principal.id]) === scope.actor && membership && canOwnProject(principal, membership, scope.project) && identityState?.status === "success" && membershipState?.status === "success" && identityState.fetchStatus !== "fetching" && membershipState.fetchStatus !== "fetching" && !databaseDenied(client.getQueryState(connectionKey(scope))?.error) && !databaseDenied(client.getQueryState(connectionOptionsQuery(scope).queryKey)?.error);
  }
  async function settleAuthority() {
    // Join existing revalidation without starting another authority read. Query
    // cache updates are synchronous; React observer notifications may be deferred.
    const reads: Promise<unknown>[] = [];
    if (client.getQueryState(queryKeys.principal)?.fetchStatus === "fetching") reads.push(client.fetchQuery({ ...principalQuery(), staleTime: 0 }));
    if (client.getQueryState(queryKeys.allProjectMembers)?.fetchStatus === "fetching") reads.push(client.fetchQuery({ ...allProjectMembersQuery(), staleTime: 0 }));
    try { await boundedDatabaseRequest(() => Promise.all(reads), pending.current?.signal); } catch { return false; }
    return Boolean(currentAuthority());
  }
  async function refresh() {
    const latest = await status.refetch();
    if (databaseDenied(latest.error)) { onDenied(); throw latest.error; }
    if (latest.error) throw latest.error;
    if (latest.data === null) {
      const choices = await options.refetch();
      if (databaseDenied(choices.error)) { onDenied(); throw choices.error; }
      if (choices.error) throw choices.error;
    }
  }
  async function requestAction(kind: Attempt["kind"], event?: FormEvent) {
    event?.preventDefault();
    if (!currentAuthority() || submitting.current || paused || conflict || !confirmed || (attempt && attempt.kind !== kind)) return;
    if (!attempt && (kind === "connect" ? connection !== null || !selected || selected.status !== "available" || selected.reason_code !== "eligible" || options.isError || options.isFetching : !connection || connection.state !== "active" || Boolean(connection.hold_operation_id))) return;
    const controller = new AbortController(); pending.current = controller;
    submitting.current = true; setBusy(true); setError(""); setNotice("");
    let input = attempt;
    try {
      if (!input) {
        const latest = await boundedDatabaseRequest((signal) => getResource(kind === "maintain" ? connection!.database_resource_id : resource.id, { signal }), controller.signal);
        if (!active.current || controller.signal.aborted) return;
        if (latest.project_id !== scope.project || latest.id !== (kind === "maintain" ? connection!.database_resource_id : resource.id) || latest.kind !== (kind === "maintain" ? "database" : "application")) throw new Error("Resource scope mismatch");
        if (kind !== "maintain" && (latest.source.kind !== "image" || latest.generation !== (reviewBase ?? resource).generation)) throw new ApiError(409, "Application configuration changed", null);
        if (kind === "maintain") {
          input = { kind, database: connection!.database_resource_id, body: { expected_generation: latest.generation, expected_membership_revision: connection!.membership_revision, idempotency_key: databaseRequestID() } };
        } else {
          const { runtime_configuration: runtime } = await boundedDatabaseRequest((signal) => getHostingRuntimeConfiguration(resource.id, { signal }), controller.signal);
          if (!active.current || controller.signal.aborted) return;
          if (runtime.resource_id !== resource.id || runtime.project_id !== scope.project || runtime.resource_generation !== latest.generation || !Number.isSafeInteger(runtime.runtime_generation) || runtime.runtime_generation < 0) throw new Error("Runtime configuration scope mismatch");
          const deployment: ConnectedDeploymentInput = { expected_generation: latest.generation, expected_runtime_configuration_generation: runtime.runtime_generation, idempotency_key: databaseRequestID(), container_port: Number(port), readiness: { path: readiness, timeout_seconds: Number(timeout) } };
          if (kind === "connect") {
            const currentOptions = await options.refetch();
            if (currentOptions.error) throw currentOptions.error;
            const choice = currentOptions.data?.items.find((item) => item.resource_id === database);
            if (!choice || choice.status !== "available" || choice.reason_code !== "eligible" || choice.generation !== selected?.generation || choice.destination_id !== selected?.destination_id) { setConflict(true); throw new ApiError(409, "Selection changed", null); }
            const binding = await boundedDatabaseRequest((signal) => getResourceBinding(resource.id, { signal }), controller.signal);
            if (!active.current || controller.signal.aborted) return;
            if (binding.resource_id !== resource.id || binding.destination_id !== choice.destination_id) throw new Error("Destination scope mismatch");
            input = { kind, body: { binding: { database_resource_id: choice.resource_id, expected_database_generation: choice.generation, expected_resource_generation: latest.generation, expected_runtime_generation: runtime.runtime_generation, request_id: databaseRequestID() }, deployment } };
          } else input = { kind, body: deployment };
        }
        if (!active.current || controller.signal.aborted) return;
        setAttempt(input);
      }
      if (!await settleAuthority() || controller.signal.aborted) return;
      const operation = input.kind === "connect" ? await connectDatabase(scope, input.body, controller.signal) : input.kind === "deploy" ? await deployConnectedApplication(scope, input.body, controller.signal) : await maintainConnectedDatabase(scope, input.database, input.body, controller.signal);
      if (!active.current || controller.signal.aborted) return;
      if (!await settleAuthority() || controller.signal.aborted) return;
      setNotice(`Request accepted: ${operation.id}. Review recorded progress; acceptance does not confirm runtime health.`);
      setAttempt(undefined); setConfirmed(false);
      if (input.kind === "connect") client.setQueryData(connectionKey(scope), operation);
      await refresh();
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (databaseDenied(cause)) { onDenied(); return; }
      if (!await settleAuthority() || controller.signal.aborted) return;
      setError(!input && !(cause instanceof ApiError) ? "Unable to verify current configuration. No new request was sent. Refresh before retrying." : connectionError(cause));
      if (cause instanceof ApiError && cause.status === 400) { setAttempt(undefined); setConfirmed(false); }
      if (cause instanceof ApiError && cause.status === 409) { setConflict(true); setReviewed(false); }
    } finally { submitting.current = false; if (pending.current === controller) pending.current = null; if (active.current) setBusy(false); }
  }
  async function review() {
    if (busy || accessPaused) return;
    setBusy(true); setError("");
    try {
      await refresh();
      const latest = await boundedDatabaseRequest((signal) => getResource(resource.id, { signal }));
      if (latest.id !== resource.id || latest.project_id !== scope.project) throw new Error("Review scope mismatch");
      if (active.current) { setReviewResource(latest); setReviewed(true); }
    }
    catch (cause) {
      if (!active.current) return;
      if (databaseDenied(cause)) { onDenied(); return; }
      setError(connectionError(cause));
    }
    finally { if (active.current) setBusy(false); }
  }
  if (resource.source.kind !== "image") return <><p>Database connections require a public container image. Source builds are unsupported.</p><ResourceDeployments resource={resource} writable={legacyWritable} blocked={accessPaused} /></>;
  if (denied) return <p role="alert">Database connection access is unavailable.</p>;
  const editingLocked = accessPaused || busy || Boolean(attempt);
  const canConnect = !connection && !options.isError && !options.isPending && selected?.status === "available" && selected.reason_code === "eligible";
  return <section className="min-w-0 space-y-3 border-t pt-3" aria-label={`Database connection for ${resource.name}`}>
    <h5 className="font-medium">Database connection</h5>
    {accessPaused ? <p role="status">Project access is being verified or is temporarily unavailable. Changes are paused; retained requests and draft settings are preserved.</p> : null}
    {status.isPending ? <p role="status">Checking saved database connection...</p> : null}
    {status.isError ? <p role="alert">Unable to verify the database connection. Deployment controls are paused. {connectionError(status.error)}</p> : null}
    <Button type="button" variant="outline" className="h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words" disabled={accessPaused || busy || status.isFetching} onClick={() => void review()}>Refresh database connection</Button>
    {connection ? <>
      <p role="status">{states[connection.state] ?? "Unrecognized connection status. Actions are paused."}</p>
      <dl className="space-y-1 break-all text-sm"><dt>Application</dt><dd>{connection.application_resource_id}</dd><dt>Database</dt><dd>{connection.database_resource_id}</dd><dt>Membership revision</dt><dd>{connection.membership_revision}</dd><dt>Recorded reason</dt><dd>{connection.reason_code || "None recorded"}</dd><dt>Network observation</dt><dd>{connection.capture_id || "Not recorded"}</dd><dt>Database update operation</dt><dd>{connection.upgrade_operation_id || "Not recorded"}</dd><dt>Application operation</dt><dd>{connection.application_operation_id || "Not recorded"}</dd><dt>Recorded update</dt><dd>{connection.updated_at}</dd></dl>
      {connection.state === "active" ? <p className="text-sm">The connection workflow completed. This recorded result is not continuous health monitoring.</p> : null}
      {connection.hold_operation_id || connection.state === "held" ? <div role="status" className="space-y-2 break-all"><p>Changes are paused for operation {connection.hold_operation_id || "awaiting recorded identity"}. The hold remains in place.</p><p>Observation is unavailable here until the server provides the original operation, stage and candidate evidence. No unlock or replay is requested.</p></div> : null}
    </> : status.data === null ? <>
      <p>Connect one undeployed public-image application to a healthy PostgreSQL or Redis database on the same destination. The server checks eligibility and adds the named PG* or REDIS* credential references. Credential values are never displayed.</p>
      <p className="text-xs text-muted-foreground">One application per database. Source builds, application storage, route continuity and an already running unbound application are unsupported. The controller observes the database network, updates it and deploys the application.</p>
      {options.isPending ? <p role="status">Loading eligible databases...</p> : null}
      {options.isError ? <p role="alert">Eligible databases are unavailable. {connectionError(options.error)}</p> : null}
      {options.data?.items.length === 0 ? <p>No eligible databases were returned. Check database health, placement and application prerequisites.</p> : null}
      {owner ? <div className="space-y-1"><label htmlFor={`${id}-database`}>Database to connect</label><select id={`${id}-database`} className="h-10 w-full min-w-0 rounded-xl border bg-card px-2 text-sm" value={database} disabled={editingLocked || options.isError || options.isPending} onChange={(event) => { setDatabase(event.target.value); setConfirmed(false); }}><option value="">Select an eligible database</option>{options.data?.items.map((item) => <option key={item.resource_id} value={item.resource_id} disabled={item.status !== "available" || item.reason_code !== "eligible"}>{item.name} · {item.engine} · {item.status === "available" && item.reason_code === "eligible" ? "Available" : "Unavailable"}</option>)}</select></div> : null}
    </> : null}
    {owner && !status.isPending && !status.isError ? <form className="min-w-0 space-y-3 rounded-lg border p-3" aria-label="Review database connection request" onSubmit={(event) => void requestAction(attempt?.kind ?? (connection ? "deploy" : "connect"), event)}>
      <div className="grid min-w-0 gap-3 sm:grid-cols-2"><label htmlFor={`${id}-port`}>Application container port<Input id={`${id}-port`} type="number" min={1} max={65535} value={port} disabled={editingLocked} required onChange={(event) => { setPort(event.target.value); setConfirmed(false); }} /></label><label htmlFor={`${id}-path`}>Application readiness path<Input id={`${id}-path`} maxLength={1024} value={readiness} required disabled={editingLocked} onChange={(event) => { setReadiness(event.target.value); setConfirmed(false); }} /></label><label htmlFor={`${id}-timeout`}>Readiness timeout in seconds<Input id={`${id}-timeout`} type="number" min={1} max={120} value={timeout} disabled={editingLocked} required onChange={(event) => { setTimeoutValue(event.target.value); setConfirmed(false); }} /></label></div>
      <p className="text-xs text-muted-foreground">The readiness path is local, such as /health. The server allocates a separate runner-local application port. These actions do not promise public traffic or uninterrupted service. Database redeployment can interrupt database access.</p>
      <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={confirmed} disabled={accessPaused || busy} onChange={(event) => setConfirmed(event.target.checked)} /><span>I reviewed the selected database and application settings and approve this request.</span></label>
      <div className="flex flex-wrap gap-2"><Button type="submit" className="h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words" disabled={Boolean(paused) || conflict || !confirmed || (attempt ? attempt.kind === "maintain" : connection ? connection.state !== "active" || Boolean(connection.hold_operation_id) : !canConnect)}>{attempt?.kind === "connect" ? "Retry identical connection request" : attempt?.kind === "deploy" ? "Retry identical application request" : connection ? "Redeploy connected application" : "Connect database and deploy application"}</Button>
      {connection ? <Button type="button" variant="outline" className="h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words" disabled={Boolean(paused) || conflict || !confirmed || (attempt ? attempt.kind !== "maintain" : connection.state !== "active" || connection.membership_revision < 1 || Boolean(connection.hold_operation_id))} onClick={() => void requestAction("maintain")}>{attempt?.kind === "maintain" ? "Retry identical database request" : "Redeploy connected database"}</Button> : null}</div>
    </form> : <p className="text-xs text-muted-foreground">Project owner access is required for database connection changes.</p>}
    {attempt ? <p className="break-all text-xs">Retained request: {attempt.kind === "connect" ? attempt.body.binding.request_id : attempt.body.idempotency_key}. Settings stay frozen for an identical retry.</p> : null}
    {conflict ? <section className="space-y-2" aria-label="Review changed connection"><p role="status">Nothing was resubmitted. Refresh the recorded connection, then explicitly approve a separate request.</p><Button type="button" variant="outline" className="h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words" disabled={busy || accessPaused} onClick={() => void review()}>Refresh for conflict review</Button><Button type="button" variant="outline" className="h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words" disabled={!reviewed || busy || accessPaused || status.isError} onClick={() => { setReviewBase(reviewResource); setConflict(false); setAttempt(undefined); setConfirmed(false); setReviewed(false); setError(""); }}>Use refreshed configuration for a new request</Button></section> : null}
    {error ? <p role="alert">{error}</p> : null}{notice ? <p role="status" className="break-all">{notice}</p> : null}
    {status.data === null && !status.isError && !attempt && !busy && !conflict ? <ResourceDeployments resource={resource} writable={legacyWritable} blocked={accessPaused} /> : null}
  </section>;
}
