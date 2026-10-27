import { useId } from "react";
import type { components } from "@/api/generated";
import { Input } from "@/components/ui/input";

export type GitApplicationDraft = components["schemas"]["GitResourceInput"];
export const emptyGitDraft = (): GitApplicationDraft => ({ repository_url: "", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile" } });
export function GitApplicationFields({ value, onChange, disabled }: { value: GitApplicationDraft; onChange: (value: GitApplicationDraft) => void; disabled: boolean }) {
 const id = useId();
 const settings = value.settings;
 const setSetting = (key: "context_dir" | "dockerfile_path" | "build_target" | "publish_dir" | "compose_file" | "public_component", text: string) => {
  const next = { ...settings }; if (text) next[key] = text; else delete next[key]; onChange({ ...value, settings: next });
 };
 return <div className="min-w-0 space-y-3">
  <div className="space-y-1.5"><label htmlFor={`${id}-url`}>Git repository URL</label><Input id={`${id}-url`} type="url" required value={value.repository_url} onChange={(event) => onChange({ ...value, repository_url: event.target.value })} disabled={disabled} placeholder="https://github.com/team/application.git" spellCheck={false} /></div>
  <div className="space-y-1.5"><label htmlFor={`${id}-ref`}>Git branch, tag, or commit</label><Input id={`${id}-ref`} required value={value.requested_ref} onChange={(event) => onChange({ ...value, requested_ref: event.target.value })} disabled={disabled} spellCheck={false} /></div>
  <div className="space-y-1.5"><label htmlFor={`${id}-credentials`}>Git source access</label><select id={`${id}-credentials`} className="h-10 w-full min-w-0 rounded-xl border border-border bg-card px-2 text-sm" value={value.credential_kind} disabled={disabled} onChange={(event) => onChange({ ...value, credential_kind: event.target.value as GitApplicationDraft["credential_kind"] })}>{!["public", "https_basic"].includes(value.credential_kind) ? <option value={value.credential_kind} disabled>Unsupported saved credential kind</option> : null}<option value="public">Public HTTPS</option><option value="https_basic">Private HTTPS with a source grant</option></select></div>
  <p className="text-xs text-muted-foreground">The server controls allowed HTTPS hosts. Private HTTPS builds require an owner-created source grant referencing an existing managed secret version. SSH is not supported in this editor.</p>
  <div className="space-y-1.5"><label htmlFor={`${id}-strategy`}>Build strategy</label><select id={`${id}-strategy`} className="h-10 w-full min-w-0 rounded-xl border border-border bg-card px-2 text-sm" value={settings.strategy} disabled={disabled} onChange={(event) => onChange({ ...value, settings: { strategy: event.target.value as GitApplicationDraft["settings"]["strategy"], ...(settings.context_dir ? { context_dir: settings.context_dir } : {}), ...(event.target.value === "compose" ? { compose_file: "docker-compose.yaml", public_component: "", container_port: 80 } : {}) } })}><option value="dockerfile">Dockerfile</option><option value="railpack">Railpack</option><option value="static">Static assets</option><option value="compose">Docker Compose</option></select></div>
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
  {settings.strategy === "static" ? <>
   <div className="space-y-1.5"><label htmlFor={`${id}-publish`}>Publish directory</label><Input id={`${id}-publish`} value={settings.publish_dir ?? ""} onChange={(event) => setSetting("publish_dir", event.target.value)} disabled={disabled} placeholder="Prepared assets in repository root when empty" spellCheck={false} /></div>
   <label className="flex items-center gap-2"><input type="checkbox" checked={settings.spa_fallback ?? false} disabled={disabled} onChange={(event) => onChange({ ...value, settings: { ...settings, spa_fallback: event.target.checked } })} />SPA fallback</label>
  </> : null}
  <p className="text-xs text-muted-foreground">Saving records configuration only. Request builds separately when enabled by your operator. Select a successful build to request deployment of its recorded artifact. Static assets must already be prepared.</p>
 </div>;
}
