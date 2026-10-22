import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, principalQuery } from "@/api";
import { getResource, type Resource } from "@/api/hosting";
import { resourceBindingQuery } from "@/api/destinations";
import { buildError, buildQuery, buildsQuery, buildTerminal, cancelHostingBuild, checkBuildScope, requestHostingBuild, type HostingBuildInput } from "@/api/hosting-builds";
import { Button } from "@/components/ui/button";
import { PageNavigation } from "@/components/common/PageNavigation";
import { maximumPageOffset } from "@/router/search";
import { BuildLogs } from "./BuildLogs";
import { eligibleSourceGrant, sourceGrantKeys, sourceGrantsQuery } from "@/api/hosting-source-grants";
import { ResourceSourceGrants, useSourceGrantAccess } from "./ResourceSourceGrants";
import { LocalBuildDeployment } from "./LocalBuildDeployment";
const names: Record<string, string> = { queued: "Queued", sourcing: "Acquiring source", building: "Building artifact", succeeded: "Succeeded", failed: "Failed", canceled: "Canceled", cleanup_required: "Cleanup required" };
const buttonClass = "h-auto max-w-full whitespace-normal break-all";
function cancellationError(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 409) return "Cancellation conflicts with the retained build revision. Review refreshed state and reconfirm explicitly.";
    if (cause.status === 503) return "Cancellation service is unavailable. The confirmed build revision is preserved for an explicit retry.";
    return buildError(cause);
  }
  return "Cancellation delivery is unconfirmed. Retry at the retained revision; do not assume the build or cleanup stopped.";
}
function BuildDetail({ actor, resource, id, writable, blocked, changed, onDenied }: { actor: string; resource: Resource; id: string; writable: boolean; blocked: boolean; changed: () => Promise<unknown>; onDenied: () => void }) {
  const detail = useQuery({ ...buildQuery(actor, resource.project_id, resource.id, id), refetchInterval: (q) => q.state.data && !buildTerminal(q.state.data.state) ? 3000 : false });
  const [revision, setRevision] = useState<number>();
  const [attempted, setAttempted] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [denied, setDenied] = useState(false);
  const [error, setError] = useState("");
  const active = useRef(true), submitting = useRef(false);
  useEffect(() => () => { active.current = false; }, []);
  const b = detail.data;
  async function cancel() {
    if (!writable || blocked || denied || detail.isError || !b || !confirmed || conflict || submitting.current || buildTerminal(b.state)) return;
    const expected = revision ?? b.revision;
    setRevision(expected); setAttempted(true); setBusy(true); submitting.current = true; setError("");
    try { const result = await cancelHostingBuild(id, expected); if (!active.current) return; checkBuildScope(result, resource.project_id, resource.id, id); setRevision(undefined); setAttempted(false); setConfirmed(false); await detail.refetch(); await changed(); }
    catch (cause) { if (active.current) { setError(cancellationError(cause)); if (cause instanceof ApiError && [401, 403].includes(cause.status)) { setDenied(true); onDenied(); } if (cause instanceof ApiError && cause.status === 409) { setConflict(true); await detail.refetch(); await changed(); } } }
    finally { submitting.current = false; if (active.current) setBusy(false); }
  }
  return <section className="min-w-0 space-y-2 rounded-lg border p-3" aria-label="Build operation detail">
    <p className="break-all font-medium">Build {id}</p><Button className={buttonClass} type="button" variant="outline" disabled={busy} onClick={() => void detail.refetch()}>Refresh build detail</Button>
    {detail.isPending ? <p role="status">Loading build detail...</p> : null}{detail.isError ? <p role="alert">Unable to refresh build detail. Retained metadata may be stale; cancellation is paused.</p> : null}
    {b ? <><dl className="space-y-1 break-all text-sm"><dt>Recorded build state</dt><dd>{names[b.state] ?? b.state}</dd><dt>Build revision</dt><dd>{b.revision}</dd><dt>Requested resource / source generation</dt><dd>{b.snapshot.resource_generation} / {b.snapshot.source.generation}</dd><dt>Repository / requested ref</dt><dd>{b.snapshot.source.repository_url} · {b.snapshot.source.requested_ref}</dd><dt>Build strategy / context</dt><dd>{b.snapshot.source.settings.strategy} · {b.snapshot.source.settings.context_dir || "repository root"}</dd><dt>Target binding / destination / runner</dt><dd>{b.snapshot.target.binding_id} / {b.snapshot.target.destination_id} / {b.snapshot.target.runner_id}</dd><dt>Created / last updated</dt><dd>{b.created_at} / {b.updated_at}</dd>{b.run_id ? <><dt>Run ID</dt><dd>{b.run_id}</dd></> : null}{b.failure_code ? <><dt>Recorded failure or cancellation code</dt><dd>{b.failure_code}</dd></> : null}{b.source_receipt ? <><dt>Resolved commit</dt><dd>{b.source_receipt.commit}</dd><dt>Source tree digest</dt><dd>{b.source_receipt.tree_digest}</dd></> : null}{b.artifact ? <><dt>Artifact origin</dt><dd>{b.artifact.origin}</dd><dt>Daemon image ID</dt><dd>{b.artifact.image_id}</dd><dt>OCI index / manifest / config digest</dt><dd>{b.artifact.index_digest} / {b.artifact.manifest_digest} / {b.artifact.config_digest}</dd><dt>Artifact platform</dt><dd>{b.artifact.platform}</dd></> : null}</dl>
      <BuildLogs actor={actor} project={resource.project_id} resource={resource.id} build={b.id} blocked={blocked || denied || detail.isError} />
      {b.failure_code === "cancel_requested" ? <p role="status">{buildTerminal(b.state) ? "Cancellation intent was recorded. See the terminal build state above; this is not application runtime status." : "Cancellation was requested. Cleanup or a terminal acknowledgment may still be pending."}</p> : null}
      {b.state === "cleanup_required" ? <p role="status">Runner cleanup is required; this state does not prove cleanup or cancellation completed.</p> : null}
      {b.state === "succeeded" ? <p>Build success records an artifact. It does not mean the application is deployed or running.</p> : null}
      {b.state === "succeeded" && b.artifact ? <LocalBuildDeployment key={JSON.stringify([actor, resource.id, b.id, writable])} actor={actor} resource={resource} build={b} writable={writable} blocked={blocked || denied || detail.isError} onDenied={onDenied} /> : null}
      {writable && !buildTerminal(b.state) ? <div className="space-y-2"><label className="flex items-start gap-2 text-sm"><input type="checkbox" disabled={busy || attempted} checked={confirmed} onChange={(e) => { setConfirmed(e.target.checked); setRevision(e.target.checked ? b.revision : undefined); }} /><span className="min-w-0 break-all">I confirm cancellation of build {id} at {revision !== undefined ? `retained revision ${revision}` : `revision ${b.revision}`}.</span></label><Button className={buttonClass} type="button" disabled={busy || blocked || denied || detail.isError || !confirmed || conflict} onClick={() => void cancel()}>{busy ? "Requesting cancellation..." : attempted ? "Retry cancellation at retained revision" : "Request build cancellation"}</Button></div> : null}
      {conflict ? <><p role="status">Cancellation revision changed. Review the refreshed state and confirm again; no new revision was submitted automatically.</p><Button className={buttonClass} type="button" variant="outline" disabled={busy || blocked || denied || detail.isError} onClick={() => { setRevision(undefined); setAttempted(false); setConfirmed(false); setConflict(false); setError(""); }}>Review refreshed cancellation revision</Button></> : null}
    </> : null}{error ? <p role="alert">{error}</p> : null}
  </section>;
}
export function ResourceBuilds(props: { resource: Resource; writable: boolean; blocked: boolean }) { return props.resource.source.kind === "git" ? <BuildIdentity {...props} /> : null; }
function BuildIdentity(props: { resource: Resource; writable: boolean; blocked: boolean }) {
  const principal = useQuery(principalQuery());
  if (!principal.data) return <p role={principal.isError ? "alert" : "status"}>{principal.isError ? "Unable to load build identity. Reload to refresh access." : "Loading build identity..."}</p>;
  const actor = JSON.stringify([principal.data.provider, principal.data.id]);
  return <Builds key={JSON.stringify([actor, props.resource.project_id, props.resource.id, props.writable])} {...props} actor={actor} blocked={props.blocked || principal.isError} />;
}
function Builds({ resource, writable, blocked, actor }: { resource: Resource; writable: boolean; blocked: boolean; actor: string }) {
  const client = useQueryClient();
  const [grantOffset, setGrantOffset] = useState(0), [grantID, setGrantID] = useState("");
  const [offset, setOffset] = useState(0), [selected, setSelected] = useState("");
  const [base, setBase] = useState(resource), [latest, setLatest] = useState<Resource>();
  const [attempt, setAttempt] = useState<HostingBuildInput>();
  const [acknowledged, setAcknowledged] = useState(false), [busy, setBusy] = useState(false), [conflict, setConflict] = useState(false), [denied, setDenied] = useState(false);
  const [error, setError] = useState(""), [refreshError, setRefreshError] = useState("");
  const active = useRef(true), submitting = useRef(false), request = useRef<AbortController | null>(null), metadataRequest = useRef<AbortController | null>(null);
  useEffect(() => { active.current = true; return () => { active.current = false; request.current?.abort(); metadataRequest.current?.abort(); client.removeQueries({ queryKey: sourceGrantKeys.resource(actor, resource.project_id, resource.id) }); }; }, []);
  const history = useQuery({ ...buildsQuery(actor, resource.project_id, resource.id, offset), enabled: !blocked && !denied, refetchInterval: (q) => q.state.status !== "error" && q.state.data?.items.some((b) => !buildTerminal(b.state)) ? 3000 : false });
  const binding = useQuery({ ...resourceBindingQuery(resource.project_id, resource.id), enabled: !blocked && !denied });
  const source = base.source.kind === "git" ? base.source.git : undefined;
  const privateSource = source?.credential_kind === "https_basic";
  const grantAccess = useSourceGrantAccess(actor, resource, !privateSource || !writable || blocked || denied);
  const canReadGrants = grantAccess.canReadGrants;
  const grants = useQuery({ ...sourceGrantsQuery(actor, resource.project_id, resource.id, grantOffset), enabled: canReadGrants, refetchOnWindowFocus: false });
  const currentResource = resource.generation >= base.generation ? resource : base;
  const grant = grants.data?.items.find((item) => item.id === grantID && eligibleSourceGrant(item, base) && eligibleSourceGrant(item, currentResource));
  const sourceChanged = currentResource.generation !== base.generation || currentResource.source.kind !== "git" || currentResource.source.git.source_id !== source?.source_id || currentResource.source.git.generation !== source?.generation || currentResource.source.git.credential_kind !== source?.credential_kind;
  function deny() { request.current?.abort(); metadataRequest.current?.abort(); setDenied(true); setAttempt(undefined); setGrantID(""); setAcknowledged(false); client.removeQueries({ queryKey: sourceGrantKeys.resource(actor, resource.project_id, resource.id) }); }
  const accessDenied = [grants.error, history.error, binding.error].some((cause) => cause instanceof ApiError && [401, 403, 404].includes(cause.status));
  useEffect(() => { if (accessDenied) deny(); }, [accessDenied]);
  const paused = blocked || denied || history.isError || history.isPending || binding.isError || binding.isPending || (privateSource && writable && (!canReadGrants || grants.isError || grants.isPending));
  async function refreshCurrent() { const controller = new AbortController(); metadataRequest.current?.abort(); metadataRequest.current = controller; setRefreshError(""); setLatest(undefined); try { const current = await getResource(resource.id, { signal: controller.signal }); if (!active.current || controller.signal.aborted) return; if (current.id !== resource.id || current.project_id !== resource.project_id) throw new Error("Resource scope mismatch"); setLatest(current); await history.refetch(); await binding.refetch(); } catch { if (active.current && !controller.signal.aborted) setRefreshError("Unable to refresh current source. The retained request remains unchanged."); } }
  async function submit() {
    if (!writable || paused || conflict || submitting.current || !binding.data || !source || !["public", "https_basic"].includes(source.credential_kind) || !acknowledged || (!attempt && (sourceChanged || (privateSource && !grant)))) return;
    const body = attempt ?? { expected_resource_generation: base.generation, expected_source_generation: source.generation, idempotency_key: crypto.randomUUID(), ...(privateSource ? { grant_id: grantID } : {}) };
    const controller = new AbortController(); request.current = controller;
    setAttempt(body); setBusy(true); submitting.current = true; setError("");
    try { const result = await requestHostingBuild(resource.id, body, { signal: controller.signal, credentialKind: privateSource ? "https_basic" : "public" }); if (!active.current || controller.signal.aborted) return; checkBuildScope(result, resource.project_id, resource.id); setSelected(result.id); setAttempt(undefined); setAcknowledged(false); setOffset(0); await history.refetch(); }
    catch (cause) { if (active.current && !controller.signal.aborted) { setError(buildError(cause)); if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) deny(); if (cause instanceof ApiError && cause.status === 409) { setConflict(true); await refreshCurrent(); } } }
    finally { submitting.current = false; if (request.current === controller) request.current = null; if (active.current) setBusy(false); }
  }
  return <section className="min-w-0 space-y-3 border-t pt-3" aria-label={`Git builds for ${resource.name}`}><h5 className="font-medium">{privateSource ? "Private HTTPS Git builds" : "Public Git builds"}</h5><p className="text-xs text-muted-foreground">Build operations acquire the configured source and produce an artifact. Select a successful build to deploy its recorded local artifact. Select a build to read its persisted output. A successful build does not mean a running application.</p>
    <ResourceSourceGrants access={grantAccess} actor={actor} resource={resource} blocked={blocked || denied || !writable} onDenied={deny} />
    <Button className={buttonClass} type="button" variant="outline" disabled={busy || blocked || denied || accessDenied} onClick={() => { if (busy || blocked || denied || accessDenied) return; if (canReadGrants) void grants.refetch(); void history.refetch(); void binding.refetch(); }}>Refresh build history</Button>
    {history.isPending ? <p role="status">Loading build history...</p> : null}{history.isError || binding.isError ? <p role="alert">Unable to refresh build history or placement. Retained metadata may be stale; mutations are paused.</p> : null}
    {history.data?.items.length === 0 ? <p>No build operations recorded.</p> : null}
    <ol className="space-y-2" aria-label="Build history">{history.data?.items.map((b) => <li className="min-w-0 rounded-lg border p-2" key={b.id}><p className="break-all text-sm">{names[b.state] ?? b.state} · {b.created_at} · resource {b.snapshot.resource_generation} / source {b.snapshot.source.generation}</p><Button className={buttonClass} type="button" variant="outline" onClick={() => setSelected(b.id)}>View build {b.id}</Button></li>)}</ol>
    <PageNavigation label="build history" page={history.data} loading={history.isFetching || busy} onFirst={() => setOffset(0)} onPrevious={() => setOffset(Math.max(0, offset - 20))} onNext={() => setOffset(Math.min(maximumPageOffset, offset + 20))} />
    {selected ? <BuildDetail key={selected} actor={actor} resource={resource} id={selected} writable={writable} blocked={paused} changed={() => history.refetch()} onDenied={deny} /> : null}
    {writable ? <section className="space-y-2 rounded-lg border p-3" aria-label={privateSource ? "Request private Git build" : "Request public Git build"}><p className="break-all">Reviewed source: {source?.repository_url} · {source?.requested_ref} · resource generation {base.generation} / source generation {source?.generation}.</p><p className="text-xs">Requires an allowed HTTPS source, a saved destination with active project grant, an active enrolled hosting_build_v1 runner without shell, and an operator-enabled build policy. This UI cannot enable server policy.</p>
      {!binding.isPending && !binding.isError && !binding.data ? <p role="status">Save a desired destination before requesting a build.</p> : null}{sourceChanged ? <p role="status">Saved source metadata changed. Review current generations before a separate build request.</p> : null}
      {privateSource && !denied ? <section className="space-y-2" aria-label="Select private source grant"><label className="block">Private source grant<select className="min-h-10 w-full min-w-0 rounded border bg-background p-2 text-sm" value={grantID} disabled={busy || Boolean(attempt) || grants.isError || blocked} onChange={(event) => { setGrantID(event.target.value); setAcknowledged(false); }}><option value="">Choose an eligible source grant</option>{grants.data?.items.map((item) => <option value={item.id} key={item.id} disabled={!eligibleSourceGrant(item, base) || !eligibleSourceGrant(item, currentResource)}>{item.id} · source {item.source_generation} · {item.revoked ? "revoked" : eligibleSourceGrant(item, currentResource) ? "eligible" : "ineligible"}</option>)}</select></label>{grants.isError ? <p role="alert">Unable to verify private source grants. Refresh build history to retry.</p> : null}{grantID && !grant && !attempt ? <p role="status">The selected grant is revoked, stale, or unavailable on this page. Select an eligible current grant before a new build request.</p> : null}<PageNavigation label="build source grants" page={grants.data} loading={grants.isFetching || busy || Boolean(attempt)} onFirst={() => setGrantOffset(0)} onPrevious={() => setGrantOffset(Math.max(0, grantOffset - 20))} onNext={() => setGrantOffset(Math.min(maximumPageOffset, grantOffset + 20))} /></section> : null}
      <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={acknowledged} disabled={busy || Boolean(attempt)} onChange={(e) => setAcknowledged(e.target.checked)} /><span>I reviewed this exact {privateSource ? "private HTTPS Git source, grant," : "public Git source"} and generations for this build request.</span></label>
      <Button className={buttonClass} type="button" disabled={paused || busy || conflict || !binding.data || !source || !["public", "https_basic"].includes(source.credential_kind) || !acknowledged || (!attempt && (sourceChanged || (privateSource && !grant)))} onClick={() => void submit()}>{busy ? "Requesting build..." : attempt ? "Retry identical build request" : privateSource ? "Request private Git build" : "Request public Git build"}</Button>
      {attempt ? <p className="break-all text-xs">Frozen build key: {attempt.idempotency_key} · resource generation {attempt.expected_resource_generation} / source generation {attempt.expected_source_generation}{attempt.grant_id ? ` · grant ${attempt.grant_id}` : ""}</p> : null}
      <Button className={buttonClass} type="button" variant="outline" disabled={busy} onClick={() => void refreshCurrent()}>Refresh current build source</Button>
      {latest ? <section className="space-y-2" aria-label="Current build source"><p className="break-all">Current saved configuration: resource generation {latest.generation} · {latest.source.kind === "git" ? `${latest.source.git.repository_url} at ${latest.source.git.requested_ref} · source generation ${latest.source.git.generation}` : "image source; public Git build unavailable"}.</p><Button className={buttonClass} type="button" variant="outline" disabled={busy || paused || latest.source.kind !== "git" || !["public", "https_basic"].includes(latest.source.git.credential_kind)} onClick={() => { setBase(latest); setGrantID(""); setGrantOffset(0); setAttempt(undefined); setAcknowledged(false); setConflict(false); setLatest(undefined); setError(""); }}>Use reviewed source for a separate build</Button><p className="text-xs">This discards the retained request key and requires a new acknowledgment. Check history first; an unconfirmed request may already exist. Nothing is submitted automatically.</p></section> : null}
      {refreshError ? <p role="alert">{refreshError}</p> : null}
    </section> : <p className="text-xs">Project maintainer access is required to request or cancel builds.</p>}{error ? <p role="alert">{error}</p> : null}
  </section>;
}
