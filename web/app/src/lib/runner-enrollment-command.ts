export const imageHostingCapabilities = ["compose-deploy", "image_deploy", "image_release_ports_v1"];
export const configuredImageHostingCapabilities = [...imageHostingCapabilities, "hosting_runtime_env_v1", "hosting_observation_v1", "hosting_runtime_logs_v1"];

const supported = new Set(["shell", "compose-deploy", "compose_deploy", "image_deploy", "image_release_ports_v1", "hosting_runtime_env_v1", "hosting_observation_v1", "hosting_runtime_logs_v1", "hosting_build_v1", "hosting_git_https_basic_v1", "proxy_apply", "local_build_deploy_v1"]);
export function shellQuote(value: string): string { return `'${value.replaceAll("'", "'\\''")}'`; }

// Capabilities must come from the created enrollment, never the editable form.
export function runnerEnrollmentCommand(serverURL: string, capabilities: readonly string[] | undefined): { command?: string; issues: string[] } {
  const issues: string[] = [];
  let origin = "";
  try {
    const url = new URL(serverURL);
    if (["https:", "http:"].includes(url.protocol) && !url.username && !url.password && !url.search && !url.hash && ["", "/"].includes(url.pathname) && !serverURL.includes("?") && !serverURL.includes("#")) origin = url.origin;
  } catch { /* Invalid origins cannot produce a command. */ }
  if (!origin) issues.push("Enter an HTTP or HTTPS origin without credentials, a path, query, or fragment.");
  if (!capabilities) return { issues: [...issues, "Enrollment capabilities are unavailable. Verify the persisted grants before starting the runner."] };
  const has = (capability: string) => capabilities.includes(capability);
  const unknown = capabilities.filter((capability) => !supported.has(capability));
  if (unknown.length) issues.push(`Custom capabilities are not verified by this command helper: ${unknown.join(", ")}. Consult the runner configuration before starting it.`);
  if (has("hosting_runtime_env_v1") && (!has("image_deploy") || !has("image_release_ports_v1") || has("shell"))) issues.push("hosting_runtime_env_v1 requires image_deploy and image_release_ports_v1 without shell. Create a corrected enrollment.");
  if (has("hosting_observation_v1") && has("shell")) issues.push("hosting_observation_v1 cannot share shell. Create a corrected enrollment.");
  if (has("hosting_runtime_logs_v1") && has("shell")) issues.push("hosting_runtime_logs_v1 cannot share shell. Create a corrected enrollment.");
  if ((has("hosting_runtime_env_v1") || has("hosting_observation_v1") || has("hosting_runtime_logs_v1")) && origin && !origin.startsWith("https:")) issues.push("Runtime configuration, observations and runtime logs require a runner-reachable HTTPS origin, including in development mode.");
  if (has("hosting_build_v1") || has("hosting_git_https_basic_v1")) issues.push("Build execution requires --build-config-file pointing to an operator-owned, nonsymlink regular mode-0600 JSON file with validated toolchain, target, policy, delegated cgroup and disk limits. Never put credentials in this file.");
  if (has("hosting_git_https_basic_v1")) {
    if (!has("hosting_build_v1")) issues.push("hosting_git_https_basic_v1 requires hosting_build_v1. Create a corrected enrollment.");
    issues.push("Private Git also requires the build configuration's private_git opt-in and matching HTTPS source credential policy.");
  }
  if ((has("hosting_build_v1") || has("local_build_deploy_v1")) && has("shell")) issues.push("Build execution and local build deployment cannot share shell. Create a corrected enrollment.");
  if (has("proxy_apply")) issues.push("Managed proxy execution requires Linux and explicit --proxy-state-root, --proxy-docker-binary, --proxy-docker-socket and --proxy-protocol managed-traefik.file.v1. Supply the actual persistent mode-0700 root, absolute Docker executable and local Unix socket paths.");
  if (has("local_build_deploy_v1")) issues.push("Local build deployment requires --local-build-deploy and the same runner and Docker daemon as the successful build. A runner that also builds needs its validated --build-config-file.");
  if (issues.length) return { issues };
  const flags = [has("hosting_runtime_env_v1") ? "--hosting-runtime-env" : "", has("hosting_observation_v1") ? "--hosting-observation" : "", has("hosting_runtime_logs_v1") ? "--hosting-runtime-logs" : ""].filter(Boolean);
  return { issues, command: `${origin.startsWith("http:") ? "NEROCD_MODE=development " : ""}nerocd runner --server ${shellQuote(origin)} --enrollment-file /secure/nerocd-runner-enrollment.token --credential-file /secure/nerocd-runner.credential --work-dir /var/lib/nerocd/work --journal-dir /var/lib/nerocd/journal --capabilities ${shellQuote(capabilities.join(","))} --compose-image-policy pull ${[...flags, "--execute"].join(" ")}` };
}
