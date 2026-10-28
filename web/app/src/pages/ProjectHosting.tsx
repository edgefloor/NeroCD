import { DatabaseBackups, ResourceComposeDatabaseBackups } from "./DatabaseBackups";
import { useDatabaseAccess } from "./ResourceDatabase";
import { getHostingDatabase, validateDatabase } from "@/api/hosting-databases";
import { getNotificationRecoveryTransfer, getNotificationRecoveryBackup, parseBackupRecoveryLocator, type BackupRecoveryLocator } from "@/api/hosting-backup-notifications";
import { type FormEvent, useId, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, type Project } from "@/api";
import { createProjectEnvironment, createResource, getResource, listProjectEnvironments, listResources, updateResource, type ProjectEnvironment, type Resource, type ResourceSourceInput } from "@/api/hosting";
import { PageNavigation } from "@/components/common/PageNavigation";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { maximumPageOffset } from "@/router/search";
import { ResourceDestination } from "./ResourceDestination";
import { ResourceRoutes } from "./ResourceRoutes";
import { GitApplicationFields, emptyGitDraft, gitBuildVariablesError, type GitApplicationDraft } from "./GitApplicationFields";
import { ResourceBuilds } from "./ResourceBuilds";
import { ResourceDatabaseConnections } from "./ResourceDatabaseConnections";
import { ResourceRuntimeConfiguration } from "./ResourceRuntimeConfiguration";
import { ResourceRuntimeStatus } from "./ResourceRuntimeStatus";
import { ResourceRuntimeLogs } from "./ResourceRuntimeLogs";

import { ResourceStorage } from "./ResourceStorage";
import { ResourceDatabase } from "./ResourceDatabase";
import { HostingCatalog } from "./HostingCatalog";
import { DockerfileApplicationFields, dockerfileContentError, emptyDockerfileDraft, type DockerfileApplicationDraft } from "./DockerfileApplicationFields";
import { ComposeApplicationFields, emptyComposeDraft, type ComposeApplicationDraft } from "./ComposeApplicationFields";
import { DatabaseCreateForm } from "./DatabaseCreateForm";

const pageSize = 20;
const selectClass = "h-10 w-full min-w-0 rounded-xl border border-border bg-card px-2 text-sm";
const environmentKey = (projectID: string) => ["projectEnvironments", projectID] as const;
const resourceKey = (projectID: string) => ["hostingResources", projectID] as const;
function errorText(error: unknown): string {
  if (error instanceof ApiError) return `${error.message}${error.requestID ? ` (request ${error.requestID})` : ""}`;
  return "The request could not be completed. Check your connection and try again.";
}

function sourceDescription(resource: Resource): string {
  if (resource.source.kind === "image") return resource.source.image.reference;
  if (resource.source.kind === "compose") return `Compose stack · public component ${resource.source.compose.public_component}`;
  if (resource.source.kind === "dockerfile") return `Dockerfile · source generation ${resource.source.dockerfile.generation}`;
  return `${resource.source.git.repository_url} · ${resource.source.git.requested_ref}`;
}

function ApplicationFields({ name, image, setName, setImage, disabled, showImage = true }: {
  name: string; image: string; setName: (value: string) => void; setImage: (value: string) => void; disabled: boolean; showImage?: boolean;
}) {
  const id = useId();
  return <>
    <div className="space-y-1.5"><label htmlFor={`${id}-name`}>Application name</label><Input id={`${id}-name`} value={name} onChange={(event) => setName(event.target.value)} disabled={disabled} required /></div>
    {showImage ? <div className="space-y-1.5"><label htmlFor={`${id}-image`}>Image reference</label><Input id={`${id}-image`} value={image} onChange={(event) => setImage(event.target.value)} disabled={disabled} required placeholder="registry.example.com/team/app:1.0" spellCheck={false} /><p className="text-xs text-muted-foreground">Use an explicit image tag or sha256 digest. Saving does not pull or run this image.</p></div> : null}
  </>;
}

