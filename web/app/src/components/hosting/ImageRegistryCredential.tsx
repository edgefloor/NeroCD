import { ApiError } from "@/api/errors";
import type { HostingDeploymentInput } from "@/api/hosting-deployments";
import { createManagedSecret } from "@/api/managed-secrets";
import { PageNavigation } from "@/components/common/PageNavigation";
import { ProtectedSecretValue } from "@/components/common/ProtectedSecretValue";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useEffect, useId, useRef, useState } from "react";
import { registrySelection, useRegistryMetadata } from "./useRegistryMetadata";

type Credential = NonNullable<HostingDeploymentInput["registry_credential"]>;
export type ImageRegistrySelection = {
  credential?: Credential;
  valid: boolean;
};

export function ImageRegistryCredential(input: {
  actor: string;
  project: string;
  image: string;
  compose?: boolean;
  purpose?: "deployment" | "build" | "build-secret";
  secretLabel?: string;
  initialCredential?: Credential;
  disabled: boolean;
  onChange: (selection: ImageRegistrySelection) => void;
  onDenied: () => void;
  onAccessRestored?: () => void;
}) {
  const viewModel = useImageRegistryCredential(input);
  const {
    project,
    purpose,
    disabled,
    onAccessRestored,
    onDenied,
    secretLabel,
    privateImage,
    metadata,
    secrets,
    versions,
    busy,
    refreshMetadata,
    build,
  } = viewModel;
  return (
    <section
      aria-label={
        purpose === "build-secret"
          ? secretLabel
          : purpose === "build"
            ? "Build registry credentials"
            : "Image registry credentials"
      }
      className="min-w-0 space-y-3 rounded-lg border p-3"
    >
      <RegistryAccessMode {...viewModel} />
      {privateImage ? (
        <>
          <RegistryCredentialHelp {...viewModel} />
          <RegistrySecretSelector {...viewModel} />{" "}
          <RegistryVersionSelector {...viewModel} />{" "}
          <Button
            type="button"
            variant="outline"
            disabled={busy || (disabled && !onAccessRestored)}
            onClick={() => void refreshMetadata()}
          >
            {purpose === "build-secret"
              ? `Refresh ${secretLabel.toLowerCase()} metadata`
              : "Refresh registry credential metadata"}
          </Button>
          {secrets.isError || versions.isError ? (
            <p role="alert">
              Credential metadata is unavailable. Refresh before selecting
              credentials.
            </p>
          ) : null}
          {!build ? (
            <RegistryCredentialCreate
              key={String(disabled)}
              project={project}
              disabled={disabled}
              onDenied={onDenied}
              onCreated={metadata.refresh}
            />
          ) : null}
        </>
      ) : null}
    </section>
  );
}

