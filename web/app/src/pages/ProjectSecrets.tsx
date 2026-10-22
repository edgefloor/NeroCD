import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, type Principal, type Project, type ProjectMember } from "@/api";
import { createManagedSecret, getManagedSecret, listManagedSecrets, listManagedSecretVersions, revealManagedSecret, revokeManagedSecret, rotateManagedSecret, type ManagedSecret } from "@/api/managed-secrets";
import { canMaintainProject } from "@/lib/permissions";
import { PageNavigation } from "@/components/common/PageNavigation";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { maximumPageOffset } from "@/router/search";

const pageSize = 20;
const listKey = (projectID: string) => ["managedSecrets", projectID] as const;
const detailKey = (id: string) => ["managedSecret", id] as const;
const versionsKey = (id: string) => ["managedSecretVersions", id] as const;
// Keep server payloads out of errors: a failed request must never echo a value.
function secretError(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 503) return "Encrypted storage is unavailable. The server's secret keyring is unavailable. Metadata and revocation may still be available.";
    if (cause.status === 409) return "The secret changed or was revoked. Refresh its metadata and review the current version before retrying.";
    if (cause.status === 401 || cause.status === 403) return "You do not have permission for this secret action. Refresh project access before retrying.";
    if (cause.status === 404) return "This secret or project is unavailable.";
    if (cause.status === 400) return "The request was invalid. Check the name or enter a valid nonempty value, then try again.";
  }
  return "The request could not be completed. Check your connection and try again.";
}

function SecretReveal({ secretID, version, onClose }: { secretID: string; version: number; onClose: () => void }) {
  const [value, setValue] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  const active = useRef(true);
  const request = useRef<AbortController | undefined>(undefined);
  useEffect(() => {
    active.current = true;
    return () => { active.current = false; request.current?.abort(); };
  }, []);
  function close() { active.current = false; request.current?.abort(); setValue(undefined); onClose(); }
  async function reveal() {
    if (busy) return;
    const controller = new AbortController();
    request.current?.abort(); request.current = controller;
    setBusy(true); setError(""); setValue(undefined); setCopied(false);
    try {
      const result = await revealManagedSecret(secretID, version, { signal: controller.signal });
      if (!active.current || controller.signal.aborted || request.current !== controller) return;
      if (result.secret_id !== secretID || result.version !== version) { setError("The reveal response did not match the selected secret version."); return; }
      setValue(result.value);
    } catch (cause) { if (active.current && !controller.signal.aborted) setError(secretError(cause)); }
    finally { if (active.current && request.current === controller) setBusy(false); }
  }
  async function copy() {
    if (value === undefined) return;
    try { await navigator.clipboard.writeText(value); if (active.current) setCopied(true); }
    catch { if (active.current) setError("Clipboard access failed. You can select the revealed value manually."); }
  }
  return <section className="space-y-3 rounded-xl border p-3" aria-label={`Reveal version ${version}`}>
    <p className="text-sm">Reveal only if you need this value. The server audits each reveal.</p>
    {value === undefined ? <Button type="button" disabled={busy} onClick={() => void reveal()} className="h-auto whitespace-normal">{busy ? "Revealing value..." : `Reveal value for version ${version}`}</Button> : <>
      <pre aria-label="Revealed secret value" className="max-h-60 overflow-auto whitespace-pre-wrap break-all rounded-lg border p-3 text-sm">{value}</pre>
      <Button type="button" variant="outline" onClick={() => void copy()}>Copy revealed value</Button>
      {copied ? <p role="status">Copied to clipboard.</p> : null}
    </>}
    {error ? <p role="alert">{error}</p> : null}
    <Button type="button" variant="outline" onClick={close}>Close reveal</Button>
  </section>;
}

