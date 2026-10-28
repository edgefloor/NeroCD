import { useId } from "react";
import type { components } from "@/api/generated";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { DockerfileBuildFields } from "./DockerfileBuildFields";

export type DockerfileApplicationDraft = components["schemas"]["DockerfileResourceInput"];
export function dockerfileContentError(content: string): string {
 if (!content.trim()) return "Dockerfile content is required.";
 if (content.includes("\0")) return "Dockerfile content cannot contain NUL.";
 if (new TextEncoder().encode(content).length > 1_048_576) return "Dockerfile content allows at most 1 MiB of UTF-8 text.";
 return "";
}
export const emptyDockerfileDraft = (): DockerfileApplicationDraft => ({ content: "", settings: { strategy: "dockerfile" } });

export function DockerfileApplicationFields({ value, onChange, disabled, projectID, onRegistryValidityChange, onSecretsValidityChange }: { value: DockerfileApplicationDraft; onChange: (value: DockerfileApplicationDraft) => void; disabled: boolean; projectID: string; onRegistryValidityChange: (valid: boolean) => void; onSecretsValidityChange: (valid: boolean) => void }) {
 const id = useId();
 return <div className="min-w-0 space-y-3">
  <div className="space-y-1.5"><label htmlFor={`${id}-content`}>Dockerfile content</label><Textarea id={`${id}-content`} rows={20} className="min-h-64 max-h-[32rem] w-full min-w-0 resize-y overflow-auto font-mono [field-sizing:fixed]" value={value.content} onChange={event => onChange({ ...value, content: event.target.value })} disabled={disabled} required spellCheck={false} aria-describedby={`${id}-help`} placeholder={'FROM nginx\nEXPOSE 80\nCMD ["nginx", "-g", "daemon off;"]\n'} /><p id={`${id}-help`} className="text-xs text-muted-foreground">Paste a Dockerfile to build without a Git repository. The build context contains this Dockerfile only. Supply dependencies in the Dockerfile and select managed build secrets below for credentials.</p></div>
  <div className="space-y-1.5"><label htmlFor={`${id}-target`}>Build target</label><Input id={`${id}-target`} value={value.settings.build_target ?? ""} disabled={disabled} spellCheck={false} onChange={event => { const settings = { ...value.settings }; if (event.target.value) settings.build_target = event.target.value; else delete settings.build_target; onChange({ ...value, settings }); }} /></div>
  <DockerfileBuildFields settings={value.settings} onChange={settings => onChange({ ...value, settings })} disabled={disabled} projectID={projectID} onRegistryValidityChange={onRegistryValidityChange} onSecretsValidityChange={onSecretsValidityChange} />
  <p className="text-xs text-muted-foreground">Saving records configuration only. Request a build separately, then select its successful artifact to deploy. Deployment uses the first numeric EXPOSE port, or port 80 when none is declared. You can override the port before deploying.</p>
 </div>;
}