function ApplicationEditor({ resource, writable, onClose, onSaved }: { resource: Resource; writable: boolean; onClose: () => void; onSaved: () => Promise<void> }) {
  const [base, setBase] = useState(resource);
  const [dockerfile, setDockerfile] = useState<DockerfileApplicationDraft>(resource.source.kind === "dockerfile" ? {content: resource.source.dockerfile.content, settings: resource.source.dockerfile.settings} : emptyDockerfileDraft());
  const [dockerfileSecretsValid, setDockerfileSecretsValid] = useState(false);
  const [dockerfileRegistryValid, setDockerfileRegistryValid] = useState(resource.source.kind !== "dockerfile" || !resource.source.dockerfile.settings.build_registry_credential);
  const [gitSecretsValid, setGitSecretsValid] = useState(false);
  const [gitRegistryValid, setGitRegistryValid] = useState(resource.source.kind !== "git" || !resource.source.git.settings.build_registry_credential);
  const [name, setName] = useState(resource.name);
  const [compose, setCompose] = useState<ComposeApplicationDraft>(resource.source.kind === "compose" ? resource.source.compose : emptyComposeDraft());
  const [image, setImage] = useState(resource.source.kind === "image" ? resource.source.image.reference : "");
  const [git, setGit] = useState<GitApplicationDraft>(resource.source.kind === "git" ? { repository_url: resource.source.git.repository_url, requested_ref: resource.source.git.requested_ref, credential_kind: resource.source.git.credential_kind as GitApplicationDraft["credential_kind"], ...(resource.source.git.ssh_host_fingerprints ? { ssh_host_fingerprints: resource.source.git.ssh_host_fingerprints } : {}), settings: resource.source.git.settings } : emptyGitDraft());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [latest, setLatest] = useState<Resource>();
  const [refreshError, setRefreshError] = useState("");
  const baseInput: ResourceSourceInput = base.source.kind === "dockerfile" ? {kind:"dockerfile",dockerfile:{content:base.source.dockerfile.content,settings:base.source.dockerfile.settings}} : base.source.kind !== "git" ? base.source : { kind: "git", git: { repository_url: base.source.git.repository_url, requested_ref: base.source.git.requested_ref, credential_kind: base.source.git.credential_kind as GitApplicationDraft["credential_kind"], ...(base.source.git.ssh_host_fingerprints ? { ssh_host_fingerprints: base.source.git.ssh_host_fingerprints } : {}), settings: base.source.git.settings } };
  const source: ResourceSourceInput = base.source.kind === "image" ? { kind: "image", image: { reference: image } } : base.source.kind === "compose" ? {kind: "compose", compose} : base.source.kind === "dockerfile" ? {kind:"dockerfile",dockerfile} : { kind: "git", git };
  const sourceChanged = JSON.stringify(source) !== JSON.stringify(baseInput);
  const changed = name !== base.name || sourceChanged;

  async function loadLatest() {
    setBusy(true);
    setRefreshError("");
    try {
      const current = await getResource(resource.id);
      setLatest(current);
    } catch (cause) { setRefreshError(errorText(cause)); }
    finally { setBusy(false); }
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    if (busy || !writable || conflict || !changed || (base.source.kind === "git" && (!gitRegistryValid || !gitSecretsValid) || base.source.kind === "dockerfile" && (!dockerfileRegistryValid || !dockerfileSecretsValid))) return;
    const problem = base.source.kind === "git" ? gitBuildVariablesError(git.settings) : base.source.kind === "dockerfile" ? dockerfileContentError(dockerfile.content) || gitBuildVariablesError(dockerfile.settings) : "";
    if (problem) { setError(problem); return; }
    setBusy(true);
    setError("");
    try {

      await updateResource(resource.id, name !== base.name ? {
        expected_generation: base.generation,
        name,
        ...(sourceChanged ? { source } : {}),
      } : { expected_generation: base.generation, source });
      await onSaved();
      onClose();
    } catch (cause) {
      setError(errorText(cause));
      if (cause instanceof ApiError && cause.status === 409) {
        setConflict(true);
        setLatest(undefined);
        setRefreshError("");
        try { setLatest(await getResource(resource.id)); }
        catch (refreshCause) { setRefreshError(errorText(refreshCause)); }
      }
    } finally { setBusy(false); }
  }
  return <form onSubmit={(event) => void save(event)} className="space-y-3 rounded-xl border p-3" aria-label={`Edit ${resource.name}`} aria-busy={busy}>
    <h4 className="font-medium">Edit application configuration</h4>
    <ApplicationFields name={name} image={image} setName={setName} setImage={setImage} disabled={busy || !writable} showImage={base.source.kind === "image"} />
    {base.source.kind === "dockerfile" ? <DockerfileApplicationFields value={dockerfile} onChange={setDockerfile} disabled={busy || !writable} projectID={resource.project_id} onRegistryValidityChange={setDockerfileRegistryValid} onSecretsValidityChange={setDockerfileSecretsValid} /> : null}
    {base.source.kind === "compose" ? <ComposeApplicationFields value={compose} onChange={setCompose} disabled={busy || !writable} /> : null}
    {base.source.kind === "git" ? <GitApplicationFields value={git} onChange={setGit} disabled={busy || !writable} projectID={resource.project_id} onRegistryValidityChange={setGitRegistryValid} onSecretsValidityChange={setGitSecretsValid} /> : null}
    {error ? <p role="alert" className="break-words [overflow-wrap:anywhere]">Unable to save application. {error}</p> : null}
    {conflict ? <section className="space-y-2 rounded-lg border p-3" aria-label="Generation conflict">
      <p role="status">The saved configuration changed. Your draft is preserved. Review the current values before saving again.</p>
      {latest ? <>
        <dl className="space-y-1 break-words text-sm [overflow-wrap:anywhere]"><dt>Current generation</dt><dd>{latest.generation}</dd><dt>Current name</dt><dd>{latest.name}</dd><dt>Current source</dt><dd>{sourceDescription(latest)}</dd></dl>
        {latest.source.kind === "dockerfile" ? <section aria-label="Current Dockerfile configuration"><dl className="min-w-0 space-y-1 break-words text-sm [overflow-wrap:anywhere]"><dt>Current source generation</dt><dd>{latest.source.dockerfile.generation}</dd><dt>Current Dockerfile content</dt><dd><pre className="max-h-64 overflow-auto whitespace-pre-wrap">{latest.source.dockerfile.content}</pre></dd><dt>Current build settings</dt><dd><pre className="max-h-64 overflow-auto whitespace-pre-wrap">{JSON.stringify(latest.source.dockerfile.settings, null, 2)}</pre></dd></dl></section> : null}
        {latest.source.kind === "git" ? <section aria-label="Current Git configuration">
          <dl className="min-w-0 space-y-1 break-words text-sm [overflow-wrap:anywhere]">
            <dt>Current repository URL</dt><dd>{latest.source.git.repository_url}</dd>
            <dt>Current requested ref</dt><dd>{latest.source.git.requested_ref}</dd>
            <dt>Current credential kind</dt><dd>{latest.source.git.credential_kind}</dd>{latest.source.git.credential_kind === "ssh_private_key" ? <><dt>Current SSH host fingerprints</dt><dd>{latest.source.git.ssh_host_fingerprints?.join(", ")}</dd></> : null}
            <dt>Current source ID</dt><dd>{latest.source.git.source_id}</dd>
            <dt>Current source generation</dt><dd>{latest.source.git.generation}</dd>
            <dt>Current build strategy</dt><dd>{latest.source.git.settings.strategy}</dd>
            <dt>Current context directory</dt><dd>{latest.source.git.settings.context_dir || "Repository root (default)"}</dd>
            <dt>Current Dockerfile path</dt><dd>{latest.source.git.settings.dockerfile_path || (latest.source.git.settings.strategy === "dockerfile" ? "Dockerfile (default)" : "Not applicable")}</dd>
            <dt>Current build target</dt><dd>{latest.source.git.settings.build_target || (latest.source.git.settings.strategy === "dockerfile" ? "Final stage (default)" : "Not applicable")}</dd>
            <dt>Current Compose file</dt><dd>{latest.source.git.settings.compose_file || "Not applicable"}</dd>
            <dt>Current public Compose service</dt><dd>{latest.source.git.settings.public_component || "Not applicable"}</dd>
            <dt>Current public Compose service port</dt><dd>{latest.source.git.settings.container_port ?? "Not applicable"}</dd>
            <dt>Current Dockerfile ARG injection</dt><dd>{latest.source.git.settings.strategy === "dockerfile" ? latest.source.git.settings.inject_build_args_to_dockerfile === false ? "Disabled; use source declarations" : "Enabled" : "Not applicable"}</dd>
            <dt>Current build registry credential</dt><dd>{latest.source.git.settings.build_registry_credential ? `${latest.source.git.settings.build_registry_credential.secret_id}, version ${latest.source.git.settings.build_registry_credential.version}` : "None"}</dd>
            <dt>Current build secrets</dt><dd>{latest.source.git.settings.build_secrets?.map(row => `${row.name}: ${row.secret.secret_id}, version ${row.secret.version}`).join(", ") || "None"}</dd>
            <dt>Current build variables</dt><dd>{latest.source.git.settings.build_variables?.map(variable => `${variable.name}=${JSON.stringify(variable.value)}`).join(", ") || "None"}</dd>
            <dt>Current publish directory</dt><dd>{latest.source.git.settings.publish_dir || (latest.source.git.settings.strategy === "static" ? "Selected context root (default)" : "Not applicable")}</dd>
            <dt>Current SPA fallback</dt><dd>{latest.source.git.settings.spa_fallback ? "Enabled" : "Disabled (default)"}</dd>
          </dl>
        </section> : null}
        <Button type="button" variant="outline" className="h-auto whitespace-normal" disabled={busy || !writable} onClick={() => { setBase(latest); setConflict(false); setError(""); }}>Keep my draft with generation {latest.generation}</Button>
        <p className="text-xs text-muted-foreground">This enables Save configuration. It does not save automatically.</p>
      </> : null}
      {refreshError ? <p role="alert">Unable to load current configuration. {refreshError}</p> : null}
      <Button type="button" variant="outline" disabled={busy} onClick={() => void loadLatest()}>Reload current configuration</Button>
    </section> : null}
    <div className="flex flex-wrap gap-2"><Button type="submit" disabled={busy || !writable || conflict || !changed || (base.source.kind === "git" && (!gitRegistryValid || !gitSecretsValid) || base.source.kind === "dockerfile" && (!dockerfileRegistryValid || !dockerfileSecretsValid))}>{busy ? "Saving configuration..." : "Save configuration"}</Button><Button type="button" variant="outline" disabled={busy} onClick={onClose}>Cancel edit</Button></div>
  </form>;
}