function SecretDetail({ initial, writable, owner, blocked, onChanged, onClose }: { initial: ManagedSecret; writable: boolean; owner: boolean; blocked: boolean; onChanged: () => Promise<void>; onClose: () => void }) {
  const client = useQueryClient();
  const id = useId();
  const [offset, setOffset] = useState(0);
  const [expected, setExpected] = useState(initial.current_version);
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [current, setCurrent] = useState<ManagedSecret>();
  const [refreshError, setRefreshError] = useState("");
  const [revealVersion, setRevealVersion] = useState<number>();
  const metadata = useQuery({ queryKey: detailKey(initial.id), queryFn: ({ signal }) => getManagedSecret(initial.id, { signal }), retry: false });
  const versions = useQuery({ queryKey: [...versionsKey(initial.id), offset], queryFn: ({ signal }) => listManagedSecretVersions(initial.id, { limit: pageSize, offset }, { signal }), retry: false });
  const secret = metadata.data ?? initial;
  const unavailable = blocked || metadata.isError || metadata.isPending;
  const active = secret.status === "active";
  const canWrite = writable && !unavailable && active;
  const canReveal = owner && !unavailable && !versions.isError && !versions.isPending && active && !busy;
  async function loadCurrent() {
    setCurrent(undefined); setRefreshError("");
    const result = await metadata.refetch();
    if (result.isSuccess) setCurrent(result.data);
    else setRefreshError("Unable to refresh the current secret. Retry metadata before continuing.");
  }
  async function update(kind: "rotate" | "revoke", event: FormEvent) {
    event.preventDefault();
    if (busy || !canWrite || conflict || (kind === "revoke" && (!owner || !confirmed))) return;
    setBusy(true); setError(""); setNotice(""); setRevealVersion(undefined);
    try {
      const result = kind === "rotate" ? await rotateManagedSecret(secret.id, { expected_version: expected, value }) : await revokeManagedSecret(secret.id, { expected_version: expected });
      setExpected(result.current_version); setValue(""); setConfirmed(false);
      setNotice(kind === "rotate" ? "Secret rotated. The new value is hidden." : "Secret revoked. Its metadata and version history are retained.");
      await Promise.all([client.invalidateQueries({ queryKey: detailKey(secret.id) }), client.invalidateQueries({ queryKey: versionsKey(secret.id) }), onChanged()]);
    } catch (cause) {
      setError(secretError(cause));
      if (cause instanceof ApiError && cause.status === 409) { setConflict(true); await loadCurrent(); }
    } finally { setValue(""); setBusy(false); }
  }
  return <section className="min-w-0 space-y-4 rounded-xl border p-4" aria-label={`Secret ${secret.name}`}>
    <div className="flex flex-wrap items-start justify-between gap-2"><h3 className="min-w-0 flex-1 break-words font-semibold [overflow-wrap:anywhere]">{secret.name}</h3><Button type="button" variant="outline" onClick={onClose}>Close secret</Button></div>
    <p className="text-sm">{secret.status === "revoked" ? "Revoked" : "Active"} · Current version {secret.current_version} · Values hidden</p>
    {metadata.isError ? <div className="space-y-2"><p role="alert">Unable to refresh secret metadata. Changes and reveal are paused.</p><Button type="button" variant="outline" onClick={() => void metadata.refetch()}>Retry secret metadata</Button></div> : null}
    {error ? <p role="alert">{error} Sensitive input was cleared; re-enter a value to retry a rotation.</p> : null}
    {notice ? <p role="status">{notice}</p> : null}
    {conflict ? <section className="space-y-2 rounded-lg border p-3" aria-label="Secret version conflict">
      <p>Your action was not retried. Review the current secret before trying again.</p>
      {current ? <><p>Current version {current.current_version} · {current.status}</p><Button type="button" variant="outline" disabled={busy || unavailable || current.status !== "active"} onClick={() => { setExpected(current.current_version); setConflict(false); setConfirmed(false); setError(""); }}>Use current version {current.current_version}</Button></> : null}
      {refreshError ? <p role="alert">{refreshError}</p> : null}
      <Button type="button" variant="outline" disabled={busy || metadata.isFetching} onClick={() => void loadCurrent()}>Refresh current secret</Button>
    </section> : null}
    {writable && active ? <form aria-label="Rotate secret" autoComplete="off" onSubmit={(event) => void update("rotate", event)} className="space-y-3 border-t pt-3">
      <label htmlFor={`${id}-value`}>New secret value</label><Input id={`${id}-value`} type="password" autoComplete="new-password" value={value} onChange={(event) => setValue(event.target.value)} disabled={busy || unavailable} required />
      <p className="text-xs text-muted-foreground">Creates an immutable version. The value stays hidden and is cleared after this request.</p>
      <Button type="submit" disabled={busy || !canWrite || conflict}>{busy ? "Updating secret..." : "Rotate secret"}</Button>
    </form> : null}
    <section className="space-y-2" aria-label="Secret version history"><h4 className="font-medium">Version history</h4>
      {versions.isError ? <div className="space-y-2"><p role="alert">Unable to load version metadata.</p><Button type="button" variant="outline" onClick={() => void versions.refetch()}>Retry secret versions</Button></div> : null}
      {versions.isPending ? <p role="status">Loading versions...</p> : null}
      {versions.data?.items.map((version) => <div key={version.version} className="flex flex-wrap items-center justify-between gap-2 rounded-lg border p-2"><p className="text-sm">Version {version.version}</p>{canReveal ? <Button type="button" size="sm" variant="outline" onClick={() => { setValue(""); setRevealVersion(version.version); }}>Select version {version.version} to reveal</Button> : null}</div>)}
      <PageNavigation label="secret versions" page={versions.data} loading={versions.isFetching} onFirst={() => setOffset(0)} onPrevious={() => setOffset(Math.max(0, offset - pageSize))} onNext={() => setOffset(Math.min(maximumPageOffset, offset + pageSize))} />
    </section>
    {canReveal && revealVersion !== undefined ? <SecretReveal key={`${secret.id}:${revealVersion}`} secretID={secret.id} version={revealVersion} onClose={() => setRevealVersion(undefined)} /> : null}
    {owner && active ? <form aria-label={`Revoke ${secret.name}`} onSubmit={(event) => void update("revoke", event)} className="space-y-3 border-t pt-3">
      <p className="break-words text-sm [overflow-wrap:anywhere]">Revoking {secret.name} is irreversible. All its versions become unavailable for reveal. Metadata and history remain.</p>
      <label className="flex items-start gap-2"><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} disabled={busy || unavailable} /><span className="min-w-0 break-words [overflow-wrap:anywhere]">I understand that revoking {secret.name} is irreversible.</span></label>
      <Button type="submit" variant="destructive" className="h-auto max-w-full whitespace-normal" disabled={busy || !canWrite || conflict || !confirmed}><span className="min-w-0 break-all">Revoke {secret.name}</span></Button>
    </form> : null}
  </section>;
}

