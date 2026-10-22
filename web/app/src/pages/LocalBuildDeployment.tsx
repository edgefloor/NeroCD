import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "@/api";
import { getResource, type Resource } from "@/api/hosting";
import { getHostingBuild, checkBuildScope, type HostingBuild } from "@/api/hosting-builds";
import { HostingDeploymentError, hostingBindingQuery, hostingDeploymentKeys, hostingDeploymentsQuery, requestHostingBuildDeployment, type HostingBuildDeploymentInput } from "@/api/hosting-deployments";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { OperationDetail } from "./ResourceDeployments";

// The parent keys this component by actor, resource, permission and exact build.
// Keep a submitted body intact until its response is known or the operator leaves.
export function LocalBuildDeployment({ actor, resource, build, writable, blocked, onDenied }: {
  actor: string; resource: Resource; build: HostingBuild; writable: boolean; blocked: boolean; onDenied: () => void;
}) {
  const id = useId(), client = useQueryClient();
  const [base, setBase] = useState(resource);
  const [containerPort, setContainerPort] = useState("80"), [hostPort, setHostPort] = useState("8080");
  const [mode, setMode] = useState("automatic"), [path, setPath] = useState("/"), [timeout, setTimeout] = useState("30");
  const [confirmed, setConfirmed] = useState(false), [busy, setBusy] = useState(false);
  const [attempt, setAttempt] = useState<HostingBuildDeploymentInput>();
  const [conflict, setConflict] = useState(false), [restoreHeld, setRestoreHeld] = useState(false), [denied, setDenied] = useState(false);
  const [error, setError] = useState(""), [selected, setSelected] = useState("");
  const active = useRef(true), submitting = useRef(false);
  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);
  const binding = useQuery(hostingBindingQuery(resource.project_id, resource.id, actor));
  const history = useQuery(hostingDeploymentsQuery(resource.project_id, resource.id, 0, actor));
  const changed = async () => { await Promise.all([
    client.invalidateQueries({ queryKey: hostingDeploymentKeys.history(resource.project_id, resource.id) }),
    client.invalidateQueries({ queryKey: hostingDeploymentKeys.retained(resource.project_id, resource.id) }),
  ]); };
  const source = base.source.kind === "git" ? base.source.git : undefined;
  const currentSource = resource.source.kind === "git" ? resource.source.git : undefined;
  const exactBuild = build.project_id === resource.project_id && build.resource_id === resource.id && build.state === "succeeded" && Boolean(build.artifact) && source?.credential_kind === "public" && build.snapshot.source.source_id === source.source_id && build.snapshot.source.generation === source.generation;
  const changedSource = resource.generation > base.generation || (currentSource?.generation ?? 0) > (source?.generation ?? 0) || currentSource?.source_id !== source?.source_id;
  const placementMatches = binding.data?.id === build.snapshot.target.binding_id && binding.data?.destination_id === build.snapshot.target.destination_id;
  const held = Boolean(history.data?.items.some((operation) => operation.mutation_hold));
  const restored = restoreHeld || Boolean(history.data?.items.some((operation) => operation.restore_ownership_held));
  const paused = blocked || denied || binding.isError || binding.isPending || history.isError || history.isPending || restored || conflict;
  const disabled = paused || busy || !confirmed || !writable || (!attempt && (!exactBuild || changedSource || !placementMatches || held));
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (disabled || submitting.current || !source || !binding.data) return;
    const body = attempt ?? { build_id: build.id, expected_generation: base.generation, expected_source_generation: source.generation, idempotency_key: crypto.randomUUID(), container_port: Number(containerPort), ...(mode === "fixed" ? { host_port: Number(hostPort) } : {}), readiness: { path, timeout_seconds: Number(timeout) } };
    setAttempt(body); setBusy(true); submitting.current = true; setError("");
    try {
      const operation = await requestHostingBuildDeployment(resource.id, body);
      if (!active.current) return;
      if (operation.project_id !== resource.project_id || operation.resource_id !== resource.id || operation.binding_id !== build.snapshot.target.binding_id || operation.snapshot.local_build?.artifact.build_id !== body.build_id || operation.snapshot.local_build.artifact.image_id !== build.artifact?.image_id || operation.snapshot.resource_generation !== body.expected_generation || operation.snapshot.local_build.source_generation !== body.expected_source_generation) throw new Error("Deployment response does not match selected build");
      setSelected(operation.id); setAttempt(undefined); setConfirmed(false); await changed();
    } catch (cause) {
      if (!active.current) return;
      if (cause instanceof HostingDeploymentError && cause.code === "hosting_restore_held") { setRestoreHeld(true); setError(""); }
      else if (cause instanceof ApiError && [401, 403].includes(cause.status)) { setDenied(true); onDenied(); setError("Deployment permission denied. Reload to refresh project access before another action."); }
      else if (cause instanceof ApiError && cause.status === 409) { setConflict(true); setError("Deployment conflicts with current resource/source generations, exact artifact, placement, runner capability, or a retained request key. Refresh the resource and build history, review the exact successful build, and explicitly select it again. No generation, build, or request was changed automatically."); }
      else if (cause instanceof ApiError && cause.status === 400) { setConflict(true); setError("The server rejected these ports or readiness settings. Reopen this build to review a separate request."); }
      else setError("Deployment delivery is unconfirmed. The operation may already exist. Retry the identical request with its retained build, generations, settings, and key.");
    } finally { submitting.current = false; if (active.current) setBusy(false); }
  }
  async function reviewSeparateRequest() {
    if (busy || denied || restored) return;
    setBusy(true);
    try {
      const [latest, exact] = await Promise.all([getResource(resource.id), getHostingBuild(build.id)]);
      if (!active.current) return;
      checkBuildScope(exact, resource.project_id, resource.id, build.id);
      if (latest.id !== resource.id || latest.project_id !== resource.project_id || latest.source.kind !== "git" || latest.source.git.generation !== exact.snapshot.source.generation || latest.source.git.source_id !== exact.snapshot.source.source_id || exact.state !== "succeeded" || JSON.stringify(exact.artifact) !== JSON.stringify(build.artifact)) throw new Error("Build no longer matches current source");
      await binding.refetch(); await changed();
      if (!active.current) return;
      setBase(latest); setAttempt(undefined); setConflict(false); setConfirmed(false); setError("");
    } catch (cause) {
      if (!active.current) return;
      if (cause instanceof ApiError && [401, 403].includes(cause.status)) { setDenied(true); onDenied(); setError("Deployment permission denied. Reload to refresh project access before another action."); }
      else setError("Unable to review this exact artifact against current resource and source generations. Keep the retained request, refresh the resource, and select a current successful build explicitly.");
    }
    finally { if (active.current) setBusy(false); }
  }
  return <section className="min-w-0 space-y-3 border-t pt-3" aria-label="Deploy recorded local build">
    <h6 className="font-medium">Deploy recorded local build</h6>
    <p className="break-all text-sm">Build {build.id} · source generation {source?.generation} · resource generation {base.generation} · image ID {build.artifact?.image_id}.</p>
    <p className="text-xs text-muted-foreground">Requires the same saved destination, runner and Docker daemon as this build, a current project grant and enrolled runner with local_build_deploy_v1, and an operator-enabled agent started with --local-build-deploy. The agent uses the recorded local image ID without fetching or transferring it.</p>
    {restored ? <p role="alert">Deployment blocked after platform restore. Trusted recovery proof is pending. Refreshing, retrying, or reconciliation cannot release restored ownership. Settings are preserved.</p> : null}
    {!exactBuild || changedSource ? <p role="status">This build does not match the reviewed current public Git source. Refresh the resource and explicitly select a successful build for its current source generation.</p> : null}
    {!binding.isPending && !placementMatches ? <p role="status">Restore the build's exact saved destination before deploying. A local artifact cannot move to another host.</p> : null}
    {held ? <p role="status">A recorded mutation hold pauses new deployment requests. A retained identical request can recover its existing operation.</p> : null}
    {binding.isError || history.isError ? <p role="alert">Unable to refresh deployment placement or history. Requests are paused and settings are preserved.</p> : null}
    <Button type="button" variant="outline" className="h-auto max-w-full whitespace-normal" disabled={busy} onClick={() => { void binding.refetch(); void history.refetch(); }}>Refresh local deployment prerequisites</Button>
    {writable ? <form className="min-w-0 space-y-3" aria-label="Deploy selected build" onSubmit={(event) => void submit(event)}>
      <div className="grid min-w-0 gap-3 sm:grid-cols-2">
        <label htmlFor={`${id}-container`}>Build container port<Input id={`${id}-container`} type="number" min={1} max={65535} required value={containerPort} disabled={busy || Boolean(attempt)} onChange={(e) => { setContainerPort(e.target.value); setConfirmed(false); }} /></label>
        <label htmlFor={`${id}-mode`}>Build host port allocation<select id={`${id}-mode`} className="h-10 w-full min-w-0 rounded-xl border bg-card px-2 text-sm" value={mode} disabled={busy || Boolean(attempt)} onChange={(e) => { setMode(e.target.value); setConfirmed(false); }}><option value="automatic">Automatic per-release port</option><option value="fixed">Advanced: explicit fixed port</option></select></label>
        {mode === "fixed" ? <label htmlFor={`${id}-host`}>Build runner-local host port<Input id={`${id}-host`} type="number" min={1024} max={65535} required value={hostPort} disabled={busy || Boolean(attempt)} onChange={(e) => { setHostPort(e.target.value); setConfirmed(false); }} /></label> : null}
        <label htmlFor={`${id}-path`}>Build readiness path<Input id={`${id}-path`} required pattern="/.*" maxLength={1024} value={path} disabled={busy || Boolean(attempt)} onChange={(e) => { setPath(e.target.value); setConfirmed(false); }} /></label>
        <label htmlFor={`${id}-timeout`}>Build readiness timeout in seconds<Input id={`${id}-timeout`} type="number" min={1} max={120} required value={timeout} disabled={busy || Boolean(attempt)} onChange={(e) => { setTimeout(e.target.value); setConfirmed(false); }} /></label>
      </div>
      <p className="text-xs text-muted-foreground">Readiness uses ordinary HTTP at a relative path such as /health. Automatic allocation retains earlier containers until explicit cleanup. A fixed port can interrupt the current release before readiness succeeds. Deployment does not automatically switch traffic or configure HTTPS.</p>
      <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={confirmed} disabled={busy || Boolean(attempt)} onChange={(e) => setConfirmed(e.target.checked)} /><span>I reviewed this exact successful build and local artifact, generations, ports and readiness settings. I understand fixed-port replacement can interrupt service.</span></label>
      <Button type="submit" className="h-auto max-w-full whitespace-normal" disabled={disabled}>{busy ? "Requesting local build deployment..." : attempt ? "Retry identical local build deployment" : "Deploy selected build"}</Button>
      {attempt ? <p className="break-all text-xs">Retained request key {attempt.idempotency_key} · build {attempt.build_id} · resource/source generations {attempt.expected_generation}/{attempt.expected_source_generation}. Inputs remain frozen for receipt recovery.</p> : null}
    </form> : <p>Project maintainer access is required to deploy this build.</p>}
    {attempt && !restored && !denied ? <section className="space-y-2" aria-label="Review separate local deployment request"><p className="text-xs">The earlier request may already exist. Review history before choosing a separate request. This action checks this exact build against current generations and clears its key and acknowledgment only after a successful review. It never selects another build or deploys automatically.</p><Button type="button" variant="outline" className="h-auto max-w-full whitespace-normal" disabled={busy || blocked || held} onClick={() => void reviewSeparateRequest()}>Review this exact build for a separate request</Button></section> : null}
    {error ? <p role="alert">{error}</p> : null}
    {selected ? <><p role="status" className="break-all">Deployment operation {selected} was admitted. Review its recorded result below or in deployment history. After a settled healthy release, use this application's Resource routes to select the existing managed proxy route backend.</p><OperationDetail key={selected} actor={actor} projectID={resource.project_id} resourceID={resource.id} operationID={selected} writable={writable && !denied} owner={false} blocked={blocked || denied} onChanged={changed} /></> : null}
  </section>;
}
