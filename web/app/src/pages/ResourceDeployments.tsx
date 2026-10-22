import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { allProjectMembersQuery, ApiError, principalQuery } from "@/api";
import { getResource, type Resource } from "@/api/hosting";
import { resourceBindingQuery } from "@/api/destinations";
import { cancelHostingOperation, HostingDeploymentError, hostingDeploymentKeys, hostingDeploymentsQuery, hostingOperationQuery, reconcileHostingOperation, requestHostingDeployment, type HostingDeploymentInput } from "@/api/hosting-deployments";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PageNavigation } from "@/components/common/PageNavigation";
import { maximumPageOffset } from "@/router/search";

const terminalStates = new Set(["succeeded", "failed", "canceled"]);
const stateText: Record<string, string> = {
  queued: "Queued", dispatched: "Dispatched to runner", preparing: "Preparing image",
  prepared: "Image prepared", applying: "Applying workload", verifying: "Verifying readiness",
  succeeded: "Succeeded", failed: "Failed", canceled: "Canceled",
  cancel_requested: "Cancellation requested after admission", reconciliation_required: "Reconciliation required",
};
function deploymentError(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 503) return "Deployment service unavailable. Delivery may be uncertain; retry the identical request when the service returns.";
    if (cause.status === 401 || cause.status === 403) return "Project permission is unavailable for this action. Refresh access before retrying.";
    if (cause.status === 409) return "The request conflicts with saved configuration, placement, an existing operation, or a previously used request key. Nothing was resubmitted automatically.";
    if (cause.status === 400) return "The server rejected the deployment settings. Review the ports and readiness path.";
    if (cause.status === 404) return "The requested application or operation is unavailable.";
  }
  return "The connection ended without a confirmed response. The request may have been accepted. Retry the identical request to recover its operation.";
}