function RegistryCredentialCreate({
  project,
  disabled,
  onDenied,
  onCreated,
}: {
  project: string;
  disabled: boolean;
  onDenied: () => void;
  onCreated: () => void;
}) {
  const id = useId();
  const [name, setName] = useState("");
  const [registry, setRegistry] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [entry, setEntry] = useState(0);
  const [pendingRequest, setPendingRequest] = useState<AbortController | null>(
    null,
  );
  const busy = pendingRequest !== null;
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const active = useRef(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      request.current?.abort();
    };
  }, []);
  async function create() {
    if (
      disabled ||
      request.current ||
      !name.trim() ||
      !registry.trim() ||
      !username ||
      !password
    )
      return;
    const enteredRegistry = registry.trim().toLowerCase();
    const host = ["index.docker.io", "registry-1.docker.io"].includes(
      enteredRegistry,
    )
      ? "docker.io"
      : enteredRegistry;
    if (
      !/^[a-z0-9][a-z0-9.-]*(?::[0-9]+)?$/.test(host) ||
      /[\s:]/.test(username) ||
      /[\u0000-\u001f\u007f]/.test(password)
    ) {
      setError(
        "Enter a registry hostname with an optional port, a username without spaces or colons, and a password or token without line breaks.",
      );
      setPassword("");
      setEntry((current) => current + 1);
      return;
    }
    const controller = new AbortController();
    request.current = controller;
    setPendingRequest(controller);
    setError("");
    setNotice("");
    const value = JSON.stringify({ registry: host, username, password });
    setPassword("");
    setEntry((current) => current + 1);
    try {
      const created = await createManagedSecret(
        { project_id: project, name, value },
        { signal: controller.signal },
      );
      if (!active.current || controller.signal.aborted) return;
      if (created.project_id !== project || created.status !== "active")
        throw new Error("Secret metadata mismatch");
      setNotice(
        "Registry credential saved. Select its secret and explicit version below. The password is cleared.",
      );
      setName("");
      onCreated();
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status))
        onDenied();
      else
        setError(
          "Credential creation is unconfirmed. Refresh the secret list and check the name before creating another. Re-enter the password to retry.",
        );
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) {
        setPassword("");
        setEntry((current) => current + 1);
        setPendingRequest((current) =>
          current === controller ? null : current,
        );
      }
    }
  }
  const locked = disabled || busy;
  return (
    <details
      className="min-w-0 space-y-2"
      onKeyDown={(event) => {
        if (event.key === "Enter" && event.target instanceof HTMLInputElement) {
          event.preventDefault();
          void create();
        }
      }}
    >
      <summary>Create a registry credential</summary>
      <label htmlFor={`${id}-name`}>Registry secret name</label>
      <Input
        id={`${id}-name`}
        autoComplete="off"
        value={name}
        disabled={locked}
        onChange={(event) => setName(event.target.value)}
      />
      <label htmlFor={`${id}-registry`}>Registry host</label>
      <Input
        id={`${id}-registry`}
        placeholder="ghcr.io or registry.example.com:5000"
        autoComplete="off"
        value={registry}
        disabled={locked}
        onChange={(event) => setRegistry(event.target.value)}
      />
      <label htmlFor={`${id}-username`}>Registry username</label>
      <Input
        id={`${id}-username`}
        autoComplete="off"
        value={username}
        disabled={locked}
        onChange={(event) => setUsername(event.target.value)}
      />
      <ProtectedSecretValue
        key={entry}
        id={`${id}-password`}
        label="Registry password or token"
        value={password}
        onChange={setPassword}
        disabled={locked}
      />
      <Button
        type="button"
        disabled={
          locked || !name.trim() || !registry.trim() || !username || !password
        }
        onClick={() => void create()}
      >
        Save registry credential
      </Button>
      {error ? <p role="alert">{error}</p> : null}
      {notice ? <p role="status">{notice}</p> : null}
    </details>
  );
}

