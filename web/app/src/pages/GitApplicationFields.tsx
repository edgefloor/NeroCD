import { useId } from "react";
import type { components } from "@/api/generated";
import { Input } from "@/components/ui/input";

export type GitApplicationDraft = components["schemas"]["GitResourceInput"];
export const emptyGitDraft = (): GitApplicationDraft => ({ repository_url: "", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile" } });
export function GitApplicationFields({ value, onChange, disabled }: { value: GitApplicationDraft; onChange: (value: GitApplicationDraft) => void; disabled: boolean }) {
 const id = useId();
 const settings = value.settings;
 const setSetting = (key: "context_dir" | "dockerfile_path" | "build_target" | "publish_dir", text: string) => {
  const next = { ...settings }; if (text) next[key] = text; else delete next[key]; onChange({ ...value, settings: next });
 };
 return <div className="min-w-0 space-y-3">
  <div className="space-y-1.5"><label htmlFor={`${id}-url`}>Git repository URL</label><Input id={`${id}-url`} type="url" required value={value.repository_url} onChange={(event) => onChange({ ...value, repository_url: event.target.value })} disabled={disabled} placeholder="https://github.com/team/application.git" spellCheck={false} /></div>
  <div className="space-y-1.5"><label htmlFor={`${id}-ref`}>Git branch, tag, or commit</label><Input id={`${id}-ref`} required value={value.requested_ref} onChange={(event) => onChange({ ...value, requested_ref: event.target.value })} disabled={disabled} spellCheck={false} /></div>
  <p className="text-xs text-muted-foreground">Public HTTPS repositories only. The server controls allowed hosts. Private repositories and SSH are not supported yet.</p>
  <div className="space-y-1.5"><label htmlFor={`${id}-strategy`}>Build strategy</label><select id={`${id}-strategy`} className="h-10 w-full min-w-0 rounded-xl border border-border bg-card px-2 text-sm" value={settings.strategy} disabled={disabled} onChange={(event) => onChange({ ...value, settings: { strategy: event.target.value as GitApplicationDraft["settings"]["strategy"], ...(settings.context_dir ? { context_dir: settings.context_dir } : {}) } })}><option value="dockerfile">Dockerfile</option><option value="railpack">Railpack</option><option value="static">Static assets</option></select></div>
  <div className="space-y-1.5"><label htmlFor={`${id}-context`}>Context directory</label><Input id={`${id}-context`} value={settings.context_dir ?? ""} onChange={(event) => setSetting("context_dir", event.target.value)} disabled={disabled} placeholder="Repository root when empty" spellCheck={false} /></div>
  {settings.strategy === "dockerfile" ? <>
   <div className="space-y-1.5"><label htmlFor={`${id}-dockerfile`}>Dockerfile path</label><Input id={`${id}-dockerfile`} value={settings.dockerfile_path ?? ""} onChange={(event) => setSetting("dockerfile_path", event.target.value)} disabled={disabled} placeholder="Dockerfile" spellCheck={false} /></div>
   <div className="space-y-1.5"><label htmlFor={`${id}-target`}>Build target</label><Input id={`${id}-target`} value={settings.build_target ?? ""} onChange={(event) => setSetting("build_target", event.target.value)} disabled={disabled} spellCheck={false} /></div>
  </> : null}
  {settings.strategy === "static" ? <>
   <div className="space-y-1.5"><label htmlFor={`${id}-publish`}>Publish directory</label><Input id={`${id}-publish`} value={settings.publish_dir ?? ""} onChange={(event) => setSetting("publish_dir", event.target.value)} disabled={disabled} placeholder="Prepared assets in repository root when empty" spellCheck={false} /></div>
   <label className="flex items-center gap-2"><input type="checkbox" checked={settings.spa_fallback ?? false} disabled={disabled} onChange={(event) => onChange({ ...value, settings: { ...settings, spa_fallback: event.target.checked } })} />SPA fallback</label>
  </> : null}
  <p className="text-xs text-muted-foreground">Saving records configuration only. Request builds separately when enabled by your operator. Deploying a build artifact is not available yet. Static assets must already be prepared.</p>
 </div>;
}