function OperationDetail({ projectID, resourceID, operationID, writable, owner, blocked, onChanged }: {
  projectID: string; resourceID: string; operationID: string; writable: boolean; owner: boolean; blocked: boolean; onChanged: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const active = useRef(true);
  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);
  const detail = useQuery({ ...hostingOperationQuery(projectID, resourceID, operationID), refetchInterval: (query) => {
    const operation = query.state.data;
    return operation && (!terminalStates.has(operation.state) || operation.reconcile_requested || operation.mutation_hold) ? 5000 : false;
  } });
  const operation = detail.data;
  async function control(kind: "cancel" | "reconcile") {
    if (busy || blocked || detail.isError || !operation || (kind === "cancel" ? !writable : !owner)) return;
    setBusy(true); setError(""); setNotice("");
    try {
      if (kind === "cancel") await cancelHostingOperation(operationID);
      else await reconcileHostingOperation(operationID);
      if (!active.current) return;
      await detail.refetch(); await onChanged();
      if (active.current) setNotice(kind === "cancel" ? "Cancellation requested. Review the persisted operation state; this does not guarantee the workload stopped." : "Reconciliation requested. Observation does not authorize replaying a deployment.");
    } catch (cause) {
      if (!active.current) return;
      setError(deploymentError(cause));
      if (cause instanceof ApiError && cause.status === 409) { await detail.refetch(); await onChanged(); }
    } finally { if (active.current) setBusy(false); }
  }
  return <section className="min-w-0 space-y-3 rounded-lg border p-3" aria-label="Deployment operation detail">
    {detail.isPending ? <p role="status">Loading operation...</p> : null}
    {detail.isError ? <p role="alert">Unable to refresh operation. Its last recorded state may be stale; controls are paused.</p> : null}
    <Button type="button" variant="outline" disabled={detail.isFetching || busy} onClick={() => void detail.refetch()}>Refresh operation</Button>
    {operation ? <>
      <h6 className="font-medium">{stateText[operation.state] ?? operation.state}</h6>
      <dl className="space-y-1 break-all text-sm">
        <dt>Operation ID</dt><dd>{operation.id}</dd><dt>Recorded update</dt><dd>{operation.updated_at}</dd>
        <dt>Requested image</dt><dd>{operation.snapshot.image_reference}</dd><dt>Configuration generation</dt><dd>{operation.snapshot.resource_generation}</dd>
        <dt>Exact binding</dt><dd>{operation.binding_id}</dd><dt>Destination</dt><dd>{operation.snapshot.destination_id}</dd>
        <dt>Runner</dt><dd>{operation.snapshot.runner_id}</dd><dt>Runtime namespace</dt><dd>{operation.snapshot.runtime_namespace}</dd>
        <dt>Runner-local endpoint</dt><dd>127.0.0.1:{operation.snapshot.host_port} → container {operation.snapshot.container_port}</dd>
        <dt>Readiness request</dt><dd>{operation.snapshot.readiness.path} · {operation.snapshot.readiness.timeout_seconds}s timeout</dd>
        {operation.release ? <><dt>Resolved image</dt><dd>{operation.release.canonical_image}</dd><dt>Image ID</dt><dd>{operation.release.image_id}</dd></> : null}
        {operation.run_id ? <><dt>Deployment run</dt><dd>{operation.run_id}</dd></> : null}
        {operation.reconciliation_run_id ? <><dt>Observation run</dt><dd>{operation.reconciliation_run_id}</dd></> : null}
      </dl>
      {operation.mutation_hold ? <p role="status">Mutation hold retained. Another workload change must wait for the server to settle this operation.</p> : null}
      {operation.restore_ownership_held ? <p role="alert">Deployment admission blocked after platform restore. Trusted local recovery proof is pending. Reconciliation can observe the target but cannot release restored ownership; there is no administrator unlock action.</p> : null}
      {operation.state === "cancel_requested" || operation.state === "reconciliation_required" ? <p className="text-sm">Apply may have started. Cancellation is not proof of rollback or a stopped workload. An owner can request reconciliation.</p> : null}
      {operation.result ? <section className="space-y-1 text-sm" aria-label="Recorded deployment result">
        <h6 className="font-medium">Recorded deployment result</h6><p>Outcome: {operation.result.outcome}</p>
        <p>Readiness recorded at completion: {operation.result.ready ? "passed" : "not confirmed"}.</p>
        {operation.result.restored ? <p>The result records restoration of the previous workload.</p> : null}
        {operation.result.uncertain ? <p>The result is uncertain; do not assume the workload is running or stopped.</p> : null}
      </section> : null}
      <p className="text-xs text-muted-foreground">This is persisted operation evidence, not continuous health monitoring. Reconciliation requests a separate observation. The loopback port is local to the runner; no public URL or revision-specific HTTP identity is implied.</p>
      {writable && !terminalStates.has(operation.state) && !["cancel_requested", "reconciliation_required"].includes(operation.state) ? <Button type="button" variant="outline" disabled={blocked || busy || detail.isError} onClick={() => void control("cancel")}>Request cancellation</Button> : null}
      {owner ? <Button type="button" variant="outline" disabled={blocked || busy || detail.isError || operation.reconcile_requested} onClick={() => void control("reconcile")}>{operation.reconcile_requested ? "Reconciliation pending" : "Request reconciliation"}</Button> : <p className="text-xs text-muted-foreground">Project owner access is required to request reconciliation.</p>}
    </> : null}
    {error ? <p role="alert">{error}</p> : null}{notice ? <p role="status">{notice}</p> : null}
  </section>;
}

