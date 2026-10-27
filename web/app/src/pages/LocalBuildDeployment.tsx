import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { allProjectMembersQuery, ApiError, principalQuery } from "@/api";
import { getHostingRuntimeConfiguration } from "@/api/hosting-runtime-config";
import { getResource, type Resource } from "@/api/hosting";
import { getHostingBuild, checkBuildScope, type HostingBuild } from "@/api/hosting-builds";
import { HostingDeploymentError, hostingBindingQuery, hostingDeploymentKeys, hostingDeploymentsQuery, requestHostingBuildDeployment, type HostingBuildDeploymentInput } from "@/api/hosting-deployments";
import { currentGitStackKey, currentGitStackQuery, getCurrentGitStack } from "@/api/hosting-stack-lifecycle";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { canMaintainProject } from "@/lib/permissions";
import { OperationDetail } from "./ResourceDeployments";

function matchesBuildGenerations(resource: Resource, build: HostingBuild): boolean {
  return resource.source.kind === "git" && ["public", "https_basic"].includes(resource.source.git.credential_kind)
    && resource.source.git.credential_kind === build.snapshot.source.credential_kind
    && resource.generation === build.snapshot.resource_generation
    && resource.source.git.source_id === build.snapshot.source.source_id
    && resource.source.git.generation === build.snapshot.source.generation;
}

