import { useEffect, useId, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { principalQuery } from "@/api";
import { ImageRegistryCredential, type ImageRegistrySelection } from "@/components/hosting/ImageRegistryCredential";
import type { components } from "@/api/generated";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Input } from "@/components/ui/input";
export type HostingBuildSettings = components["schemas"]["HostingBuildSettings"];
type BuildSettingsValue = { settings: HostingBuildSettings };
export function gitBuildVariablesError(settings: HostingBuildSettings): string {
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
 const secrets = settings.build_secrets ?? [];
 if (secrets.length > 64) return "At most 64 build secrets are allowed.";
 const secretNames = new Set<string>();
 for (const row of secrets) {
  if (!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(row.name)) return "Build secret names must use ASCII letters, numbers or underscores and start with a letter or underscore, with at most 128 characters.";
  if (/^(NEROCD_|BUILDKIT_|BUILDX_)/i.test(row.name) || reserved.has(row.name.toUpperCase())) return "This build secret name is reserved for build execution.";
  if (secretNames.has(row.name)) return "Build secret names must be unique.";
  if (names.has(row.name)) return "Build secret names must differ from public build variable names.";
  if (!row.secret.secret_id || !Number.isSafeInteger(row.secret.version) || row.secret.version < 1) return "Choose a managed secret and explicit version for every build secret.";
  secretNames.add(row.name);
 }
 return "";
}
export function DockerfileBuildFields({ settings, onChange: setSettings, disabled, projectID, onRegistryValidityChange, onSecretsValidityChange }: { settings: HostingBuildSettings; onChange: (settings: HostingBuildSettings) => void; disabled: boolean; projectID?: string; onRegistryValidityChange?: (valid: boolean) => void; onSecretsValidityChange?: (valid: boolean) => void }) {
 const id = useId();
 const value = { settings };
 const onChange = (next: BuildSettingsValue) => setSettings(next.settings);
 const setBuildVariables = (rows: NonNullable<HostingBuildSettings["build_variables"]>) => {
  const next = { ...settings }; if (rows.length) next.build_variables = rows; else delete next.build_variables; setSettings(next);
 };
 return <>
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
  {projectID ? <BuildSecretsField value={value} projectID={projectID} disabled={disabled} onChange={onChange} onValidityChange={onSecretsValidityChange} /> : null}
  {projectID ? <BuildRegistryField value={value} projectID={projectID} disabled={disabled} onChange={onChange} onValidityChange={onRegistryValidityChange} /> : null}
 </>;
}

function BuildRegistryField({ value, projectID, disabled, onChange, onValidityChange }: { value: BuildSettingsValue; projectID: string; disabled: boolean; onChange: (value: BuildSettingsValue) => void; onValidityChange?: (valid: boolean) => void }) {
 // The hosting page owns identity refresh. Observe it without starting a fetch.
 const identity = useQuery({ ...principalQuery(), enabled: false });
 const [denied, setDenied] = useState(false);
 const supported = ["dockerfile", "compose"].includes(value.settings.strategy);
 const ready = Boolean(identity.data) && !identity.isPending && !identity.isFetching && !identity.isError;
 const actor = identity.data ? JSON.stringify([identity.data.provider, identity.data.id]) : "";
 useEffect(() => { if (!supported) onValidityChange?.(true); else if (!ready) onValidityChange?.(false); }, [supported, ready, onValidityChange]);
 if (!supported) return null;
 const selected = value.settings.build_registry_credential;
 return <>
  {!ready || denied ? <p role="alert">Build registry credential access is unavailable. {ready ? "Use Refresh registry credential metadata to recheck access" : "Refresh access"} before saving this build source.</p> : null}
  {ready ? <ImageRegistryCredential key={JSON.stringify([actor, projectID])} actor={actor} project={projectID} image="" purpose="build" initialCredential={selected ?? undefined} disabled={disabled || denied} onAccessRestored={denied && !disabled ? () => setDenied(false) : undefined} onDenied={() => { setDenied(true); onValidityChange?.(false); }} onChange={(selection: ImageRegistrySelection) => {
   onValidityChange?.(selection.valid);
   if (!selection.valid) return;
   const next = { ...value.settings };
   if (selection.credential) next.build_registry_credential = { ...selection.credential }; else delete next.build_registry_credential;
   if (JSON.stringify(next) !== JSON.stringify(value.settings)) onChange({ ...value, settings: next });
  }} /> : null}
  <p className="break-all text-sm">Selected build registry credential: {selected ? `${selected.secret_id}, version ${selected.version}` : "None"}. This does not select deployment registry credentials or a Git source grant.</p>
 </>;
}