function Applications({ projectID, environment, writable, blocked }: { projectID: string; environment: ProjectEnvironment; writable: boolean; blocked: boolean }) {
  const client = useQueryClient();
  const [offset, setOffset] = useState(0);
  const [name, setName] = useState("");
  const [image, setImage] = useState("");
  const [compose, setCompose] = useState<ComposeApplicationDraft>(emptyComposeDraft());
  const [composeRequestID, setComposeRequestID] = useState(() => crypto.randomUUID().replaceAll("-", ""));
  const [gitSecretsValid, setGitSecretsValid] = useState(false);
  const [gitRegistryValid, setGitRegistryValid] = useState(true);
  const [sourceKind, setSourceKind] = useState<"image" | "git" | "compose" | "dockerfile">("image");
  const [dockerfile, setDockerfile] = useState<DockerfileApplicationDraft>(emptyDockerfileDraft());
  const [dockerfileSecretsValid, setDockerfileSecretsValid] = useState(false);
  const [dockerfileRegistryValid, setDockerfileRegistryValid] = useState(true);
  const [git, setGit] = useState<GitApplicationDraft>(emptyGitDraft());
  const sourceSelectID = useId();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [editing, setEditing] = useState<Resource>();
  const resources = useQuery({ queryKey: [...resourceKey(projectID), environment.id, offset], queryFn: ({ signal }) => listResources({ project_id: projectID, project_environment_id: environment.id, limit: pageSize, offset }, { signal }), retry: false });
  const canWrite = writable && !blocked && !resources.isError && !resources.isPending;
  const refresh = async () => { await client.invalidateQueries({ queryKey: resourceKey(projectID) }); };
  async function create(event: FormEvent) {
    event.preventDefault();
    if (!canWrite || busy || editing || (sourceKind === "git" && (!gitRegistryValid || !gitSecretsValid) || sourceKind === "dockerfile" && (!dockerfileRegistryValid || !dockerfileSecretsValid))) return;
    const problem = sourceKind === "git" ? gitBuildVariablesError(git.settings) : sourceKind === "dockerfile" ? dockerfileContentError(dockerfile.content) || gitBuildVariablesError(dockerfile.settings) : "";
    if (problem) { setError(problem); return; }
    setBusy(true); setError(""); setNotice("");
    try {
      await createResource({ ...(sourceKind === "compose" ? {request_id: composeRequestID} : {}), project_id: projectID, project_environment_id: environment.id, name, kind: "application", source: sourceKind === "image" ? { kind: "image", image: { reference: image } } : sourceKind === "compose" ? { kind: "compose", compose } : sourceKind === "dockerfile" ? {kind:"dockerfile",dockerfile} : { kind: "git", git } });
      setName(""); setImage(""); setGit(emptyGitDraft()); setDockerfile(emptyDockerfileDraft()); setCompose(emptyComposeDraft()); setComposeRequestID(crypto.randomUUID().replaceAll("-", "")); setNotice("Application configuration saved. Deployment is a separate request.");
      setOffset(0);
      await refresh();
    } catch (cause) { setError(errorText(cause)); }
    finally { setBusy(false); }
  }
  return <section className="min-w-0 space-y-4" aria-label={`Applications in ${environment.name}`}>
    <h3 className="break-words font-semibold [overflow-wrap:anywhere]">Applications in {environment.name}</h3>
    {resources.isError ? <div className="space-y-2"><p role="alert">Unable to load application configurations. Your draft is preserved.</p><Button type="button" variant="outline" disabled={resources.isFetching} onClick={() => void resources.refetch()}>Retry applications</Button></div> : null}
    {resources.isPending ? <p role="status">Loading applications...</p> : null}
    {resources.data?.items.length === 0 ? <p className="text-sm text-muted-foreground">No saved applications in this environment.</p> : null}
    <div className="space-y-3">{resources.data?.items.map((resource) => resource.kind === "database" ? <article key={resource.id} className="min-w-0 rounded-xl border p-3" aria-label={resource.name}>{resource.project_id === projectID && resource.project_environment_id === environment.id ? <ResourceDatabase resource={resource} blocked={resources.isError || resources.isPending || resources.isFetching} /> : <p role="alert">Database resource scope mismatch.</p>}</article> : <article key={resource.id} className="min-w-0 space-y-3 rounded-xl border p-3" aria-label={resource.name}>
      <h4 className="break-words font-medium [overflow-wrap:anywhere]">{resource.name}</h4>
      <p className="break-words font-mono text-xs [overflow-wrap:anywhere]">{sourceDescription(resource)}</p>
      <p className="text-xs text-muted-foreground">Saved configuration · Generation {resource.generation}</p>
      {resource.source.kind === "git" ? <p className="text-xs text-muted-foreground">Git source generation {resource.source.git.generation} · {resource.source.git.settings.strategy} · {resource.source.git.credential_kind === "ssh_private_key" ? "Private SSH" : resource.source.git.credential_kind === "https_basic" ? "Private HTTPS" : "Public Git"} build operations available; {resource.source.git.settings.strategy === "compose" ? "prepared Compose deployment" : "artifact deployment"} is separate</p> : null}
      <ResourceDestination resource={resource} writable={writable} blocked={blocked || resources.isError} />
      <ResourceBuilds resource={resource} writable={writable} blocked={blocked || resources.isError} />
      {/* Status verifies its own viewer authority and does not depend on legacy task reads. */}
      <ResourceRuntimeStatus resource={resource} blocked={resources.isError || resources.isPending || resources.isFetching} />
      <ResourceRuntimeLogs resource={resource} blocked={resources.isError || resources.isPending || resources.isFetching} />
      <ResourceComposeDatabaseBackups resource={resource} blocked={blocked || resources.isError || resources.isPending || resources.isFetching} />
      <ResourceRuntimeConfiguration resource={resource} blocked={blocked || resources.isError || resources.isPending} />
      <ResourceStorage resource={resource} blocked={blocked || resources.isError || resources.isPending} />
      <ResourceDatabaseConnections resource={resource} writable={writable} blocked={blocked || resources.isError} />
      <ResourceRoutes resource={resource} writable={writable} blocked={blocked || resources.isError} />
      {canWrite && editing?.id !== resource.id ? <Button type="button" variant="outline" disabled={busy || Boolean(editing)} onClick={() => { setEditing(resource); setNotice(""); }}>Edit configuration</Button> : null}
      {editing?.id === resource.id ? <ApplicationEditor key={resource.id} resource={editing} writable={canWrite} onClose={() => setEditing(undefined)} onSaved={async () => { await refresh(); setNotice("Application configuration updated. Deployment is a separate request."); }} /> : null}
    </article>)}</div>
    <PageNavigation label="applications" page={resources.data} loading={resources.isFetching || Boolean(editing) || busy} onFirst={() => setOffset(0)} onPrevious={() => setOffset(Math.max(0, offset - pageSize))} onNext={() => setOffset(Math.min(maximumPageOffset, offset + pageSize))} />
    {writable ? <form onSubmit={(event) => void create(event)} aria-label="Create application" aria-busy={busy} className="space-y-3 border-t pt-4">
      <h4 className="font-medium">Add application</h4>
      <div className="space-y-1.5"><label htmlFor={sourceSelectID}>Application source</label><select id={sourceSelectID} className={selectClass} value={sourceKind} disabled={busy} onChange={(event) => setSourceKind(event.target.value as "image" | "git" | "compose" | "dockerfile")}><option value="image">Container image</option><option value="git">Git repository</option><option value="dockerfile">Dockerfile</option><option value="compose">Compose stack</option></select></div>
      <ApplicationFields name={name} image={image} setName={setName} setImage={setImage} disabled={busy} showImage={sourceKind === "image"} />
      {sourceKind === "dockerfile" ? <DockerfileApplicationFields value={dockerfile} onChange={setDockerfile} disabled={busy} projectID={projectID} onRegistryValidityChange={setDockerfileRegistryValid} onSecretsValidityChange={setDockerfileSecretsValid} /> : null}
      {sourceKind === "compose" ? <ComposeApplicationFields value={compose} onChange={setCompose} disabled={busy} /> : null}
      {sourceKind === "git" ? <GitApplicationFields key={composeRequestID} value={git} onChange={setGit} disabled={busy} projectID={projectID} onRegistryValidityChange={setGitRegistryValid} onSecretsValidityChange={setGitSecretsValid} /> : null}
      <Button type="submit" disabled={!canWrite || busy || Boolean(editing) || (sourceKind === "git" && (!gitRegistryValid || !gitSecretsValid) || sourceKind === "dockerfile" && (!dockerfileRegistryValid || !dockerfileSecretsValid))}>{busy ? "Saving application..." : "Save application"}</Button>
      {error ? <p role="alert" className="break-words [overflow-wrap:anywhere]">Unable to create application. {error}</p> : null}
    </form> : null}
    <HostingCatalog projectID={projectID} environmentID={environment.id} blocked={blocked || resources.isError || resources.isPending} />
    <DatabaseCreateForm projectID={projectID} environmentID={environment.id} blocked={blocked || resources.isError || resources.isPending || environment.project_id !== projectID} accessError={resources.isError ? resources.error : undefined} />
    {notice ? <p role="status">{notice}</p> : null}
  </section>;
}