// The parent keys this component by actor, resource, permission and exact build.
// Keep a submitted body intact until its response is known or the operator leaves.
type LocalDeploymentProps = { actor: string; resource: Resource; build: HostingBuild; writable: boolean; blocked: boolean; onDenied: () => void };
export function LocalBuildDeployment(props: LocalDeploymentProps) {
  const identity = useQuery(principalQuery());
  const members = useQuery({ ...allProjectMembersQuery(), staleTime: 10_000 });
  if (props.blocked || identity.isPending || identity.isFetching || identity.isError || members.isPending || members.isFetching || members.isError || !identity.data || JSON.stringify([identity.data.provider, identity.data.id]) !== props.actor) return <p role={identity.isError || members.isError ? "alert" : "status"}>Local deployment access is pending or unavailable. Drafts and retained requests are cleared.</p>;
  const writable = props.writable && canMaintainProject(identity.data, members.data ?? [], props.resource.project_id);
  return <LocalDeployment key={JSON.stringify([props.actor, props.resource.project_id, props.resource.id, props.build.id, writable])} {...props} writable={writable} />;
}
function LocalDeployment({ actor, resource, build, writable, blocked, onDenied }: LocalDeploymentProps) {
  const id = useId(), client = useQueryClient();
  const [base, setBase] = useState(resource);
  const compose = build.compose_result;
  const [containerPort, setContainerPort] = useState(String(compose?.source.container_port ?? 80)), [hostPort, setHostPort] = useState("8080");
  const [mode, setMode] = useState("automatic"), [path, setPath] = useState("/"), [timeout, setTimeout] = useState("30");
  const [confirmed, setConfirmed] = useState(false), [busy, setBusy] = useState(false);
  const [attempt, setAttempt] = useState<HostingBuildDeploymentInput>();
  const [conflict, setConflict] = useState(false), [restoreHeld, setRestoreHeld] = useState(false), [denied, setDenied] = useState(false);
  const [error, setError] = useState(""), [selected, setSelected] = useState("");
  const active = useRef(true), submitting = useRef(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => { active.current = true; return () => { active.current = false; request.current?.abort(); }; }, []);
  const binding = useQuery(hostingBindingQuery(resource.project_id, resource.id, actor));
  const history = useQuery(hostingDeploymentsQuery(resource.project_id, resource.id, 0, actor));
  const stackScope = { actor, project: resource.project_id, resource: resource.id };
  const currentStack = useQuery({ ...currentGitStackQuery(stackScope), enabled: Boolean(compose) && !blocked && !denied });
  const committedCompose = Boolean(compose && currentStack.isSuccess && !currentStack.isFetching && currentStack.data.operation);
  const lifecycle = currentStack.data?.lifecycle;
  const changed = async () => { await Promise.all([
    client.invalidateQueries({ queryKey: hostingDeploymentKeys.history(resource.project_id, resource.id) }),
    client.invalidateQueries({ queryKey: hostingDeploymentKeys.retained(resource.project_id, resource.id) }),
    client.invalidateQueries({ queryKey: currentGitStackKey(stackScope) }),
  ]); };
  const source = base.source.kind === "git" ? base.source.git : undefined;
  const currentSource = resource.source.kind === "git" ? resource.source.git : undefined;
  const exactBuild = build.project_id === resource.project_id && build.resource_id === resource.id && build.state === "succeeded" && Boolean(compose ? !build.artifact : build.artifact) && matchesBuildGenerations(base, build) && matchesBuildGenerations(resource, build);
  const changedSource = resource.generation > base.generation || (currentSource?.generation ?? 0) > (source?.generation ?? 0) || currentSource?.source_id !== source?.source_id;
  const placementMatches = binding.data?.id === build.snapshot.target.binding_id && binding.data?.destination_id === build.snapshot.target.destination_id;
  const held = Boolean(history.data?.items.some((operation) => operation.mutation_hold));
  const restored = restoreHeld || Boolean(history.data?.items.some((operation) => operation.restore_ownership_held));
  const paused = blocked || denied || binding.isError || binding.isPending || history.isError || history.isPending || restored || conflict || Boolean(compose) && (currentStack.isPending || currentStack.isFetching || currentStack.isError);
  const disabled = paused || busy || !confirmed || !writable || (!attempt && (!exactBuild || changedSource || !placementMatches || held));
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (disabled || submitting.current || !source || !binding.data) return;
    let body = attempt;
    const controller = new AbortController(); request.current = controller;
    setBusy(true); submitting.current = true; setError("");
    try {
      if (!body) {
        const { runtime_configuration: runtime } = await getHostingRuntimeConfiguration(resource.id, { signal: controller.signal });
        if (!active.current || controller.signal.aborted) return;
        if (runtime.resource_id !== resource.id || runtime.project_id !== resource.project_id || !Number.isSafeInteger(runtime.runtime_generation) || runtime.runtime_generation < 0) throw new Error("Runtime configuration scope mismatch");
        const verifiedStack = compose ? await getCurrentGitStack(stackScope, controller.signal) : undefined;
        const currentLifecycle = verifiedStack?.lifecycle;
        if (!active.current || controller.signal.aborted) return;
        if (currentLifecycle?.state === "held") throw new Error("Compose lifecycle is held");
        body = { ...(compose ? { managed_stack: { acknowledge_interruption: true, ...(currentLifecycle?.state === "stopped" ? { expected_stopped_operation_id: currentLifecycle.stop_operation_id } : {}) } } : {}), ...(runtime.runtime_generation > 0 ? { expected_runtime_configuration_generation: runtime.runtime_generation } : {}), build_id: build.id, expected_generation: base.generation, expected_source_generation: source.generation, idempotency_key: crypto.randomUUID(), container_port: Number(containerPort), ...(mode === "fixed" ? { host_port: Number(hostPort) } : {}), readiness: { path, timeout_seconds: Number(timeout) } };
        setAttempt(body);
      }
      const operation = await requestHostingBuildDeployment(resource.id, body, { signal: controller.signal });
      if (!active.current || controller.signal.aborted) return;
      const provenance = "git_compose" in operation.snapshot ? operation.snapshot.git_compose : undefined;
      const stack = "stack" in operation.snapshot ? operation.snapshot.stack : undefined;
      const wrongResult = compose
        ? !provenance || provenance.build_id !== body.build_id || provenance.source_generation !== body.expected_source_generation || provenance.result_hash !== compose.hash ||
          !stack || stack.generation !== body.expected_generation || operation.snapshot.stack_stopped_operation_id !== body.managed_stack?.expected_stopped_operation_id || operation.snapshot.local_build !== undefined || operation.snapshot.container_port !== compose.source.container_port ||
          (operation.snapshot.runtime_configuration?.generation ?? 0) !== (body.expected_runtime_configuration_generation ?? 0)
        : operation.snapshot.local_build?.artifact.build_id !== body.build_id || operation.snapshot.local_build.artifact.image_id !== build.artifact?.image_id || operation.snapshot.local_build.source_generation !== body.expected_source_generation;
      if (operation.project_id !== resource.project_id || operation.resource_id !== resource.id || operation.binding_id !== build.snapshot.target.binding_id || wrongResult || operation.snapshot.resource_generation !== body.expected_generation || (body.expected_runtime_configuration_generation !== undefined && operation.snapshot.runtime_configuration?.generation !== body.expected_runtime_configuration_generation)) throw new Error("Deployment response does not match selected build");
      setSelected(operation.id); setAttempt(undefined); setConfirmed(false); await changed();
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) { setDenied(true); onDenied(); return; }
      if (!body) { setError("Unable to verify saved runtime configuration. No new deployment request was sent. Refresh access and try again."); return; }
      if (cause instanceof HostingDeploymentError && cause.code === "hosting_restore_held") { setRestoreHeld(true); setError(""); }
      else if (cause instanceof ApiError && cause.status === 409) { setConflict(true); setError("Deployment conflicts with current resource/source generations, exact artifact, placement, runner capability, or a retained request key. Refresh the resource and build history, review the exact successful build, and explicitly select it again. No generation, build, or request was changed automatically."); }
      else if (cause instanceof ApiError && cause.status === 400) { setConflict(true); setError("The server rejected these ports or readiness settings. Reopen this build to review a separate request."); }
      else setError("Deployment delivery is unconfirmed. The operation may already exist. Retry the identical request with its retained build, generations, settings, and key.");
    } finally { submitting.current = false; if (request.current === controller) request.current = null; if (active.current) setBusy(false); }
  }
  async function reviewSeparateRequest() {
    if (busy || denied || restored) return;
    setBusy(true);
    try {
      const [latest, exact] = await Promise.all([getResource(resource.id), getHostingBuild(build.id)]);
      if (!active.current) return;
      checkBuildScope(exact, resource.project_id, resource.id, build.id);
      if (latest.id !== resource.id || latest.project_id !== resource.project_id || exact.state !== "succeeded" || (compose ? JSON.stringify(exact.compose_result) !== JSON.stringify(compose) || Boolean(exact.artifact) : JSON.stringify(exact.artifact) !== JSON.stringify(build.artifact))) throw new Error("Build no longer matches the reviewed artifact");
      if (!matchesBuildGenerations(latest, exact) || !matchesBuildGenerations(latest, build)) {
        setError("A new build is required for the current resource and source generations before a separate deployment. The retained request, key, acknowledgment, and settings are unchanged.");
        return;
      }
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
    <h6 className="font-medium">{compose ? "Deploy prepared Compose build" : "Deploy recorded local build"}</h6>
    <p className="text-sm">A new deployment captures the current saved runtime generation and requires a compatible runner. Saving variables does not update the serving release.</p>
    <p className="break-all text-sm">Build {build.id} · source generation {source?.generation} · resource generation {base.generation} · {compose ? `prepared Compose ${compose.hash}` : `image ID ${build.artifact?.image_id}`}.</p>
    <p className="text-xs text-muted-foreground">Requires the same saved destination, runner and Docker daemon as this build, a current project grant and enrolled runner with local_build_deploy_v1, and an operator-enabled agent started with --local-build-deploy. {compose ? "Built services use recorded local image IDs. Other service images are pulled from public registries. Deploying this stack interrupts its writers and retains owned named volumes." : "The agent uses the recorded local image ID without fetching or transferring it."}</p>
    {restored ? <p role="alert">Deployment blocked after platform restore. Trusted recovery proof is pending. Refreshing, retrying, or reconciliation cannot release restored ownership. Settings are preserved.</p> : null}
    {!exactBuild || changedSource ? <p role="status">This build does not match the current resource and source generations. A new build is required for the current configuration before a separate deployment.</p> : null}
    {!binding.isPending && !placementMatches ? <p role="status">Restore the build's exact saved destination before deploying. A local artifact cannot move to another host.</p> : null}
    {held ? <p role="status">A recorded mutation hold pauses new deployment requests. A retained identical request can recover its existing operation.</p> : null}
    {compose && currentStack.isError ? <p role="alert">Compose lifecycle evidence is unavailable. Refresh prerequisites before deployment or Start.</p> : null}
    {committedCompose && lifecycle?.state === "stopped" ? <p>Start recreates this selected build's stack containers and retains its named volumes. The exact stop operation is captured before admission.</p> : null}
    {binding.isError || history.isError ? <p role="alert">Unable to refresh deployment placement or history. Requests are paused and settings are preserved.</p> : null}
    <Button type="button" variant="outline" className="h-auto max-w-full whitespace-normal" disabled={busy} onClick={() => { void binding.refetch(); void history.refetch(); if (compose) void currentStack.refetch(); }}>Refresh local deployment prerequisites</Button>
    {writable ? <form className="min-w-0 space-y-3" aria-label="Deploy selected build" onSubmit={(event) => void submit(event)}>
      <div className="grid min-w-0 gap-3 sm:grid-cols-2">
        <label htmlFor={`${id}-container`}>Build container port<Input id={`${id}-container`} type="number" min={1} max={65535} required value={containerPort} disabled={busy || Boolean(attempt) || Boolean(compose)} onChange={(e) => { setContainerPort(e.target.value); setConfirmed(false); }} /></label>
        <label htmlFor={`${id}-mode`}>Build host port allocation<select id={`${id}-mode`} className="h-10 w-full min-w-0 rounded-xl border bg-card px-2 text-sm" value={mode} disabled={busy || Boolean(attempt)} onChange={(e) => { setMode(e.target.value); setConfirmed(false); }}><option value="automatic">{compose ? "Automatic stable stack port" : "Automatic per-release port"}</option><option value="fixed">Advanced: explicit fixed port</option></select></label>
        {mode === "fixed" ? <label htmlFor={`${id}-host`}>Build runner-local host port<Input id={`${id}-host`} type="number" min={1024} max={65535} required value={hostPort} disabled={busy || Boolean(attempt)} onChange={(e) => { setHostPort(e.target.value); setConfirmed(false); }} /></label> : null}
        <label htmlFor={`${id}-path`}>Build readiness path<Input id={`${id}-path`} required pattern="/.*" maxLength={1024} value={path} disabled={busy || Boolean(attempt)} onChange={(e) => { setPath(e.target.value); setConfirmed(false); }} /></label>
        <label htmlFor={`${id}-timeout`}>Build readiness timeout in seconds<Input id={`${id}-timeout`} type="number" min={1} max={120} required value={timeout} disabled={busy || Boolean(attempt)} onChange={(e) => { setTimeout(e.target.value); setConfirmed(false); }} /></label>
      </div>
      <p className="text-xs text-muted-foreground">{compose ? "Readiness uses ordinary HTTP at a relative path such as /health. Automatic allocation reuses the current stack's port on replacement. Every Compose replacement interrupts all components while preserving named volumes. A failed replacement may require recovery before another deployment." : "Readiness uses ordinary HTTP at a relative path such as /health. Automatic allocation retains earlier containers until explicit cleanup. A fixed port can interrupt the current release before readiness succeeds."} Deployment does not automatically switch traffic or configure HTTPS.</p>
      <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={confirmed} disabled={busy || Boolean(attempt)} onChange={(e) => setConfirmed(e.target.checked)} /><span>{compose ? "I reviewed this exact successful Compose build, its service images, generations, ports and readiness. I acknowledge interruption of all stack writers during deployment and retention of named volumes." : "I reviewed this exact successful build and local artifact, generations, ports and readiness settings. I understand fixed-port replacement can interrupt service."}</span></label>
      <Button type="submit" className="h-auto max-w-full whitespace-normal" disabled={disabled}>{busy ? "Requesting local build deployment..." : attempt ? "Retry identical local build deployment" : committedCompose && lifecycle?.state === "stopped" ? "Start selected Compose build" : "Deploy selected build"}</Button>
      {attempt ? <p className="break-all text-xs">Retained request key {attempt.idempotency_key} · build {attempt.build_id} · resource/source/runtime generations {attempt.expected_generation}/{attempt.expected_source_generation}/{attempt.expected_runtime_configuration_generation ?? 0}. Inputs remain frozen for receipt recovery.</p> : null}
    </form> : <p>Project maintainer access is required to deploy this build.</p>}
    {attempt && !restored && !denied ? <section className="space-y-2" aria-label="Review separate local deployment request"><p className="text-xs">The earlier request may already exist. Review history before choosing a separate request. This action checks this exact build against current generations and clears its key and acknowledgment only after a successful review. It never selects another build or deploys automatically.</p><Button type="button" variant="outline" className="h-auto max-w-full whitespace-normal" disabled={busy || blocked || held} onClick={() => void reviewSeparateRequest()}>Review this exact build for a separate request</Button></section> : null}
    {error ? <p role="alert">{error}</p> : null}
    {selected ? <><p role="status" className="break-all">Deployment operation {selected} was admitted. Review its recorded result below or in deployment history. After a settled healthy release, use this application's Resource routes to select the existing managed proxy route backend.</p><OperationDetail key={selected} actor={actor} projectID={resource.project_id} resourceID={resource.id} operationID={selected} writable={writable && !denied} owner={false} blocked={blocked || denied} onChanged={changed} /></> : null}
  </section>;
}
