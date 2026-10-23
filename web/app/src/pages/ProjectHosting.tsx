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
import { GitApplicationFields, emptyGitDraft, type GitApplicationDraft } from "./GitApplicationFields";
import { ResourceBuilds } from "./ResourceBuilds";
import { ResourceDeployments } from "./ResourceDeployments";
import { ResourceRuntimeConfiguration } from "./ResourceRuntimeConfiguration";
import { ResourceRuntimeStatus } from "./ResourceRuntimeStatus";
import { ResourceRuntimeLogs } from "./ResourceRuntimeLogs";

const pageSize = 20;
const selectClass = "h-10 w-full min-w-0 rounded-xl border border-border bg-card px-2 text-sm";
const environmentKey = (projectID: string) => ["projectEnvironments", projectID] as const;
const resourceKey = (projectID: string) => ["hostingResources", projectID] as const;
function errorText(error: unknown): string {
  if (error instanceof ApiError) return `${error.message}${error.requestID ? ` (request ${error.requestID})` : ""}`;
  return "The request could not be completed. Check your connection and try again.";
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
  const [name, setName] = useState(resource.name);
  const [image, setImage] = useState(resource.source.kind === "image" ? resource.source.image.reference : "");
  const [git, setGit] = useState<GitApplicationDraft>(resource.source.kind === "git" ? { repository_url: resource.source.git.repository_url, requested_ref: resource.source.git.requested_ref, credential_kind: resource.source.git.credential_kind as GitApplicationDraft["credential_kind"], settings: resource.source.git.settings } : emptyGitDraft());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [latest, setLatest] = useState<Resource>();
  const [refreshError, setRefreshError] = useState("");
  const baseInput: ResourceSourceInput = base.source.kind === "image" ? base.source : { kind: "git", git: { repository_url: base.source.git.repository_url, requested_ref: base.source.git.requested_ref, credential_kind: base.source.git.credential_kind as GitApplicationDraft["credential_kind"], settings: base.source.git.settings } };
  const source: ResourceSourceInput = base.source.kind === "image" ? { kind: "image", image: { reference: image } } : { kind: "git", git };
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
    if (busy || !writable || conflict || !changed) return;
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
    {base.source.kind === "git" ? <GitApplicationFields value={git} onChange={setGit} disabled={busy || !writable} /> : null}
    {error ? <p role="alert" className="break-words [overflow-wrap:anywhere]">Unable to save application. {error}</p> : null}
    {conflict ? <section className="space-y-2 rounded-lg border p-3" aria-label="Generation conflict">
      <p role="status">The saved configuration changed. Your draft is preserved. Review the current values before saving again.</p>
      {latest ? <>
        <dl className="space-y-1 break-words text-sm [overflow-wrap:anywhere]"><dt>Current generation</dt><dd>{latest.generation}</dd><dt>Current name</dt><dd>{latest.name}</dd><dt>Current source</dt><dd>{latest.source.kind === "image" ? latest.source.image.reference : `${latest.source.git.repository_url} at ${latest.source.git.requested_ref} · source generation ${latest.source.git.generation}`}</dd></dl>
        {latest.source.kind === "git" ? <section aria-label="Current Git configuration">
          <dl className="min-w-0 space-y-1 break-words text-sm [overflow-wrap:anywhere]">
            <dt>Current repository URL</dt><dd>{latest.source.git.repository_url}</dd>
            <dt>Current requested ref</dt><dd>{latest.source.git.requested_ref}</dd>
            <dt>Current credential kind</dt><dd>{latest.source.git.credential_kind}</dd>
            <dt>Current source ID</dt><dd>{latest.source.git.source_id}</dd>
            <dt>Current source generation</dt><dd>{latest.source.git.generation}</dd>
            <dt>Current build strategy</dt><dd>{latest.source.git.settings.strategy}</dd>
            <dt>Current context directory</dt><dd>{latest.source.git.settings.context_dir || "Repository root (default)"}</dd>
            <dt>Current Dockerfile path</dt><dd>{latest.source.git.settings.dockerfile_path || (latest.source.git.settings.strategy === "dockerfile" ? "Dockerfile (default)" : "Not applicable")}</dd>
            <dt>Current build target</dt><dd>{latest.source.git.settings.build_target || (latest.source.git.settings.strategy === "dockerfile" ? "Final stage (default)" : "Not applicable")}</dd>
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
    <div className="flex flex-wrap gap-2"><Button type="submit" disabled={busy || !writable || conflict || !changed}>{busy ? "Saving configuration..." : "Save configuration"}</Button><Button type="button" variant="outline" disabled={busy} onClick={onClose}>Cancel edit</Button></div>
  </form>;
}

function Applications({ projectID, environment, writable, blocked }: { projectID: string; environment: ProjectEnvironment; writable: boolean; blocked: boolean }) {
  const client = useQueryClient();
  const [offset, setOffset] = useState(0);
  const [name, setName] = useState("");
  const [image, setImage] = useState("");
  const [sourceKind, setSourceKind] = useState<"image" | "git">("image");
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
    if (!canWrite || busy || editing) return;
    setBusy(true); setError(""); setNotice("");
    try {
      await createResource({ project_id: projectID, project_environment_id: environment.id, name, kind: "application", source: sourceKind === "image" ? { kind: "image", image: { reference: image } } : { kind: "git", git } });
      setName(""); setImage(""); setGit(emptyGitDraft()); setNotice("Application configuration saved. Deployment is a separate request.");
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
    <div className="space-y-3">{resources.data?.items.map((resource) => <article key={resource.id} className="min-w-0 space-y-3 rounded-xl border p-3" aria-label={resource.name}>
      <h4 className="break-words font-medium [overflow-wrap:anywhere]">{resource.name}</h4>
      <p className="break-words font-mono text-xs [overflow-wrap:anywhere]">{resource.source.kind === "image" ? resource.source.image.reference : `${resource.source.git.repository_url} · ${resource.source.git.requested_ref}`}</p>
      <p className="text-xs text-muted-foreground">Saved configuration · Generation {resource.generation}</p>
      {resource.source.kind === "git" ? <p className="text-xs text-muted-foreground">Git source generation {resource.source.git.generation} · {resource.source.git.settings.strategy} · {resource.source.git.credential_kind === "https_basic" ? "Private HTTPS" : "Public Git"} build operations available; artifact deployment is separate</p> : null}
      <ResourceDestination resource={resource} writable={writable} blocked={blocked || resources.isError} />
      <ResourceBuilds resource={resource} writable={writable} blocked={blocked || resources.isError} />
      {/* Status verifies its own viewer authority and does not depend on legacy task reads. */}
      <ResourceRuntimeStatus resource={resource} blocked={resources.isError || resources.isPending || resources.isFetching} />
      <ResourceRuntimeLogs resource={resource} blocked={resources.isError || resources.isPending || resources.isFetching} />
      <ResourceRuntimeConfiguration resource={resource} blocked={blocked || resources.isError || resources.isPending} />
      <ResourceDeployments resource={resource} writable={writable} blocked={blocked || resources.isError} />
      <ResourceRoutes resource={resource} writable={writable} blocked={blocked || resources.isError} />
      {canWrite && editing?.id !== resource.id ? <Button type="button" variant="outline" disabled={busy || Boolean(editing)} onClick={() => { setEditing(resource); setNotice(""); }}>Edit configuration</Button> : null}
      {editing?.id === resource.id ? <ApplicationEditor key={resource.id} resource={editing} writable={canWrite} onClose={() => setEditing(undefined)} onSaved={async () => { await refresh(); setNotice("Application configuration updated. Deployment is a separate request."); }} /> : null}
    </article>)}</div>
    <PageNavigation label="applications" page={resources.data} loading={resources.isFetching || Boolean(editing) || busy} onFirst={() => setOffset(0)} onPrevious={() => setOffset(Math.max(0, offset - pageSize))} onNext={() => setOffset(Math.min(maximumPageOffset, offset + pageSize))} />
    {writable ? <form onSubmit={(event) => void create(event)} aria-label="Create application" aria-busy={busy} className="space-y-3 border-t pt-4">
      <h4 className="font-medium">Add application</h4>
      <div className="space-y-1.5"><label htmlFor={sourceSelectID}>Application source</label><select id={sourceSelectID} className={selectClass} value={sourceKind} disabled={busy} onChange={(event) => setSourceKind(event.target.value as "image" | "git")}><option value="image">Container image</option><option value="git">Git repository</option></select></div>
      <ApplicationFields name={name} image={image} setName={setName} setImage={setImage} disabled={busy} showImage={sourceKind === "image"} />
      {sourceKind === "git" ? <GitApplicationFields value={git} onChange={setGit} disabled={busy} /> : null}
      <Button type="submit" disabled={!canWrite || busy || Boolean(editing)}>{busy ? "Saving application..." : "Save application"}</Button>
      {error ? <p role="alert" className="break-words [overflow-wrap:anywhere]">Unable to create application. {error}</p> : null}
    </form> : null}
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

export function ProjectHosting({ projects, canOperateProject, unavailable = false }: { projects: Project[]; canOperateProject: (projectID: string) => boolean; unavailable?: boolean }) {
  const id = useId();
  const [projectID, setProjectID] = useState("");
  const project = projects.find((item) => item.id === projectID);
  return <Card className="min-w-0" role="region" aria-label="Project hosting">
    <CardHeader><CardTitle>Project hosting</CardTitle><p className="text-sm text-muted-foreground">Save image or Git application configuration in shared project environments, choose placement, and request image deployment separately. Operation history records outcomes; it does not provide continuous runtime health.</p></CardHeader>
    <CardContent className="min-w-0 space-y-5">
      <div className="max-w-xl space-y-1.5"><label htmlFor={`${id}-project`}>Hosting project</label><select id={`${id}-project`} className={selectClass} value={project?.id ?? ""} onChange={(event) => setProjectID(event.target.value)}><option value="">Select a project</option>{projects.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></div>
      {project ? <><p className="break-all text-xs text-muted-foreground">Project ID for destination grants: {project.id}</p><ProjectEnvironments key={project.id} projectID={project.id} writable={canOperateProject(project.id)} blocked={unavailable} /></> : <p className="text-sm text-muted-foreground">Choose a project to manage or view saved configuration.</p>}
    </CardContent>
  </Card>;
}