function ProjectEnvironments({ projectID, writable, blocked }: { projectID: string; writable: boolean; blocked: boolean }) {
  const client = useQueryClient();
  const id = useId();
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<ProjectEnvironment>();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const environments = useQuery({ queryKey: [...environmentKey(projectID), offset], queryFn: ({ signal }) => listProjectEnvironments({ project_id: projectID, limit: pageSize, offset }, { signal }), retry: false });
  async function create(event: FormEvent) {
    event.preventDefault();
    if (!writable || blocked || busy || environments.isError || environments.isPending) return;
    setBusy(true); setError("");
    try {
      const created = await createProjectEnvironment({ project_id: projectID, name });
      setName(""); setSelected(created); setOffset(0);
      await client.invalidateQueries({ queryKey: environmentKey(projectID) });
    } catch (cause) { setError(errorText(cause)); }
    finally { setBusy(false); }
  }
  return <div className="grid min-w-0 items-start gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
    <section className="min-w-0 space-y-3" aria-label="Shared environments">
      <h3 className="font-semibold">Shared environments</h3>
      {environments.isPending ? <p role="status">Loading environments...</p> : null}
      {environments.isError ? <div className="space-y-2"><p role="alert">Unable to load shared environments. Your draft is preserved.</p><Button type="button" variant="outline" disabled={environments.isFetching} onClick={() => void environments.refetch()}>Retry environments</Button></div> : null}
      {environments.data?.items.length === 0 ? <p className="text-sm text-muted-foreground">No shared environments yet.</p> : null}
      <div className="flex flex-wrap gap-2">{environments.data?.items.map((environment) => <Button key={environment.id} type="button" className="h-auto max-w-full whitespace-normal break-words [overflow-wrap:anywhere]" variant={selected?.id === environment.id ? "default" : "outline"} aria-pressed={selected?.id === environment.id} onClick={() => setSelected(environment)}>{environment.name}</Button>)}</div>
      <PageNavigation label="shared environments" page={environments.data} loading={environments.isFetching || busy} onFirst={() => setOffset(0)} onPrevious={() => setOffset(Math.max(0, offset - pageSize))} onNext={() => setOffset(Math.min(maximumPageOffset, offset + pageSize))} />
      {writable ? <form onSubmit={(event) => void create(event)} aria-label="Create shared environment" aria-busy={busy} className="space-y-3 border-t pt-4">
        <label htmlFor={`${id}-environment`}>Environment name</label><Input id={`${id}-environment`} value={name} onChange={(event) => setName(event.target.value)} disabled={busy} required placeholder="Production" />
        <Button type="submit" disabled={blocked || busy || environments.isError || environments.isPending} className="h-auto whitespace-normal">{busy ? "Creating environment..." : "Create shared environment"}</Button>
        {error ? <p role="alert" className="break-words [overflow-wrap:anywhere]">Unable to create environment. {error}</p> : null}
      </form> : <p className="text-sm text-muted-foreground">You have read access. A project owner, maintainer, or administrator can save configuration.</p>}
    </section>
    {selected ? <Applications key={selected.id} projectID={projectID} environment={selected} writable={writable} blocked={blocked || environments.isError} /> : <p className="text-sm text-muted-foreground">Select a shared environment to view its applications.</p>}
  </div>;
}

