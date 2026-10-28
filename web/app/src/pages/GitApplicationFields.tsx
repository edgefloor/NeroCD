import type { components } from "@/api/generated";
import { Input } from "@/components/ui/input";
import { useId } from "react";
import { DockerfileBuildFields } from "./DockerfileBuildFields";

export type GitApplicationDraft = components["schemas"]["GitResourceInput"];
export function GitApplicationFields(props: {
  value: GitApplicationDraft;
  onChange: (value: GitApplicationDraft) => void;
  disabled: boolean;
  projectID?: string;
  onRegistryValidityChange?: (valid: boolean) => void;
  onSecretsValidityChange?: (valid: boolean) => void;
}) {
  const viewModel = useGitApplicationFields(props);
  const {
    value,
    onChange,
    disabled,
    projectID,
    onRegistryValidityChange,
    onSecretsValidityChange,
    id,
    settings,
    setSetting,
  } = viewModel;
  return (
    <div className="min-w-0 space-y-3">
      <div className="space-y-1.5">
        <label htmlFor={`${id}-url`}>Git repository URL</label>
        <Input
          id={`${id}-url`}
          type={value.credential_kind === "ssh_private_key" ? "text" : "url"}
          required
          value={value.repository_url}
          onChange={(event) =>
            onChange({ ...value, repository_url: event.target.value })
          }
          disabled={disabled}
          placeholder={
            value.credential_kind === "ssh_private_key"
              ? "git@github.com:team/application.git"
              : "https://github.com/team/application.git"
          }
          spellCheck={false}
        />
      </div>
      <div className="space-y-1.5">
        <label htmlFor={`${id}-ref`}>Git branch, tag, or commit</label>
        <Input
          id={`${id}-ref`}
          required
          value={value.requested_ref}
          onChange={(event) =>
            onChange({ ...value, requested_ref: event.target.value })
          }
          disabled={disabled}
          spellCheck={false}
        />
      </div>
      <GitSourceAccess {...viewModel} />
      {value.credential_kind === "ssh_private_key" ? (
        <div className="space-y-1.5">
          <label htmlFor={`${id}-fingerprints`}>SSH host fingerprints</label>
          <textarea
            id={`${id}-fingerprints`}
            className="min-h-24 w-full min-w-0 rounded-xl border border-border bg-card p-2 text-sm"
            required
            value={(value.ssh_host_fingerprints ?? []).join("\n")}
            onChange={(event) =>
              onChange({
                ...value,
                ssh_host_fingerprints: event.target.value.split("\n"),
              })
            }
            disabled={disabled}
            spellCheck={false}
            aria-describedby={`${id}-ssh-help`}
          />
          <p id={`${id}-ssh-help`} className="text-xs text-muted-foreground">
            Enter one verified SHA256 host fingerprint per line, up to 32.
            Obtain these from your Git provider or administrator before saving.
            Private SSH builds require hosting_git_ssh_private_key_v1 and an
            owner-created source grant selecting an existing raw, unencrypted
            PEM or OpenSSH private key in Project secrets.
          </p>
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">
          The server controls allowed HTTPS hosts. Private HTTPS builds require
          an owner-created source grant referencing an existing managed secret
          version.
        </p>
      )}
      <div className="space-y-1.5">
        <label htmlFor={`${id}-strategy`}>Build strategy</label>
        <select
          id={`${id}-strategy`}
          className="h-10 w-full min-w-0 rounded-xl border border-border bg-card px-2 text-sm"
          value={settings.strategy}
          disabled={disabled}
          onChange={(event) =>
            onChange({
              ...value,
              settings: {
                strategy: event.target
                  .value as GitApplicationDraft["settings"]["strategy"],
                ...(settings.context_dir
                  ? { context_dir: settings.context_dir }
                  : {}),
                ...(["compose", "dockerfile"].includes(event.target.value) &&
                settings.build_registry_credential
                  ? {
                      build_registry_credential: {
                        ...settings.build_registry_credential,
                      },
                    }
                  : {}),
                ...(["compose", "dockerfile"].includes(event.target.value) &&
                settings.build_secrets?.length
                  ? { build_secrets: settings.build_secrets }
                  : {}),
                ...(["compose", "dockerfile"].includes(event.target.value) &&
                settings.build_variables?.length
                  ? { build_variables: settings.build_variables }
                  : {}),
                ...(event.target.value === "compose"
                  ? {
                      compose_file: "docker-compose.yaml",
                      public_component: "",
                      container_port: 80,
                    }
                  : {}),
              },
            })
          }
        >
          <option value="dockerfile">Dockerfile</option>
          <option value="railpack">Railpack</option>
          <option value="static">Static assets</option>
          <option value="compose">Docker Compose</option>
        </select>
      </div>
      <div className="space-y-1.5">
        <label htmlFor={`${id}-context`}>Context directory</label>
        <Input
          id={`${id}-context`}
          value={settings.context_dir ?? ""}
          onChange={(event) => setSetting("context_dir", event.target.value)}
          disabled={disabled}
          placeholder="Repository root when empty"
          spellCheck={false}
        />
      </div>
      <GitComposeFields {...viewModel} />
      {settings.strategy === "dockerfile" ? (
        <>
          <div className="space-y-1.5">
            <label htmlFor={`${id}-dockerfile`}>Dockerfile path</label>
            <Input
              id={`${id}-dockerfile`}
              value={settings.dockerfile_path ?? ""}
              onChange={(event) =>
                setSetting("dockerfile_path", event.target.value)
              }
              disabled={disabled}
              placeholder="Dockerfile"
              spellCheck={false}
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor={`${id}-target`}>Build target</label>
            <Input
              id={`${id}-target`}
              value={settings.build_target ?? ""}
              onChange={(event) =>
                setSetting("build_target", event.target.value)
              }
              disabled={disabled}
              spellCheck={false}
            />
          </div>
        </>
      ) : null}
      <DockerfileBuildFields
        settings={settings}
        onChange={(next) => onChange({ ...value, settings: next })}
        disabled={disabled}
        projectID={projectID}
        onRegistryValidityChange={onRegistryValidityChange}
        onSecretsValidityChange={onSecretsValidityChange}
      />
      {settings.strategy === "static" ? (
        <>
          <div className="space-y-1.5">
            <label htmlFor={`${id}-publish`}>Publish directory</label>
            <Input
              id={`${id}-publish`}
              value={settings.publish_dir ?? ""}
              onChange={(event) =>
                setSetting("publish_dir", event.target.value)
              }
              disabled={disabled}
              placeholder="Prepared assets in repository root when empty"
              spellCheck={false}
            />
          </div>
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={settings.spa_fallback ?? false}
              disabled={disabled}
              onChange={(event) =>
                onChange({
                  ...value,
                  settings: { ...settings, spa_fallback: event.target.checked },
                })
              }
            />
            SPA fallback
          </label>
        </>
      ) : null}
      <p className="text-xs text-muted-foreground">
        Saving records configuration only. Request builds separately when
        enabled by your operator. Select a successful build to request
        deployment of its recorded artifact. Static assets must already be
        prepared.
      </p>
    </div>
  );
}