function BuildSecretsField({ value, projectID, disabled, onChange, onValidityChange }: { value: BuildSettingsValue; projectID: string; disabled: boolean; onChange: (value: BuildSettingsValue) => void; onValidityChange?: (valid: boolean) => void }) {
 const id = useId();
 const identity = useQuery({ ...principalQuery(), enabled: false });
 const [denied, setDenied] = useState(false);
 const [validated, setValidated] = useState<Record<string, boolean>>({});
 const supported = ["dockerfile", "compose"].includes(value.settings.strategy);
 const rows = value.settings.build_secrets ?? [];
 const ready = Boolean(identity.data) && !identity.isPending && !identity.isFetching && !identity.isError;
 const actor = identity.data ? JSON.stringify([identity.data.provider, identity.data.id]) : "";
 // Keep an edited row's picker mounted. New external draft objects get new identities.
 const rowIDs = useRef(new WeakMap<object, number>());
 const nextRowID = useRef(0);
 const rowID = (row: typeof rows[number]) => {
  let key = rowIDs.current.get(row);
  if (key === undefined) { key = nextRowID.current++; rowIDs.current.set(row, key); }
  return key;
 };
 const keyFor = (row: typeof rows[number]) => JSON.stringify([actor, projectID, rowID(row), row.secret]);
 const valid = !supported || !rows.length || ready && !denied && rows.every(row => validated[keyFor(row)]);
 useEffect(() => { onValidityChange?.(Boolean(valid)); }, [valid, onValidityChange]);
 const update = (nextRows: typeof rows) => {
  const settings = { ...value.settings };
  if (nextRows.length) settings.build_secrets = nextRows; else delete settings.build_secrets;
  onChange({ ...value, settings });
 };
 const updateRow = (index: number, next: typeof rows[number]) => {
  rowIDs.current.set(next, rowID(rows[index]!));
  update(rows.map((entry, i) => i === index ? next : entry));
 };
 if (!supported) return null;
 return <section aria-label="Build secrets" className="min-w-0 space-y-3 rounded-lg border p-3">
  <h6 className="font-medium">Build secrets</h6>
  <p className="text-xs text-muted-foreground">Choose existing managed secret versions for private build dependencies. Selected names are available as environment variables in build RUN commands. Commands with existing secret mounts keep their explicit configuration. Runtime secrets are configured separately.</p>
  {rows.length && (!ready || denied) ? <p role="alert">Build secret access is unavailable. Refresh build secret metadata to recheck access before saving this build source.</p> : null}
  {rows.map((row, index) => <div key={JSON.stringify([actor, projectID, rowID(row)])} className="min-w-0 space-y-2">
   <label htmlFor={`${id}-build-secret-name-${index}`}>Build secret name {index + 1}</label><Input id={`${id}-build-secret-name-${index}`} value={row.name} required pattern="[A-Za-z_][A-Za-z0-9_]{0,127}" maxLength={128} disabled={disabled} spellCheck={false} onChange={event => updateRow(index, { ...row, name: event.target.value })} />
   {ready ? <ImageRegistryCredential key={JSON.stringify([actor, projectID, rowID(row)])} actor={actor} project={projectID} image="" purpose="build-secret" secretLabel={`Build secret ${index + 1}`} initialCredential={row.secret.secret_id ? row.secret : undefined} disabled={disabled || denied} onAccessRestored={denied && !disabled ? () => setDenied(false) : undefined} onDenied={() => { setDenied(true); onValidityChange?.(false); }} onChange={selection => {
    const next = selection.valid && selection.credential ? { ...row, secret: { ...selection.credential } } : row;
    rowIDs.current.set(next, rowID(row));
    const key = keyFor(next);
    setValidated(previous => previous[key] === selection.valid ? previous : { ...previous, [key]: selection.valid });
    if (JSON.stringify(next.secret) !== JSON.stringify(row.secret)) updateRow(index, next);
   }} /> : null}
   <Button type="button" variant="outline" disabled={disabled} onClick={() => update(rows.filter((_, i) => i !== index))}>Remove build secret {index + 1}</Button>
  </div>)}
  <Button type="button" variant="outline" disabled={disabled || rows.length >= 64} onClick={() => { onValidityChange?.(false); update([...rows, { name: "", secret: { secret_id: "", version: 0 } }]); }}>Add build secret</Button>
 </section>;
}
