import { useId } from "react";
import type { components } from "@/api/generated";
import { Input } from "@/components/ui/input";

export type ComposeApplicationDraft = components["schemas"]["ComposeResourceSource"]["compose"];
export const emptyComposeDraft = (): ComposeApplicationDraft => ({ original: "", public_component: "", container_port: 80 });

export function ComposeApplicationFields({ value, onChange, disabled }: { value: ComposeApplicationDraft; onChange: (value: ComposeApplicationDraft) => void; disabled: boolean }) {
  const id = useId();
  return <div className="min-w-0 space-y-3">
    <div className="space-y-1.5"><label htmlFor={`${id}-source`}>Compose stack</label><textarea id={`${id}-source`} className="min-h-64 w-full min-w-0 rounded-xl border bg-card p-3 font-mono text-sm" value={value.original} onChange={event => onChange({ ...value, original: event.target.value })} disabled={disabled} required spellCheck={false} aria-describedby={`${id}-help`} /><p id={`${id}-help`} className="text-xs text-muted-foreground">Paste the original Compose source. Use variable references for credentials and supply managed-secret references in runtime configuration. The managed profile supports images, environment, commands, dependencies, healthchecks, and owned named volumes. Unsupported fields fail preflight.</p></div>
    <div className="space-y-1.5"><label htmlFor={`${id}-component`}>Public component</label><Input id={`${id}-component`} value={value.public_component} onChange={event => onChange({ ...value, public_component: event.target.value })} disabled={disabled} required placeholder="vikunja" spellCheck={false} /></div>
    <div className="space-y-1.5"><label htmlFor={`${id}-port`}>Public component container port</label><Input id={`${id}-port`} type="number" min={1} max={65535} value={value.container_port} onChange={event => onChange({ ...value, container_port: Number(event.target.value) })} disabled={disabled} required /></div>
  </div>;
}
