import { useEffect, useId, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { principalQuery } from "@/api";
import { ImageRegistryCredential, type ImageRegistrySelection } from "@/components/hosting/ImageRegistryCredential";
import type { components } from "@/api/generated";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Input } from "@/components/ui/input";

export type GitApplicationDraft = components["schemas"]["GitResourceInput"];
export const emptyGitDraft = (): GitApplicationDraft => ({ repository_url: "", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile" } });
export function gitBuildVariablesError(settings: GitApplicationDraft["settings"]): string {
 const rows = settings.build_variables ?? [];
 if (rows.length > 64) return "At most 64 build variables are allowed.";
 const names = new Set<string>();
 const reserved = new Set(["HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "ALL_PROXY", "NO_PROXY", "BUILDPLATFORM", "BUILDOS", "BUILDARCH", "BUILDVARIANT", "TARGETPLATFORM", "TARGETOS", "TARGETARCH", "TARGETVARIANT"]);
 for (const row of rows) {
  if (!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(row.name)) return "Build variable names must use ASCII letters, numbers or underscores and start with a letter or underscore, with at most 128 characters.";
  if (/^(NEROCD_|BUILDKIT_|BUILDX_)/i.test(row.name) || reserved.has(row.name.toUpperCase())) return "This build variable name is reserved for build execution.";
  if (names.has(row.name)) return "Build variable names must be unique.";
  names.add(row.name);
  if (new TextEncoder().encode(row.value).length > 4096 || /\0/.test(row.value)) return "Build variable values allow at most 4096 UTF-8 bytes and no NUL.";
 }
 return "";
}
export function GitApplicationFields({ value, onChange, disabled, projectID, onRegistryValidityChange }: { value: GitApplicationDraft; onChange: (value: GitApplicationDraft) => void; disabled: boolean; projectID?: string; onRegistryValidityChange?: (valid: boolean) => void }) {
 const id = useId();
 const settings = value.settings;
 const setBuildVariables = (rows: NonNullable<GitApplicationDraft["settings"]["build_variables"]>) => {
  const next = { ...settings }; if (rows.length) next.build_variables = rows; else delete next.build_variables; onChange({ ...value, settings: next });
 };
 const setSetting = (key: "context_dir" | "dockerfile_path" | "build_target" | "publish_dir" | "compose_file" | "public_component", text: string) => {
  const next = { ...settings }; if (text) next[key] = text; else delete next[key]; onChange({ ...value, settings: next });
 };
 return <div className="min-w-0 space-y-3">
  <div className="space-y-1.5"><label htmlFor={`${id}-url`}>Git repository URL</label><Input id={`${id}-url`} type="url" required value={value.repository_url} onChange={(event) => onChange({ ...value, repository_url: event.target.value })} disabled={disabled} placeholder="https://github.com/team/application.git" spellCheck={false} /></div>
  <div className="space-y-1.5"><label htmlFor={`${id}-ref`}>Git branch, tag, or commit</label><Input id={`${id}-ref`} required value={value.requested_ref} onChange={(event) => onChange({ ...value, requested_ref: event.target.value })} disabled={disabled} spellCheck={false} /></div>
  <div className="space-y-1.5"><label htmlFor={`${id}-credentials`}>Git source access</label><select id={`${id}-credentials`} className="h-10 w-full min-w-0 rounded-xl border border-border bg-card px-2 text-sm" value={value.credential_kind} disabled={disabled} onChange={(event) => onChange({ ...value, credential_kind: event.target.value as GitApplicationDraft["credential_kind"] })}>{!["public", "https_basic"].includes(value.credential_kind) ? <option value={value.credential_kind} disabled>Unsupported saved credential kind</option> : null}<option value="public">Public HTTPS</option><option value="https_basic">Private HTTPS with a source grant</option></select></div>
  <p className="text-xs text-muted-foreground">The server controls allowed HTTPS hosts. Private HTTPS builds require an owner-created source grant referencing an existing managed secret version. SSH is not supported in this editor.</p>
  <div className="space-y-1.5"><label htmlFor={`${id}-strategy`}>Build strategy</label><select id={`${id}-strategy`} className="h-10 w-full min-w-0 rounded-xl border border-border bg-card px-2 text-sm" value={settings.strategy} disabled={disabled} onChange={(event) => onChange({ ...value, settings: { strategy: event.target.value as GitApplicationDraft["settings"]["strategy"], ...(settings.context_dir ? { context_dir: settings.context_dir } : {}), ...(["compose", "dockerfile"].includes(event.target.value) && settings.build_registry_credential ? { build_registry_credential: { ...settings.build_registry_credential } } : {}), ...(["compose", "dockerfile"].includes(event.target.value) && settings.build_variables?.length ? { build_variables: settings.build_variables } : {}), ...(event.target.value === "compose" ? { compose_file: "docker-compose.yaml", public_component: "", container_port: 80 } : {}) } })}><option value="dockerfile">Dockerfile</option><option value="railpack">Railpack</option><option value="static">Static assets</option><option value="compose">Docker Compose</option></select></div>
  <div className="space-y-1.5"><label htmlFor={`${id}-context`}>Context directory</label><Input id={`${id}-context`} value={settings.context_dir ?? ""} onChange={(event) => setSetting("context_dir", event.target.value)} disabled={disabled} placeholder="Repository root when empty" spellCheck={false} /></div>
  {settings.strategy === "compose" ? <>
   <div className="space-y-1.5"><label htmlFor={`${id}-compose-file`}>Compose file</label><Input id={`${id}-compose-file`} value={settings.compose_file ?? ""} onChange={event => setSetting("compose_file", event.target.value)} disabled={disabled} required placeholder="docker-compose.yaml" spellCheck={false} aria-describedby={`${id}-compose-path-help`} /></div>
   <p id={`${id}-compose-path-help`} className="text-xs text-muted-foreground">Path relative to the context directory. Use a repository-relative path without a leading slash or parent-directory segments.</p>
   <p className="break-words text-xs [overflow-wrap:anywhere]">Compose path in repository: <code>{[settings.context_dir, settings.compose_file].filter(Boolean).join("/") || "No Compose file selected"}</code></p>
   <div className="space-y-1.5"><label htmlFor={`${id}-compose-service`}>Public Compose service</label><Input id={`${id}-compose-service`} value={settings.public_component ?? ""} onChange={event => setSetting("public_component", event.target.value)} disabled={disabled} required placeholder="web" spellCheck={false} /></div>
   <div className="space-y-1.5"><label htmlFor={`${id}-compose-port`}>Public Compose service port</label><Input id={`${id}-compose-port`} type="number" min={1} max={65535} value={settings.container_port ?? ""} onChange={event => onChange({ ...value, settings: { ...settings, container_port: Number(event.target.value) } })} disabled={disabled} required /></div>
   <p className="text-xs text-muted-foreground">Choose the service and container port that will receive application traffic. Domains and routes are configured separately.</p>
  </> : null}
  {settings.strategy === "dockerfile" ? <>
   <div className="space-y-1.5"><label htmlFor={`${id}-dockerfile`}>Dockerfile path</label><Input id={`${id}-dockerfile`} value={settings.dockerfile_path ?? ""} onChange={(event) => setSetting("dockerfile_path", event.target.value)} disabled={disabled} placeholder="Dockerfile" spellCheck={false} /></div>
   <div className="space-y-1.5"><label htmlFor={`${id}-target`}>Build target</label><Input id={`${id}-target`} value={settings.build_target ?? ""} onChange={(event) => setSetting("build_target", event.target.value)} disabled={disabled} spellCheck={false} /></div>
  </> : null}
  {settings.strategy === "compose" || settings.strategy === "dockerfile" ? <>
   <section className="min-w-0 space-y-3 rounded-lg border p-3" aria-label={settings.strategy === "compose" ? "Compose build variables" : "Dockerfile build variables"}>
    <h6 className="font-medium">Build variables</h6>
    <p className="text-xs text-muted-foreground">Use nonsecret values. Build arguments can appear in image metadata and build output. These variables are available only during builds; runtime variables are configured separately.</p>
    {settings.strategy === "dockerfile" ? <><label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={settings.inject_build_args_to_dockerfile !== false} disabled={disabled} onChange={event => onChange({ ...value, settings: { ...settings, inject_build_args_to_dockerfile: event.target.checked } })} /><span>Inject build arguments into Dockerfile</span></label>
     <p className="text-xs text-muted-foreground">When enabled, the build declares configured ARG names in each Dockerfile stage. When disabled, declare ARG in your source Dockerfile.</p></> : null}
    {(settings.build_variables ?? []).map((variable, index) => <fieldset key={index} disabled={disabled} className="min-w-0 space-y-2"><legend>Build variable {index + 1}</legend>
     <label htmlFor={`${id}-build-name-${index}`}>Build variable name {index + 1}</label><Input id={`${id}-build-name-${index}`} value={variable.name} required pattern="[A-Za-z_][A-Za-z0-9_]{0,127}" maxLength={128} spellCheck={false} onChange={event => setBuildVariables(settings.build_variables!.map((row, i) => i === index ? { ...row, name: event.target.value } : row))} />
     <label htmlFor={`${id}-build-value-${index}`}>Build variable value {index + 1}</label><Textarea id={`${id}-build-value-${index}`} className="min-h-20 w-full min-w-0" value={variable.value} spellCheck={false} autoComplete="off" onChange={event => setBuildVariables(settings.build_variables!.map((row, i) => i === index ? { ...row, value: event.target.value } : row))} />
     <Button type="button" variant="outline" className="h-auto max-w-full whitespace-normal" onClick={() => setBuildVariables(settings.build_variables!.filter((_, i) => i !== index))}>Remove build variable {index + 1}</Button>
    </fieldset>)}
    <Button type="button" variant="outline" className="h-auto max-w-full whitespace-normal" disabled={disabled || (settings.build_variables?.length ?? 0) >= 64} onClick={() => setBuildVariables([...(settings.build_variables ?? []), { name: "", value: "" }])}>Add build variable</Button>
   </section>
  </> : null}
  {projectID ? <BuildRegistryField value={value} projectID={projectID} disabled={disabled} onChange={onChange} onValidityChange={onRegistryValidityChange} /> : null}
  {settings.strategy === "static" ? <>
   <div className="space-y-1.5"><label htmlFor={`${id}-publish`}>Publish directory</label><Input id={`${id}-publish`} value={settings.publish_dir ?? ""} onChange={(event) => setSetting("publish_dir", event.target.value)} disabled={disabled} placeholder="Prepared assets in repository root when empty" spellCheck={false} /></div>
   <label className="flex items-center gap-2"><input type="checkbox" checked={settings.spa_fallback ?? false} disabled={disabled} onChange={(event) => onChange({ ...value, settings: { ...settings, spa_fallback: event.target.checked } })} />SPA fallback</label>
  </> : null}
  <p className="text-xs text-muted-foreground">Saving records configuration only. Request builds separately when enabled by your operator. Select a successful build to request deployment of its recorded artifact. Static assets must already be prepared.</p>
 </div>;
}

function BuildRegistryField({ value, projectID, disabled, onChange, onValidityChange }: { value: GitApplicationDraft; projectID: string; disabled: boolean; onChange: (value: GitApplicationDraft) => void; onValidityChange?: (valid: boolean) => void }) {
 // The hosting page owns identity refresh. Observe it without starting a fetch.
 const identity = useQuery({ ...principalQuery(), enabled: false });
 const [denied, setDenied] = useState(false);
 const supported = ["dockerfile", "compose"].includes(value.settings.strategy);
 const ready = Boolean(identity.data) && !identity.isPending && !identity.isFetching && !identity.isError && !denied;
 const actor = identity.data ? JSON.stringify([identity.data.provider, identity.data.id]) : "";
 useEffect(() => { if (!supported) onValidityChange?.(true); else if (!ready) onValidityChange?.(false); }, [supported, ready, onValidityChange]);
 if (!supported) return null;
 const selected = value.settings.build_registry_credential;
 return <>
  {!ready ? <p role="alert">Build registry credential access is unavailable. Refresh access before saving this Git source.</p> : null}
  {ready ? <ImageRegistryCredential key={JSON.stringify([actor, projectID])} actor={actor} project={projectID} image="" purpose="build" initialCredential={selected ?? undefined} disabled={disabled} onDenied={() => { setDenied(true); onValidityChange?.(false); }} onChange={(selection: ImageRegistrySelection) => {
   onValidityChange?.(selection.valid);
   if (!selection.valid) return;
   const next = { ...value.settings };
   if (selection.credential) next.build_registry_credential = { ...selection.credential }; else delete next.build_registry_credential;
   if (JSON.stringify(next) !== JSON.stringify(value.settings)) onChange({ ...value, settings: next });
  }} /> : null}
  <p className="break-all text-sm">Selected build registry credential: {selected ? `${selected.secret_id}, version ${selected.version}` : "None"}. This does not select deployment registry credentials or a Git source grant.</p>
 </>;
}
