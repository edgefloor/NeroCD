import { useEffect, useId, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { createManagedSecret, listManagedSecrets, listManagedSecretVersions } from "@/api/managed-secrets";
import type { HostingDeploymentInput } from "@/api/hosting-deployments";
import { ApiError } from "@/api/errors";
import { ProtectedSecretValue } from "@/components/common/ProtectedSecretValue";
import { PageNavigation } from "@/components/common/PageNavigation";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

type Credential = NonNullable<HostingDeploymentInput["registry_credential"]>;
export type ImageRegistrySelection = { credential?: Credential; valid: boolean };

export function ImageRegistryCredential({ actor, project, image, compose = false, purpose = "deployment", initialCredential, disabled, onChange, onDenied }: {
  actor: string; project: string; image: string; compose?: boolean; purpose?: "deployment" | "build"; initialCredential?: Credential; disabled: boolean;
  onChange: (selection: ImageRegistrySelection) => void; onDenied: () => void;
}) {
  const id = useId();
  const [privateImage, setPrivateImage] = useState(Boolean(initialCredential));
  const [secret, setSecret] = useState(initialCredential?.secret_id ?? "");
  const [version, setVersion] = useState(initialCredential ? String(initialCredential.version) : "");
  const [offset, setOffset] = useState(0);
  const [versionOffset, setVersionOffset] = useState(0);
  const [name, setName] = useState("");
  const [registry, setRegistry] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [entry, setEntry] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const active = useRef(true);
  const request = useRef<AbortController | null>(null);
  const secrets = useQuery({ queryKey: ["imageRegistrySecrets", actor, project, offset], queryFn: ({ signal }) => listManagedSecrets({ project_id: project, offset, limit: 20 }, { signal }), enabled: privateImage, retry: false, gcTime: 0 });
  const versions = useQuery({ queryKey: ["imageRegistryVersions", actor, project, secret, versionOffset], queryFn: ({ signal }) => listManagedSecretVersions(secret, { offset: versionOffset, limit: 20 }, { signal }), enabled: privateImage && !!secret, retry: false, gcTime: 0 });
  const selected = secrets.data?.items.find((item) => item.id === secret && item.project_id === project && item.status === "active");
  const valid = !!selected && !secrets.isError && !versions.isError && Number.isSafeInteger(Number(version)) && Number(version) > 0 && versions.data?.items.some((item) => item.secret_id === secret && item.version === Number(version));
  const notify = useRef(onChange);
  notify.current = onChange;
  useEffect(() => {
    if (disabled) return;
    notify.current(!privateImage ? { valid: true } : { valid: !!valid, ...(valid ? { credential: { secret_id: secret, version: Number(version) } } : {}) });
  }, [privateImage, valid, secret, version, disabled]);
  useEffect(() => {
    if ([secrets.error, versions.error].some((cause) => cause instanceof ApiError && [401, 403, 404].includes(cause.status))) onDenied();
  }, [secrets.error, versions.error, onDenied]);
  useEffect(() => {
    active.current = true;
    return () => { active.current = false; request.current?.abort(); };
  }, []);
  useEffect(() => { if (disabled) { request.current?.abort(); setPassword(""); setEntry((value) => value + 1); } }, [disabled]);
  function clearSelection() { setSecret(""); setVersion(""); setVersionOffset(0); }
  async function create() {
    if (disabled || request.current || !name.trim() || !registry.trim() || !username || !password) return;
    const enteredRegistry = registry.trim().toLowerCase();
    const host = ["index.docker.io", "registry-1.docker.io"].includes(enteredRegistry) ? "docker.io" : enteredRegistry;
    if (!/^[a-z0-9][a-z0-9.-]*(?::[0-9]+)?$/.test(host) || /[\s:]/.test(username) || /[\u0000-\u001f\u007f]/.test(password)) {
      setError("Enter a registry hostname with an optional port, a username without spaces or colons, and a password or token without line breaks.");
      setPassword(""); setEntry((current) => current + 1);
      return;
    }
    const controller = new AbortController(); request.current = controller;
    setBusy(true); setError(""); setNotice("");
    const value = JSON.stringify({ registry: host, username, password });
    setPassword(""); setEntry((current) => current + 1);
    try {
      const created = await createManagedSecret({ project_id: project, name, value }, { signal: controller.signal });
      if (!active.current || controller.signal.aborted) return;
      if (created.project_id !== project || created.status !== "active") throw new Error("Secret metadata mismatch");
      setNotice("Registry credential saved. Select its secret and explicit version below. The password is cleared.");
      setName("");
      await secrets.refetch();
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) onDenied();
      else setError("Credential creation is unconfirmed. Refresh the secret list and check the name before creating another. Re-enter the password to retry.");
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) { setPassword(""); setEntry((current) => current + 1); setBusy(false); }
    }
  }
  const locked = disabled || busy;
  return <section aria-label={purpose === "build" ? "Build registry credentials" : "Image registry credentials"} className="min-w-0 space-y-3 rounded-lg border p-3">
    <label htmlFor={`${id}-mode`}>{purpose === "build" ? "Build registry credential" : "Image access"}</label>
    <select id={`${id}-mode`} className="w-full min-w-0 rounded border bg-card p-2" disabled={locked} value={privateImage ? "private" : "public"} onChange={(event) => { if (purpose === "build") notify.current({ valid: event.target.value !== "private" }); setPrivateImage(event.target.value === "private"); clearSelection(); setPassword(""); setEntry((value) => value + 1); }}><option value="public">{purpose === "build" ? "No build registry credential" : "Public image"}</option><option value="private">{purpose === "build" ? "Select build registry credential" : "Private registry"}</option></select>
    {privateImage ? <>
      <p className="break-all text-sm">{purpose === "build" ? "Choose one credential for matching registries used by Dockerfile FROM stages. Deployment registry access and Git source grants are selected separately." : compose ? "Choose one registry credential for matching images pulled from registries. Images from other registries are pulled anonymously." : <>Choose credentials for the registry hosting {image}. The saved registry host must match the image registry, including its port.</>} Use docker.io for Docker Hub.</p>
      <label htmlFor={`${id}-secret`}>{purpose === "build" ? "Build registry credential secret" : "Registry credential secret"}</label>
      <select id={`${id}-secret`} className="w-full min-w-0 rounded border bg-card p-2" disabled={locked} value={secret} onChange={(event) => { if (purpose === "build") notify.current({ valid: false }); setSecret(event.target.value); setVersion(""); setVersionOffset(0); }}><option value="">Choose an active project secret</option>{secrets.data?.items.filter((item) => item.project_id === project && item.status === "active").map((item) => <option key={item.id} value={item.id}>{item.name} · {item.id}</option>)}</select>
      <PageNavigation label="registry secrets" page={secrets.data} loading={locked || secrets.isFetching} onFirst={() => { setOffset(0); clearSelection(); }} onPrevious={() => { setOffset(Math.max(0, offset - 20)); clearSelection(); }} onNext={() => { setOffset(offset + 20); clearSelection(); }} />
      <label htmlFor={`${id}-version`}>{purpose === "build" ? "Build registry credential version" : "Registry credential version"}</label>
      <select id={`${id}-version`} className="w-full min-w-0 rounded border bg-card p-2" disabled={locked || !selected} value={version} onChange={(event) => { if (purpose === "build") notify.current({ valid: false }); setVersion(event.target.value); }}><option value="">Choose an explicit version</option>{versions.data?.items.filter((item) => item.secret_id === secret && Number.isSafeInteger(item.version) && item.version > 0).map((item) => <option key={item.version} value={item.version}>Version {item.version}</option>)}</select>
      {secret ? <PageNavigation label="registry credential versions" page={versions.data} loading={locked || versions.isFetching} onFirst={() => { setVersionOffset(0); setVersion(""); }} onPrevious={() => { setVersionOffset(Math.max(0, versionOffset - 20)); setVersion(""); }} onNext={() => { setVersionOffset(versionOffset + 20); setVersion(""); }} /> : null}
      <Button type="button" variant="outline" disabled={locked} onClick={() => { void secrets.refetch(); if (secret) void versions.refetch(); }}>Refresh registry credential metadata</Button>
      {secrets.isError || versions.isError ? <p role="alert">Credential metadata is unavailable. Refresh before selecting credentials.</p> : null}
      {purpose !== "build" ? <details className="min-w-0 space-y-2" onKeyDown={(event) => { if (event.key === "Enter" && event.target instanceof HTMLInputElement) { event.preventDefault(); void create(); } }}><summary>Create a registry credential</summary>
        <label htmlFor={`${id}-name`}>Registry secret name</label><Input id={`${id}-name`} autoComplete="off" value={name} disabled={locked} onChange={(event) => setName(event.target.value)} />
        <label htmlFor={`${id}-registry`}>Registry host</label><Input id={`${id}-registry`} placeholder="ghcr.io or registry.example.com:5000" autoComplete="off" value={registry} disabled={locked} onChange={(event) => setRegistry(event.target.value)} />
        <label htmlFor={`${id}-username`}>Registry username</label><Input id={`${id}-username`} autoComplete="off" value={username} disabled={locked} onChange={(event) => setUsername(event.target.value)} />
        <ProtectedSecretValue key={entry} id={`${id}-password`} label="Registry password or token" value={password} onChange={setPassword} disabled={locked} />
        <Button type="button" disabled={locked || !name.trim() || !registry.trim() || !username || !password} onClick={() => void create()}>Save registry credential</Button>
        {error ? <p role="alert">{error}</p> : null}{notice ? <p role="status">{notice}</p> : null}
      </details> : null}
    </> : null}
  </section>;
}