function useGitApplicationFields({
  value,
  onChange,
  disabled,
  projectID,
  onRegistryValidityChange,
  onSecretsValidityChange,
}: {
  value: GitApplicationDraft;
  onChange: (value: GitApplicationDraft) => void;
  disabled: boolean;
  projectID?: string;
  onRegistryValidityChange?: (valid: boolean) => void;
  onSecretsValidityChange?: (valid: boolean) => void;
}) {
  const id = useId();
  const settings = value.settings;
  const setSetting = (
    key:
      | "context_dir"
      | "dockerfile_path"
      | "build_target"
      | "publish_dir"
      | "compose_file"
      | "public_component",
    text: string,
  ) => {
    const next = { ...settings };
    if (text) next[key] = text;
    else delete next[key];
    onChange({ ...value, settings: next });
  };
  return {
    value,
    onChange,
    disabled,
    projectID,
    onRegistryValidityChange,
    onSecretsValidityChange,
    id,
    settings,
    setSetting,
  };
}

type GitComposeFieldsModel = Pick<
  ReturnType<typeof useGitApplicationFields>,
  "settings" | "id" | "setSetting" | "disabled" | "onChange" | "value"
>;
function GitComposeFields(viewModel: GitComposeFieldsModel) {
  const { settings, id, setSetting, disabled, onChange, value } = viewModel;

  if (!(settings.strategy === "compose")) return null;
  return (
    <>
      <div className="space-y-1.5">
        <label htmlFor={`${id}-compose-file`}>Compose file</label>
        <Input
          id={`${id}-compose-file`}
          value={settings.compose_file ?? ""}
          onChange={(event) => setSetting("compose_file", event.target.value)}
          disabled={disabled}
          required
          placeholder="docker-compose.yaml"
          spellCheck={false}
          aria-describedby={`${id}-compose-path-help`}
        />
      </div>
      <p
        id={`${id}-compose-path-help`}
        className="text-xs text-muted-foreground"
      >
        Path relative to the context directory. Use a repository-relative path
        without a leading slash or parent-directory segments.
      </p>
      <p className="break-words text-xs [overflow-wrap:anywhere]">
        Compose path in repository:{" "}
        <code>
          {[settings.context_dir, settings.compose_file]
            .filter(Boolean)
            .join("/") || "No Compose file selected"}
        </code>
      </p>
      <div className="space-y-1.5">
        <label htmlFor={`${id}-compose-service`}>Public Compose service</label>
        <Input
          id={`${id}-compose-service`}
          value={settings.public_component ?? ""}
          onChange={(event) =>
            setSetting("public_component", event.target.value)
          }
          disabled={disabled}
          required
          placeholder="web"
          spellCheck={false}
        />
      </div>
      <div className="space-y-1.5">
        <label htmlFor={`${id}-compose-port`}>
          Public Compose service port
        </label>
        <Input
          id={`${id}-compose-port`}
          type="number"
          min={1}
          max={65535}
          value={settings.container_port ?? ""}
          onChange={(event) =>
            onChange({
              ...value,
              settings: {
                ...settings,
                container_port: Number(event.target.value),
              },
            })
          }
          disabled={disabled}
          required
        />
      </div>
      <p className="text-xs text-muted-foreground">
        Choose the service and container port that will receive application
        traffic. Domains and routes are configured separately.
      </p>
    </>
  );
}