function ProjectSecretList({ projectID, writable, owner, blocked }: { projectID: string; writable: boolean; owner: boolean; blocked: boolean }) {
  const client = useQueryClient();
  const id = useId();
  const [offset, setOffset] = useState(0);
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [selected, setSelected] = useState<ManagedSecret>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const secrets = useQuery({ queryKey: [...listKey(projectID), offset], queryFn: ({ signal }) => listManagedSecrets({ project_id: projectID, limit: pageSize, offset }, { signal }), retry: false });
  const unavailable = blocked || secrets.isError || secrets.isPending;
  const refresh = async () => { await client.invalidateQueries({ queryKey: listKey(projectID) }); };
  async function create(event: FormEvent) {
    event.preventDefault();
    if (!writable || unavailable || busy) return;
    setBusy(true); setError(""); setNotice("");
    try {
      const created = await createManagedSecret({ project_id: projectID, name, value });
      setName(""); setValue(""); setSelected(created); setOffset(0); setNotice("Secret created. Its value is hidden.");
      await refresh();
    } catch (cause) {
      setError(secretError(cause));
      if (cause instanceof ApiError && cause.status === 409) {
        await refresh();
        setError("The name is already reserved or the project changed. Review the refreshed metadata before creating another secret.");
      }
    }
    finally { setValue(""); setBusy(false); }
  }
  return <div className="grid min-w-0 items-start gap-4 lg:grid-cols-2">
    <section className="min-w-0 space-y-3" aria-label="Managed secret metadata">
      {secrets.isPending ? <p role="status">Loading secret metadata...</p> : null}
      {secrets.isError ? <div className="space-y-2"><p role="alert">Unable to load secret metadata. {secretError(secrets.error)}</p><Button type="button" variant="outline" onClick={() => void secrets.refetch()}>Retry managed secrets</Button></div> : null}
      {secrets.data?.items.length === 0 ? <p>No managed secrets in this project.</p> : null}
      {secrets.data?.items.map((secret) => <article className="min-w-0 space-y-2 rounded-xl border p-3" key={secret.id}><h3 className="break-words font-medium [overflow-wrap:anywhere]">{secret.name}</h3><p className="text-sm">{secret.status === "revoked" ? "Revoked" : "Active"} · Version {secret.current_version} · Value hidden</p><Button type="button" variant="outline" className="h-auto max-w-full whitespace-normal" onClick={() => { setValue(""); setSelected(secret); setNotice(""); }}><span className="min-w-0 break-all">Manage {secret.name}</span></Button></article>)}
      <PageNavigation label="managed secrets" page={secrets.data} loading={secrets.isFetching || busy} onFirst={() => setOffset(0)} onPrevious={() => setOffset(Math.max(0, offset - pageSize))} onNext={() => setOffset(Math.min(maximumPageOffset, offset + pageSize))} />
      {writable ? <form autoComplete="off" aria-label="Create managed secret" onSubmit={(event) => void create(event)} className="space-y-3 border-t pt-3">
        <h3 className="font-medium">Create managed secret</h3>
        <label htmlFor={`${id}-name`}>Secret name</label><Input id={`${id}-name`} value={name} onChange={(event) => setName(event.target.value)} required disabled={busy} />
        <label htmlFor={`${id}-value`}>Secret value</label><Input id={`${id}-value`} type="password" autoComplete="new-password" value={value} onChange={(event) => setValue(event.target.value)} required disabled={busy} />
        <p className="text-xs text-muted-foreground">Stored encrypted. Values stay hidden and are cleared after each request. Use 1 to 65536 UTF-8 bytes; whitespace is preserved.</p>
        <Button type="submit" disabled={busy || unavailable}>{busy ? "Creating secret..." : "Create secret"}</Button>
        {error ? <p role="alert">{error} Your name is preserved. Re-enter the value to retry.</p> : null}
      </form> : <p className="text-sm text-muted-foreground">You have metadata access. Maintainers can create and rotate secrets. Owners can reveal and revoke them.</p>}
      {notice ? <p role="status">{notice}</p> : null}
    </section>
    {selected ? <SecretDetail key={selected.id} initial={selected} writable={writable} owner={owner} blocked={unavailable} onChanged={refresh} onClose={() => setSelected(undefined)} /> : <p className="text-sm text-muted-foreground">Select a secret to view its immutable version history.</p>}
  </div>;
}

