import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, listProjectMembers, principalQuery } from "@/api";
import { getResource, type Resource } from "@/api/hosting";
import { listManagedSecrets, listManagedSecretVersions } from "@/api/managed-secrets";
import { eligibleSourceGrant, newSourceGrantRequestID, requestHostingSourceGrant, revokeHostingSourceGrant, sourceGrantKeys, sourceGrantsQuery, type HostingSourceGrantRequest } from "@/api/hosting-source-grants";
import { canOwnProject, canMaintainProject } from "@/lib/permissions";
import { Button } from "@/components/ui/button";
import { PageNavigation } from "@/components/common/PageNavigation";
const button = "h-auto max-w-full whitespace-normal break-all";
const select = "min-h-10 w-full min-w-0 rounded-xl border bg-background p-2 text-sm";
const deniedError = (cause: unknown) => cause instanceof ApiError && [401, 403, 404].includes(cause.status);
function metadataError(cause: unknown): never { throw cause instanceof ApiError ? new ApiError(cause.status, "Secret reference metadata is unavailable", cause.requestID) : new Error("Secret reference metadata is unavailable"); }
function grantError(cause: unknown) {
 if (cause instanceof ApiError && cause.status === 409) return "The source generation, secret reference, or request ID conflicts. Refresh and review a separate decision explicitly.";
 if (cause instanceof ApiError && cause.status === 400) return "The server rejected this grant reference. Review the saved source and exact secret version.";
 if (deniedError(cause)) return "Source grant access is unavailable. Reload to verify current project access.";
 return "No confirmed response was received. Retry the identical retained request to recover its result.";
}
export function ResourceSourceGrants({ actor, resource, blocked, onDenied }: { actor: string; resource: Resource; blocked: boolean; onDenied: () => void }) {
 if (blocked || resource.source.kind !== "git" || resource.source.git.credential_kind !== "https_basic") return null;
 return <GrantAccess key={JSON.stringify([actor, resource.project_id, resource.id, resource.generation, resource.source.git.source_id, resource.source.git.generation])} actor={actor} resource={resource} onDenied={onDenied} />;
}
function GrantAccess({ actor, resource, onDenied }: { actor: string; resource: Resource; onDenied: () => void }) {
 const [offset, setOffset] = useState(0), [denied, setDenied] = useState(false);
 const client = useQueryClient();
 const principal = useQuery(principalQuery());
 const members = useQuery({ queryKey: ["sourceGrantOwners", actor, resource.project_id], retry: false, gcTime: 0, queryFn: async ({ signal }) => {
  const rows = []; let offset = 0;
  for (;;) { const page = await listProjectMembers({ project_id: resource.project_id, limit: 100, offset }, { signal }); if (page.items.some((member) => member.project_id !== resource.project_id)) throw new Error("Member scope mismatch"); rows.push(...page.items); if (!page.count || page.offset + page.count >= page.total) return rows; const next = page.offset + page.count; if (next <= offset || next > 100000) throw new Error("Member pagination unavailable"); offset = next; }
 } });
 const currentActor = principal.data && JSON.stringify([principal.data.provider, principal.data.id]) === actor;
 const maintainer = Boolean(currentActor && members.data && canMaintainProject(principal.data, members.data, resource.project_id));
 const grants = useQuery({ ...sourceGrantsQuery(actor, resource.project_id, resource.id, offset), enabled: !denied && maintainer && !members.isError });
 const owner = Boolean(currentActor && !principal.isError && members.data && canOwnProject(principal.data, members.data, resource.project_id));
 const deny = () => { setDenied(true); client.removeQueries({ queryKey: sourceGrantKeys.resource(actor, resource.project_id, resource.id) }); onDenied(); };
 useEffect(() => { if (deniedError(grants.error) || deniedError(members.error) || principal.isError || !currentActor) deny(); }, [grants.error, members.error, principal.isError, currentActor]);
 const refresh = async () => { await client.invalidateQueries({ queryKey: sourceGrantKeys.resource(actor, resource.project_id, resource.id) }); };
 if (denied || !currentActor || principal.isError) return null;
 if (!members.isPending && !members.isError && !maintainer) return <p>Project maintainer access is required to list source grants. Owners create and revoke grants.</p>;
 return <section className="min-w-0 space-y-3 rounded border p-3" aria-label="Private HTTPS source grants"><h5 className="font-medium">Private HTTPS source grants</h5><p className="text-xs">Grants reference an existing managed secret and an explicit version. Create or rotate a JSON credential object with username and password strings in Project secrets. These controls select references and never reveal values.</p><Button className={button} variant="outline" disabled={grants.isFetching} onClick={() => { void grants.refetch(); void members.refetch(); }}>Refresh source grants</Button>
  {grants.isPending ? <p role="status">Loading source grants...</p> : null}{grants.isError ? <p role="alert">Unable to read current source grant metadata. Grant actions are paused.</p> : null}
  <ul className="space-y-2">{!grants.isError && grants.data?.items.map((grant) => <li className="min-w-0 break-all rounded border p-2" key={grant.id}><p>Grant {grant.id} · v{grant.version} · source {grant.source_id} / generation {grant.source_generation} · {grant.revoked ? "revoked" : eligibleSourceGrant(grant, resource) ? "eligible for this source" : "not eligible for this source"}</p>{owner && !grant.revoked ? <RevokeGrant key={grant.id} actor={actor} resource={resource} grantID={grant.id} paused={grants.isError || members.isError || members.isPending} changed={refresh} onDenied={deny} /> : null}</li>)}</ul>
  <PageNavigation label="source grants" page={grants.data} loading={grants.isFetching} onFirst={() => setOffset(0)} onPrevious={() => setOffset(Math.max(0, offset - 20))} onNext={() => setOffset(Math.min(100000, offset + 20))} />
  {owner ? <GrantRequest key="owner" actor={actor} resource={resource} paused={grants.isError || members.isError || members.isPending} changed={refresh} onDenied={deny} /> : <p className="text-xs">Project owner access is required to create or revoke source grants. Maintainers can select an eligible grant for a build.</p>}
 </section>;
}
function RevokeGrant({ grantID, resource, paused, changed, onDenied }: { actor: string; grantID: string; resource: Resource; paused: boolean; changed: () => Promise<void>; onDenied: () => void }) {
 const [busy, setBusy] = useState(false), [attempted, setAttempted] = useState(false), [conflict, setConflict] = useState(false), [error, setError] = useState(""); const request = useRef<AbortController | null>(null), active = useRef(false);
 useEffect(() => { active.current = true; return () => { active.current = false; request.current?.abort(); }; }, []);
 async function revoke() { if (paused || conflict || request.current) return; const controller = new AbortController(); request.current = controller; setBusy(true); setAttempted(true); setError(""); try { const result = await revokeHostingSourceGrant(resource.id, grantID, { signal: controller.signal }); if (!active.current || controller.signal.aborted) return; if (!result.revoked) throw new Error("Revocation unconfirmed"); await changed(); } catch (cause) { if (!active.current || controller.signal.aborted) return; if (deniedError(cause)) onDenied(); else { setError(grantError(cause)); if (cause instanceof ApiError && cause.status === 409) setConflict(true); } } finally { if (request.current === controller) request.current = null; if (active.current) setBusy(false); } }
 return <><Button className={button} type="button" variant="outline" disabled={busy || paused || conflict} onClick={() => void revoke()}>{busy ? "Revoking grant..." : attempted ? `Retry revoke grant ${grantID}` : `Revoke source grant ${grantID}`}</Button>{error ? <p role="alert">{error}</p> : null}{conflict ? <Button className={button} type="button" disabled={busy} onClick={() => { void changed().then(() => { if (active.current) { setConflict(false); setAttempted(false); setError(""); } }); }}>Review refreshed grant for revocation</Button> : null}</>;
}
function GrantRequest({ actor, resource, paused, changed, onDenied }: { actor: string; resource: Resource; paused: boolean; changed: () => Promise<void>; onDenied: () => void }) {
 const client = useQueryClient();
 const [base, setBase] = useState(resource), [latest, setLatest] = useState<Resource>();
 const [secretID, setSecretID] = useState(""), [version, setVersion] = useState(""), [secretOffset, setSecretOffset] = useState(0), [versionOffset, setVersionOffset] = useState(0);
 const [attempt, setAttempt] = useState<HostingSourceGrantRequest>(), [confirmed, setConfirmed] = useState(false), [busy, setBusy] = useState(false), [conflict, setConflict] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
 const active = useRef(false), request = useRef<AbortController | null>(null);
 useEffect(() => { active.current = true; return () => { active.current = false; request.current?.abort(); client.removeQueries({ queryKey: ["sourceGrantSecrets", actor, resource.project_id, resource.id] }); client.removeQueries({ queryKey: ["sourceGrantVersions", actor, resource.project_id, resource.id] }); }; }, []);
 const secrets = useQuery({ queryKey: ["sourceGrantSecrets", actor, resource.project_id, resource.id, secretOffset], retry: false, gcTime: 0, queryFn: async ({ signal }) => {
  const page = await listManagedSecrets({ project_id: resource.project_id, limit: 20, offset: secretOffset }, { signal }).catch(metadataError);
  if (page.items.some((secret) => secret.project_id !== resource.project_id)) throw new Error("Secret scope mismatch");
  return { limit: page.limit, offset: page.offset, count: page.count, total: page.total, items: page.items.map(({ id, project_id, name, current_version, status }) => ({ id, project_id, name, current_version, status })) };
 } });
 const versions = useQuery({ queryKey: ["sourceGrantVersions", actor, resource.project_id, resource.id, secretID, versionOffset], enabled: Boolean(secretID), retry: false, gcTime: 0, queryFn: async ({ signal }) => {
  const page = await listManagedSecretVersions(secretID, { limit: 20, offset: versionOffset }, { signal }).catch(metadataError); if (page.items.some((item) => item.secret_id !== secretID)) throw new Error("Secret version scope mismatch"); return { limit: page.limit, offset: page.offset, count: page.count, total: page.total, items: page.items.map(({ secret_id, version }) => ({ secret_id, version })) };
 } });
 useEffect(() => { if (deniedError(secrets.error) || deniedError(versions.error)) onDenied(); }, [secrets.error, versions.error]);
 const selectedSecret = secrets.data?.items.find((secret) => secret.id === secretID && secret.status === "active");
 const validVersion = versions.data?.items.some((item) => item.version === Number(version) && item.version > 0);
 const disabled = busy || paused || secrets.isError || (Boolean(secretID) && (versions.isPending || versions.isError));
 async function submit() {
  if (disabled || request.current || conflict || !confirmed || base.source.kind !== "git" || base.source.git.credential_kind !== "https_basic" || (!attempt && (!selectedSecret || !validVersion))) return;
  const input = attempt ?? { expected_resource_generation: base.generation, expected_source_generation: base.source.git.generation, secret_id: secretID, secret_version: Number(version), request_id: newSourceGrantRequestID() };
  const sourceID = base.source.git.source_id, controller = new AbortController(); request.current = controller; setAttempt(input); setBusy(true); setError(""); setNotice("");
  try { const grant = await requestHostingSourceGrant(resource.id, input, { signal: controller.signal }); if (!active.current || controller.signal.aborted) return; if (grant.resource_id !== resource.id || grant.source_id !== sourceID || grant.source_generation !== input.expected_source_generation || grant.kind !== "https_basic" || grant.version !== 1) throw new Error("Grant scope mismatch"); setAttempt(undefined); setConfirmed(false); setNotice(`Source grant ${grant.id} recorded. Select it explicitly for a private build.`); await changed(); }
  catch (cause) { if (!active.current || controller.signal.aborted) return; if (deniedError(cause)) onDenied(); else { setError(grantError(cause)); if (cause instanceof ApiError && cause.status === 409) setConflict(true); if (cause instanceof ApiError && cause.status === 400) { setAttempt(undefined); setConfirmed(false); } } }
  finally { if (request.current === controller) request.current = null; if (active.current) setBusy(false); }
 }
 async function refreshDecision() { if (busy || request.current) return; const controller = new AbortController(); request.current = controller; setBusy(true); setLatest(undefined); try { const current = await getResource(resource.id, { signal: controller.signal }); if (!active.current || controller.signal.aborted) return; if (current.id !== resource.id || current.project_id !== resource.project_id) throw new Error("Resource scope mismatch"); setLatest(current); await changed(); } catch (cause) { if (active.current && !controller.signal.aborted) { if (deniedError(cause)) onDenied(); else setError("Unable to refresh the current source. The retained grant decision remains unchanged."); } } finally { if (request.current === controller) request.current = null; if (active.current) setBusy(false); } }
 return <form aria-label="Request private source grant" className="min-w-0 space-y-3 border-t pt-3" onSubmit={(event) => { event.preventDefault(); void submit(); }}><p className="break-all text-sm">Reviewed source generation {base.source.kind === "git" ? base.source.git.generation : "unavailable"} · resource generation {base.generation}</p>
  <label className="block">Existing managed secret<select className={select} value={secretID} disabled={disabled || Boolean(attempt)} onChange={(event) => { setSecretID(event.target.value); setVersion(""); setVersionOffset(0); setConfirmed(false); }}><option value="">Choose secret metadata</option>{secrets.data?.items.filter((secret) => secret.status === "active").map((secret) => <option key={secret.id} value={secret.id}>{secret.name} · {secret.id}</option>)}</select></label>
  <PageNavigation label="grant secret metadata" page={secrets.data} loading={secrets.isFetching || busy || Boolean(attempt)} onFirst={() => { setSecretOffset(0); setSecretID(""); setVersion(""); }} onPrevious={() => { setSecretOffset(Math.max(0, secretOffset - 20)); setSecretID(""); setVersion(""); }} onNext={() => { setSecretOffset(Math.min(100000, secretOffset + 20)); setSecretID(""); setVersion(""); }} />
  <label className="block">Explicit secret version<select className={select} value={version} disabled={disabled || Boolean(attempt) || !secretID} onChange={(event) => { setVersion(event.target.value); setConfirmed(false); }}><option value="">Choose an existing version</option>{versions.data?.items.map((item) => <option key={item.version} value={item.version}>Version {item.version}</option>)}</select></label>
  {secretID ? <PageNavigation label="grant secret versions" page={versions.data} loading={versions.isFetching || busy || Boolean(attempt)} onFirst={() => { setVersionOffset(0); setVersion(""); }} onPrevious={() => { setVersionOffset(Math.max(0, versionOffset - 20)); setVersion(""); }} onNext={() => { setVersionOffset(Math.min(100000, versionOffset + 20)); setVersion(""); }} /> : null}
  {secrets.isError || versions.isError ? <><p role="alert">Unable to verify secret metadata. No secret value was requested.</p><Button className={button} type="button" onClick={() => { void secrets.refetch(); if (secretID) void versions.refetch(); }}>Refresh grant secret metadata</Button></> : null}
  <label className="flex items-start gap-2"><input type="checkbox" checked={confirmed} disabled={disabled || Boolean(attempt)} onChange={(event) => setConfirmed(event.target.checked)} /><span>I reviewed this exact source generation, secret reference, and version for a private HTTPS grant.</span></label>
  <Button className={button} type="submit" disabled={disabled || conflict || !confirmed || (!attempt && (!selectedSecret || !validVersion))}>{busy ? "Requesting source grant..." : attempt ? "Retry identical source grant request" : "Create source grant"}</Button>
  {attempt ? <p className="break-all text-xs">Frozen grant request {attempt.request_id} · resource {attempt.expected_resource_generation} / source {attempt.expected_source_generation} · secret {attempt.secret_id} version {attempt.secret_version}</p> : null}{error ? <p role="alert">{error}</p> : null}{notice ? <p role="status" className="break-all">{notice}</p> : null}
  {conflict ? <section className="space-y-2"><Button className={button} type="button" disabled={busy} onClick={() => void refreshDecision()}>Refresh source for grant decision</Button>{latest ? <><p className="break-all">Current resource {latest.generation} · {latest.source.kind === "git" ? `${latest.source.git.source_id} / source ${latest.source.git.generation} / ${latest.source.git.credential_kind}` : "image source"}. Check recorded grants before discarding an uncertain request.</p><Button className={button} type="button" disabled={busy || latest.source.kind !== "git" || latest.source.git.credential_kind !== "https_basic"} onClick={() => { setBase(latest); setAttempt(undefined); setConfirmed(false); setConflict(false); setError(""); setLatest(undefined); }}>Use reviewed source for a separate grant decision</Button></> : null}</section> : null}
 </form>;
}