type GitSourceAccessModel = Pick<
  ReturnType<typeof useGitApplicationFields>,
  "id" | "value" | "disabled" | "onChange"
>;
function GitSourceAccess(viewModel: GitSourceAccessModel) {
  const { id, value, disabled, onChange } = viewModel;

  return (
    <div className="space-y-1.5">
      <label htmlFor={`${id}-credentials`}>Git source access</label>
      <select
        id={`${id}-credentials`}
        className="h-10 w-full min-w-0 rounded-xl border border-border bg-card px-2 text-sm"
        value={value.credential_kind}
        disabled={disabled}
        onChange={(event) => {
          const { ssh_host_fingerprints, ...rest } = value;
          onChange({
            ...rest,
            credential_kind: event.target
              .value as GitApplicationDraft["credential_kind"],
            ...(event.target.value === "ssh_private_key"
              ? { ssh_host_fingerprints: ssh_host_fingerprints ?? [] }
              : {}),
          });
        }}
      >
        {!["public", "https_basic", "ssh_private_key"].includes(
          value.credential_kind,
        ) ? (
          <option value={value.credential_kind} disabled>
            Unsupported saved credential kind
          </option>
        ) : null}
        <option value="public">Public HTTPS</option>
        <option value="https_basic">Private HTTPS with a source grant</option>
        <option value="ssh_private_key">Private SSH with a deploy key</option>
      </select>
    </div>
  );
}