function useImageRegistryCredential({
  actor,
  project,
  image,
  compose = false,
  purpose = "deployment",
  initialCredential,
  disabled,
  onAccessRestored,
  onChange,
  onDenied,
  secretLabel = "Build secret",
}: {
  actor: string;
  project: string;
  image: string;
  compose?: boolean;
  purpose?: "deployment" | "build" | "build-secret";
  secretLabel?: string;
  initialCredential?: Credential;
  disabled: boolean;
  onChange: (selection: ImageRegistrySelection) => void;
  onDenied: () => void;
  onAccessRestored?: () => void;
}) {
  const id = useId();
  const [privateImage, setPrivateImage] = useState(
    purpose === "build-secret" || Boolean(initialCredential),
  );
  const [secret, setSecret] = useState(initialCredential?.secret_id ?? "");
  const [version, setVersion] = useState(
    initialCredential ? String(initialCredential.version) : "",
  );
  const [offset, setOffset] = useState(0);
  const [versionOffset, setVersionOffset] = useState(0);
  const metadata = useRegistryMetadata(
    {
      actor,
      project,
      secret,
      offset,
      versionOffset,
      enabled: privateImage,
      disabled,
      refreshable: Boolean(onAccessRestored),
    },
    (data) => onChange(registrySelection(data, project, secret, version)),
    onDenied,
    onAccessRestored,
  );
  const secrets = {
    data: metadata.data?.secrets,
    isFetching: metadata.isFetching,
    isError: metadata.isError,
  };
  const versions = {
    data: metadata.data?.versions,
    isFetching: metadata.isFetching,
    isError: metadata.isError,
  };
  const selected = secrets.data?.items.find(
    (item) =>
      item.id === secret &&
      item.project_id === project &&
      item.status === "active",
  );
  const busy = metadata.isFetching;
  function clearSelection() {
    setSecret("");
    setVersion("");
    setVersionOffset(0);
    onChange({ valid: !privateImage });
  }
  function refreshMetadata() {
    if (!disabled || onAccessRestored) {
      onChange({ valid: false });
      metadata.refresh();
    }
  }
  const locked = disabled || busy;
  const build = purpose !== "deployment";
  const label =
    purpose === "build-secret" ? secretLabel : "Build registry credential";
  return {
    project,
    image,
    compose,
    purpose,
    disabled,
    onAccessRestored,
    onChange,
    onDenied,
    secretLabel,
    id,
    privateImage,
    setPrivateImage,
    secret,
    setSecret,
    version,
    setVersion,
    offset,
    setOffset,
    versionOffset,
    setVersionOffset,
    metadata,
    secrets,
    versions,
    selected,
    busy,
    clearSelection,
    refreshMetadata,
    locked,
    build,
    label,
  };
}

type RegistryAccessModeModel = Pick<
  ReturnType<typeof useImageRegistryCredential>,
  | "purpose"
  | "id"
  | "locked"
  | "privateImage"
  | "onChange"
  | "setPrivateImage"
  | "setSecret"
  | "setVersion"
  | "setVersionOffset"
>;
function RegistryAccessMode(viewModel: RegistryAccessModeModel) {
  const {
    purpose,
    id,
    locked,
    privateImage,
    onChange,
    setPrivateImage,
    setSecret,
    setVersion,
    setVersionOffset,
  } = viewModel;

  if (!(purpose !== "build-secret")) return null;
  return (
    <>
      <label htmlFor={`${id}-mode`}>
        {purpose === "build" ? "Build registry credential" : "Image access"}
      </label>
      <select
        id={`${id}-mode`}
        className="w-full min-w-0 rounded border bg-card p-2"
        disabled={locked}
        value={privateImage ? "private" : "public"}
        onChange={(event) => {
          onChange({ valid: event.target.value !== "private" });
          setPrivateImage(event.target.value === "private");
          setSecret("");
          setVersion("");
          setVersionOffset(0);
        }}
      >
        <option value="public">
          {purpose === "build"
            ? "No build registry credential"
            : "Public image"}
        </option>
        <option value="private">
          {purpose === "build"
            ? "Select build registry credential"
            : "Private registry"}
        </option>
      </select>
    </>
  );
}

type RegistrySecretSelectorModel = Pick<
  ReturnType<typeof useImageRegistryCredential>,
  | "id"
  | "build"
  | "label"
  | "locked"
  | "secret"
  | "onChange"
  | "setSecret"
  | "setVersion"
  | "setVersionOffset"
  | "secrets"
  | "project"
  | "purpose"
  | "secretLabel"
  | "setOffset"
  | "clearSelection"
  | "offset"