export function ResourceDeployments(props: { resource: Resource; writable: boolean; blocked: boolean }) {
  return <Deployments key={`${props.resource.project_id}:${props.resource.id}`} {...props} />;
}
function Deployments({ resource, writable, blocked }: { resource: Resource; writable: boolean; blocked: boolean }) {
  const id = useId();
  const client = useQueryClient();
  const active = useRef(true);
  const submitting = useRef(false);
  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);
  const principal = useQuery(principalQuery());
  const members = useQuery(allProjectMembersQuery());
  // Mirrors requireProjectRole: only system_admin bypasses persisted membership.
  const owner = Boolean(principal.data && (principal.data.roles.includes("system_admin") || members.data?.some((member) => member.project_id === resource.project_id && member.user_id === principal.data.id && member.role === "owner")));
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState("");
  const [containerPort, setContainerPort] = useState("80");
  const [hostPort, setHostPort] = useState("8080");
  const [path, setPath] = useState("/");
  const [timeout, setTimeout] = useState("30");
  const [acknowledged, setAcknowledged] = useState(false);
  const [busy, setBusy] = useState(false);
  const [attempt, setAttempt] = useState<HostingDeploymentInput>();
  const [conflict, setConflict] = useState(false);
  const [current, setCurrent] = useState<Resource>();
  const [reviewed, setReviewed] = useState<Resource>();
  const [error, setError] = useState("");
  const [refreshError, setRefreshError] = useState("");
  const [differentIntent, setDifferentIntent] = useState(false);
  const [restoreRejected, setRestoreRejected] = useState(false);
  const binding = useQuery(resourceBindingQuery(resource.project_id, resource.id));
  const history = useQuery(hostingDeploymentsQuery(resource.project_id, resource.id, offset));
  const base = reviewed ?? resource;
  const restoreHeld = restoreRejected || Boolean(history.data?.items.some((operation) => operation.restore_ownership_held));
  const mutationHeld = Boolean(history.data?.items.some((operation) => operation.mutation_hold));
  const failed = binding.isError || history.isError || principal.isError || members.isError;
  const unavailable = blocked || failed || binding.isPending || history.isPending || principal.isPending || members.isPending;
  const canWrite = writable && !unavailable && !mutationHeld;
  const refreshHistory = async () => { await client.invalidateQueries({ queryKey: hostingDeploymentKeys.history(resource.project_id, resource.id) }); };
  async function reviewCurrent() {
    setRefreshError(""); setCurrent(undefined);
    try {
      const latest = await getResource(resource.id);
      if (!active.current) return;
      if (latest.project_id !== resource.project_id || latest.id !== resource.id) throw new Error("Resource scope mismatch");
      setCurrent(latest);
      await binding.refetch(); await refreshHistory();
    } catch { if (active.current) setRefreshError("Unable to refresh current configuration. Keep this request and retry the refresh."); }
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!canWrite || submitting.current || !binding.data || conflict || restoreHeld || !acknowledged) return;
    submitting.current = true;
    const input = attempt ?? { expected_generation: base.generation, idempotency_key: crypto.randomUUID(), container_port: Number(containerPort), host_port: Number(hostPort), readiness: { path, timeout_seconds: Number(timeout) } };
    setAttempt(input); setBusy(true); setError("");
    try {
      const operation = await requestHostingDeployment(resource.id, input);
      if (!active.current) return;
      if (operation.project_id !== resource.project_id || operation.resource_id !== resource.id || operation.binding_id !== binding.data.id) throw new Error("Operation scope mismatch");
      setSelected(operation.id); setAttempt(undefined); setAcknowledged(false); setDifferentIntent(false); setOffset(0);
      await refreshHistory();
    } catch (cause) {
      if (!active.current) return;
      if (cause instanceof HostingDeploymentError && cause.code === "hosting_restore_held") {
        setRestoreRejected(true); setError("");
      } else {
        setError(deploymentError(cause));
        if (cause instanceof ApiError && cause.status === 409) { setConflict(true); await reviewCurrent(); }
      }
    } finally { submitting.current = false; if (active.current) setBusy(false); }
  }
  function newIntent(next?: Resource) {
    if (next) setReviewed(next);
    setAttempt(undefined); setConflict(false); setCurrent(undefined); setAcknowledged(false); setDifferentIntent(false); setError("");
  }
  return <section className="min-w-0 space-y-3 border-t pt-3" aria-label={`Image deployments for ${resource.name}`}>
    <h5 className="font-medium">Image deployments</h5>
    {restoreHeld ? <p role="alert">Deployment admission blocked after platform restore. Recovery proof is pending; refreshing configuration, retrying, or requesting reconciliation cannot release restored ownership. Your settings are preserved.</p> : null}
    {mutationHeld ? <p role="status">Deployment requests are paused while a recorded mutation hold is retained. Refresh history to review settlement; cancellation alone does not release the hold.</p> : null}
    {failed ? <div className="space-y-2"><p role="alert">Unable to refresh deployment information. Draft settings are preserved; actions are paused.</p><Button type="button" variant="outline" onClick={() => { void binding.refetch(); void history.refetch(); if (principal.isError) void principal.refetch(); if (members.isError) void members.refetch(); }}>Retry deployment information</Button></div> : null}
    {history.isPending ? <p role="status">Loading deployment history...</p> : null}
    <Button type="button" variant="outline" disabled={history.isFetching || busy} onClick={() => void history.refetch()}>Refresh deployment history</Button>
    {history.data?.items.length === 0 ? <p className="text-sm">No deployment operations recorded.</p> : null}
    <ol className="space-y-2" aria-label="Deployment history">{history.data?.items.map((operation) => <li key={operation.id} className="min-w-0 rounded-lg border p-2"><p className="break-all text-sm">{stateText[operation.state] ?? operation.state} · {operation.created_at}</p><p className="break-all text-xs">{operation.snapshot.image_reference} · generation {operation.snapshot.resource_generation}</p><Button type="button" variant="outline" className="h-auto max-w-full whitespace-normal break-all" onClick={() => setSelected(operation.id)}>View operation {operation.id}</Button></li>)}</ol>
    <PageNavigation label={`deployments for ${resource.name}`} page={history.data} loading={history.isFetching || busy} onFirst={() => setOffset(0)} onPrevious={() => setOffset(Math.max(0, offset - 20))} onNext={() => setOffset(Math.min(maximumPageOffset, offset + 20))} />
    {selected ? <OperationDetail key={selected} projectID={resource.project_id} resourceID={resource.id} operationID={selected} writable={writable} owner={owner} blocked={unavailable} onChanged={refreshHistory} /> : null}
    {writable ? <form className="space-y-3 rounded-lg border p-3" aria-label={`Deploy image for ${resource.name}`} onSubmit={(event) => void submit(event)}>
      <p className="break-all text-sm">Deploy saved image {base.source.image.reference} at generation {base.generation} to binding {binding.data?.id ?? "unavailable"}.</p>
      {!binding.isPending && !binding.isError && !binding.data ? <p role="status">Save a desired destination before requesting deployment.</p> : null}
      <div className="grid min-w-0 gap-3 sm:grid-cols-2">
        <label className="space-y-1" htmlFor={`${id}-container`}>Container port<Input id={`${id}-container`} type="number" min={1} max={65535} required value={containerPort} disabled={busy || Boolean(attempt)} onChange={(event) => setContainerPort(event.target.value)} /></label>
        <label className="space-y-1" htmlFor={`${id}-host`}>Runner-local host port<Input id={`${id}-host`} type="number" min={1024} max={65535} required value={hostPort} disabled={busy || Boolean(attempt)} onChange={(event) => setHostPort(event.target.value)} /></label>
        <label className="space-y-1" htmlFor={`${id}-path`}>Readiness path<Input id={`${id}-path`} required maxLength={1024} value={path} disabled={busy || Boolean(attempt)} onChange={(event) => setPath(event.target.value)} /></label>
        <label className="space-y-1" htmlFor={`${id}-timeout`}>Readiness timeout in seconds<Input id={`${id}-timeout`} type="number" min={1} max={120} required value={timeout} disabled={busy || Boolean(attempt)} onChange={(event) => setTimeout(event.target.value)} /></label>
      </div>
      <p className="text-xs text-muted-foreground">The readiness path must be relative to the local origin, such as /health. This is a generic HTTP check without revision-specific response headers. Port 127.0.0.1:{hostPort} is local to the runner, not a public application URL.</p>
      <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={acknowledged} disabled={busy} onChange={(event) => setAcknowledged(event.target.checked)} /><span>I understand that replacing a workload on this fixed host port interrupts service while the old container stops and the new container is verified.</span></label>
      <Button type="submit" className="h-auto whitespace-normal" disabled={!canWrite || busy || !binding.data || !acknowledged || conflict || restoreHeld}>{restoreHeld ? "Deployment blocked after restore" : mutationHeld ? "Deployment waiting for settlement" : busy ? "Requesting deployment..." : attempt ? "Retry identical deployment request" : "Request image deployment"}</Button>
      {attempt ? <p className="break-all text-xs">Request key: {attempt.idempotency_key}. Submitted settings and generation {attempt.expected_generation} are retained{restoreHeld ? ". Deployment admission remains blocked." : " for an identical retry."}</p> : null}
      {error ? <p role="alert">{error}</p> : null}
      {attempt && !restoreHeld ? <section className="space-y-2" aria-label="Review deployment request">
        <Button type="button" variant="outline" disabled={busy} onClick={() => void reviewCurrent()}>Refresh configuration and history</Button>
        {current ? <p className="break-all text-sm">Current saved configuration: generation {current.generation} · {current.source.image.reference}.</p> : null}
        {refreshError ? <p role="alert">{refreshError}</p> : null}
        <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={differentIntent} disabled={busy} onChange={(event) => setDifferentIntent(event.target.checked)} /><span>I reviewed history and understand the earlier request may already exist. I intend a separate request with the current configuration and a new request key.</span></label>
        <Button type="button" variant="outline" className="h-auto whitespace-normal" disabled={busy || !current || !differentIntent || unavailable || mutationHeld} onClick={() => newIntent(current)}>Use reviewed configuration for a new request</Button>
        <p className="text-xs text-muted-foreground">This unlocks settings and requires a new interruption acknowledgment. It never deploys automatically.</p>
      </section> : null}
    </form> : <p className="text-xs text-muted-foreground">Project maintainer access is required to request or cancel deployments.</p>}
  </section>;
}