function BackupRecovery({ locator, blocked }: { locator: BackupRecoveryLocator; blocked: boolean }) {
  const access = useDatabaseAccess(locator.project_id, locator.environment_id);
  const [revoked, setRevoked] = useState(false);
  const resolved = useQuery({
    queryKey: ["backup-notification-recovery", access.epoch, locator],
    enabled: access.ready && access.viewer && !access.refreshing && !blocked && !revoked,
    retry: false, gcTime: 0,
    queryFn: async ({ signal }) => {
      // Resolve every scope through authenticated metadata before rendering actions.
      let environmentFound = false;
      for (let offset = 0; offset < 10000; offset += 100) {
        const page = await listProjectEnvironments({ project_id: locator.project_id, limit: 100, offset }, { signal });
        if (page.items.some(e => e.id === locator.environment_id && e.project_id === locator.project_id)) { environmentFound = true; break; }
        if (offset + page.items.length >= page.total || !page.items.length) break;
      }
      if (!environmentFound) throw new Error("Recovery environment is unavailable");
      if (locator.source_kind) {
        const resource=await getResource(locator.resource_id,{signal});
        if(resource.id!==locator.resource_id||resource.project_id!==locator.project_id||resource.project_environment_id!==locator.environment_id||resource.kind!=="application"||resource.source.kind!=="image") throw new Error("Storage recovery resource mismatch");
        return {kind:"storage" as const,resource};
      }
      const database = validateDatabase(await getHostingDatabase(locator.resource_id, { signal }), access.scope, locator.resource_id);
      const backup = await getNotificationRecoveryBackup(access.scope, database, locator.backup_id, signal);
      const transfer = locator.transfer_id ? await getNotificationRecoveryTransfer(access.scope, backup.id, locator.transfer_id, signal) : undefined;
      return { kind:"database" as const, database, backup, transfer };
    },
  });
  if (!access.ready || access.refreshing || blocked) return <p role="status">Verifying authenticated backup recovery access...</p>;
  if (!access.viewer || revoked || resolved.isError) return <section aria-label="Backup notification recovery"><p role="alert">Backup recovery is unavailable for this project, environment, resource, or backup. Refresh project access and open the link again.</p></section>;
  return <section aria-label="Backup notification recovery" className="min-w-0 space-y-3 rounded-xl border p-3">
    <h3 className="font-semibold">Backup notification recovery</h3>
    <p className="break-all">Backup {locator.backup_id}{locator.transfer_id ? ` · upload ${locator.transfer_id}` : ""}. The link selects history only. Review current state before requesting an upload retry or a restore.</p>
    {resolved.isPending ? <p role="status">Resolving backup history...</p> : null}
    {resolved.data?.kind === "storage" ? <ResourceStorage resource={resolved.data.resource} blocked={blocked || access.refreshing} recoveryBackup={locator.backup_id} /> : null}
    {resolved.data?.kind === "database" ? <DatabaseBackups database={resolved.data.database} recoveryBackup={resolved.data.backup} recoveryTransfer={resolved.data.transfer} scope={access.scope} owner={access.owner} blocked={blocked || access.refreshing} onDenied={() => setRevoked(true)} recovery={locator} /> : null}
  </section>;
}