>;
function RegistrySecretSelector(viewModel: RegistrySecretSelectorModel) {
  const {
    id,
    build,
    label,
    locked,
    secret,
    onChange,
    setSecret,
    setVersion,
    setVersionOffset,
    secrets,
    project,
    purpose,
    secretLabel,
    setOffset,
    clearSelection,
    offset,
  } = viewModel;

  return (
    <>
      <label htmlFor={`${id}-secret`}>
        {build ? `${label} secret` : "Registry credential secret"}
      </label>
      <select
        id={`${id}-secret`}
        className="w-full min-w-0 rounded border bg-card p-2"
        disabled={locked}
        value={secret}
        onChange={(event) => {
          onChange({ valid: false });
          setSecret(event.target.value);
          setVersion("");
          setVersionOffset(0);
        }}
      >
        <option value="">Choose an active project secret</option>
        {secrets.data?.items
          .filter(
            (item) => item.project_id === project && item.status === "active",
          )
          .map((item) => (
            <option key={item.id} value={item.id}>
              {item.name} · {item.id}
            </option>
          ))}
      </select>
      <PageNavigation
        label={
          purpose === "build-secret"
            ? `${secretLabel} secrets`
            : "registry secrets"
        }
        page={secrets.data}
        loading={locked || secrets.isFetching}
        onFirst={() => {
          setOffset(0);
          clearSelection();
        }}
        onPrevious={() => {
          setOffset(Math.max(0, offset - 20));
          clearSelection();
        }}
        onNext={() => {
          setOffset(offset + 20);
          clearSelection();
        }}
      />
    </>
  );
}

type RegistryVersionSelectorModel = Pick<
  ReturnType<typeof useImageRegistryCredential>,
  | "id"
  | "build"
  | "label"
  | "locked"
  | "selected"
  | "version"
  | "onChange"
  | "metadata"
  | "project"
  | "secret"
  | "setVersion"
  | "versions"
  | "purpose"
  | "secretLabel"
  | "setVersionOffset"
  | "versionOffset"
>;
function RegistryVersionSelector(viewModel: RegistryVersionSelectorModel) {
  const {
    id,
    build,
    label,
    locked,
    selected,
    version,
    onChange,
    metadata,
    project,
    secret,
    setVersion,
    versions,
    purpose,
    secretLabel,
    setVersionOffset,
    versionOffset,
  } = viewModel;

  return (
    <>
      <label htmlFor={`${id}-version`}>
        {build ? `${label} version` : "Registry credential version"}
      </label>
      <select
        id={`${id}-version`}
        className="w-full min-w-0 rounded border bg-card p-2"
        disabled={locked || !selected}
        value={version}
        onChange={(event) => {
          onChange(
            registrySelection(
              metadata.data,
              project,
              secret,
              event.target.value,
            ),
          );
          setVersion(event.target.value);
        }}
      >
        <option value="">Choose an explicit version</option>
        {versions.data?.items
          .filter(
            (item) =>
              item.secret_id === secret &&
              Number.isSafeInteger(item.version) &&
              item.version > 0,
          )
          .map((item) => (
            <option key={item.version} value={item.version}>
              Version {item.version}
            </option>
          ))}
      </select>
      {secret ? (
        <PageNavigation
          label={
            purpose === "build-secret"
              ? `${secretLabel} versions`
              : "registry credential versions"
          }
          page={versions.data}
          loading={locked || versions.isFetching}
          onFirst={() => {
            setVersionOffset(0);
            setVersion("");
            onChange({ valid: false });
          }}
          onPrevious={() => {
            setVersionOffset(Math.max(0, versionOffset - 20));
            setVersion("");
            onChange({ valid: false });
          }}
          onNext={() => {
            setVersionOffset(versionOffset + 20);
            setVersion("");
            onChange({ valid: false });
          }}
        />
      ) : null}
    </>
  );
}

type RegistryCredentialHelpModel = Pick<
  ReturnType<typeof useImageRegistryCredential>,
  "purpose" | "compose" | "image"
>;
function RegistryCredentialHelp(viewModel: RegistryCredentialHelpModel) {
  const { purpose, compose, image } = viewModel;

  if (!(purpose !== "build-secret")) return null;
  return (
    <p className="break-all text-sm">
      {purpose === "build" ? (
        "Choose one credential for matching registries used by Dockerfile FROM stages. Deployment registry access and Git source grants are selected separately."
      ) : compose ? (
        "Choose one registry credential for matching images pulled from registries. Images from other registries are pulled anonymously."
      ) : (
        <>
          Choose credentials for the registry hosting {image}. The saved
          registry host must match the image registry, including its port.
        </>
      )}{" "}
      Use docker.io for Docker Hub.
    </p>
  );
}
