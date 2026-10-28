import type { HostingBuildSettings } from "./DockerfileBuildFields";
export function gitBuildVariablesError(settings: HostingBuildSettings): string {
  const rows = settings.build_variables ?? [];
  if (rows.length > 64) return "At most 64 build variables are allowed.";
  const names = new Set<string>();
  const reserved = new Set([
    "HTTP_PROXY",
    "HTTPS_PROXY",
    "FTP_PROXY",
    "ALL_PROXY",
    "NO_PROXY",
    "BUILDPLATFORM",
    "BUILDOS",
    "BUILDARCH",
    "BUILDVARIANT",
    "TARGETPLATFORM",
    "TARGETOS",
    "TARGETARCH",
    "TARGETVARIANT",
  ]);
  for (const row of rows) {
    if (!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(row.name))
      return "Build variable names must use ASCII letters, numbers or underscores and start with a letter or underscore, with at most 128 characters.";
    if (
      /^(NEROCD_|BUILDKIT_|BUILDX_)/i.test(row.name) ||
      reserved.has(row.name.toUpperCase())
    )
      return "This build variable name is reserved for build execution.";
    if (names.has(row.name)) return "Build variable names must be unique.";
    names.add(row.name);
    if (
      new TextEncoder().encode(row.value).length > 4096 ||
      /\0/.test(row.value)
    )
      return "Build variable values allow at most 4096 UTF-8 bytes and no NUL.";
  }
  const secrets = settings.build_secrets ?? [];
  if (secrets.length > 64) return "At most 64 build secrets are allowed.";
  const secretNames = new Set<string>();
  for (const row of secrets) {
    if (!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(row.name))
      return "Build secret names must use ASCII letters, numbers or underscores and start with a letter or underscore, with at most 128 characters.";
    if (
      /^(NEROCD_|BUILDKIT_|BUILDX_)/i.test(row.name) ||
      reserved.has(row.name.toUpperCase())
    )
      return "This build secret name is reserved for build execution.";
    if (secretNames.has(row.name)) return "Build secret names must be unique.";
    if (names.has(row.name))
      return "Build secret names must differ from public build variable names.";
    if (
      !row.secret.secret_id ||
      !Number.isSafeInteger(row.secret.version) ||
      row.secret.version < 1
    )
      return "Choose a managed secret and explicit version for every build secret.";
    secretNames.add(row.name);
  }
  return "";
}