export function ProjectSecrets({ projects, principal, members, unavailable = false }: { projects: Project[]; principal?: Principal; members: ProjectMember[]; unavailable?: boolean }) {
  const id = useId();
  const [projectID, setProjectID] = useState("");
  const project = projects.find((item) => item.id === projectID);
  const owner = Boolean(principal && (principal.roles.includes("system_admin") || members.some((member) => member.project_id === projectID && member.user_id === principal.id && member.role === "owner")));
  return <Card role="region" aria-label="Project managed secrets" className="min-w-0"><CardHeader><CardTitle>Project managed secrets</CardTitle><p className="text-sm text-muted-foreground">Store encrypted, versioned project secrets. Workload injection is not available yet. Storage requires the server's configured secret keyring.</p></CardHeader><CardContent className="min-w-0 space-y-4">
    <div className="max-w-xl space-y-2"><label htmlFor={`${id}-project`}>Secrets project</label><select id={`${id}-project`} className="h-10 w-full min-w-0 rounded-xl border border-border bg-card px-2 text-sm" value={project?.id ?? ""} onChange={(event) => setProjectID(event.target.value)}><option value="">Select a project</option>{projects.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></div>
    {project ? <ProjectSecretList key={project.id} projectID={project.id} writable={canMaintainProject(principal, members, project.id)} owner={owner} blocked={unavailable} /> : <p className="text-sm text-muted-foreground">Choose a project to view managed secrets.</p>}
  </CardContent></Card>;
}