export function ProjectHosting({ projects, canOperateProject, unavailable = false, recoverySearch = {} }: { projects: Project[]; canOperateProject: (projectID: string) => boolean; unavailable?: boolean; recoverySearch?: Record<string, unknown> }) {
  const id = useId();
  const [projectID, setProjectID] = useState("");
  const project = projects.find((item) => item.id === projectID);
  const locator = parseBackupRecoveryLocator(recoverySearch);
  return <Card className="min-w-0" role="region" aria-label="Project hosting">
    <CardHeader><CardTitle>Project hosting</CardTitle><p className="text-sm text-muted-foreground">Save application or database configuration in shared project environments. Database metadata does not start an engine. Choose application placement and request image deployment separately. Operation history records outcomes; it does not provide continuous runtime health.</p></CardHeader>
    <CardContent className="min-w-0 space-y-5">
      {locator ? projects.some(p => p.id === locator.project_id) ? <BackupRecovery key={JSON.stringify(locator)} locator={locator} blocked={unavailable} /> : <p role="alert">Backup recovery project is unavailable.</p> : null}
      <div className="max-w-xl space-y-1.5"><label htmlFor={`${id}-project`}>Hosting project</label><select id={`${id}-project`} className={selectClass} value={project?.id ?? ""} onChange={(event) => setProjectID(event.target.value)}><option value="">Select a project</option>{projects.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></div>
      {project ? <><p className="break-all text-xs text-muted-foreground">Project ID for destination grants: {project.id}</p><ProjectEnvironments key={project.id} projectID={project.id} writable={canOperateProject(project.id)} blocked={unavailable} /></> : <p className="text-sm text-muted-foreground">Choose a project to manage or view saved configuration.</p>}
    </CardContent>
  </Card>;
}
