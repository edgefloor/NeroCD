import { execFileSync } from "node:child_process";
import { expect, test } from "vitest";
import { configuredImageHostingCapabilities, imageHostingCapabilities, runnerEnrollmentCommand, shellQuote } from "./runner-enrollment-command";

test("configured image hosting generates exactly matching protected flags and file-based credentials", () => {
  const { command, issues } = runnerEnrollmentCommand("https://control.example.test/", configuredImageHostingCapabilities);
  expect(issues).toEqual([]);
  expect(command).toContain(`--capabilities '${configuredImageHostingCapabilities.join(",")}'`);
  expect(command).toContain("--hosting-runtime-env --hosting-observation --hosting-runtime-logs --execute");
  expect(command).toContain("--credential-file /secure/nerocd-runner.credential");
  expect(command).not.toContain("--token");
});
test.each([imageHostingCapabilities, ["compose-deploy"], ["compose_deploy"], ["shell"], []].map((capabilities) => ({ capabilities })))("basic and legacy grants retain their command without protected flags: %j", ({ capabilities }) => {
  const { command } = runnerEnrollmentCommand("http://127.0.0.1:18182", capabilities);
  expect(command).toMatch(/^NEROCD_MODE=development nerocd runner/);
  expect(command).toContain(`--capabilities '${capabilities.join(",")}'`);
  expect(command).not.toContain("--hosting-");
});
test.each([["hosting_runtime_logs_v1"], ["hosting_observation_v1"], [...imageHostingCapabilities, "hosting_runtime_env_v1"]].map((capabilities) => ({ capabilities })))("independent protected grants enable only matching flags: %j", ({ capabilities }) => {
  const { command } = runnerEnrollmentCommand("https://control.example.test", capabilities);
  expect(command?.includes("--hosting-runtime-env")).toBe(capabilities.includes("hosting_runtime_env_v1"));
  expect(command?.includes("--hosting-observation")).toBe(capabilities.includes("hosting_observation_v1"));
  expect(command?.includes("--hosting-runtime-logs")).toBe(capabilities.includes("hosting_runtime_logs_v1"));
});
test.each([
  ["hosting_runtime_env_v1"], ["image_deploy", "hosting_runtime_env_v1"], [...configuredImageHostingCapabilities, "shell"], ["shell", "hosting_observation_v1"], ["shell", "hosting_runtime_logs_v1"], ["hosting_runtime_logs_v1", "unknown"], ["custom"], ["hosting_git_https_basic_v1"],
].map((capabilities) => ({ capabilities })))("invalid or custom grants withhold a complete command: %j", ({ capabilities }) => {
  const result = runnerEnrollmentCommand("https://control.example.test", capabilities);
  expect(result.command).toBeUndefined(); expect(result.issues.length).toBeGreaterThan(0);
});
test.each([
  ["hosting_build_v1", "--build-config-file"], ["hosting_git_https_basic_v1", "private_git"], ["proxy_apply", "--proxy-docker-socket"], ["local_build_deploy_v1", "--local-build-deploy"],
])("operator-owned configuration is required for %s", (capability, requirement) => {
  const result = runnerEnrollmentCommand("https://control.example.test", [...imageHostingCapabilities, capability]);
  expect(result.command).toBeUndefined(); expect(result.issues.join(" ")).toContain(requirement);
});
test.each(["http://control.example.test", "http://127.0.0.1:18182"])("protected modes require HTTPS even for %s", (origin) => {
  const result = runnerEnrollmentCommand(origin, configuredImageHostingCapabilities);
  expect(result.command).toBeUndefined(); expect(result.issues.join(" ")).toContain("HTTPS");
});
test.each(["https://user:password@control.example.test", "https://control.example.test/path", "https://control.example.test?", "https://control.example.test#", "https://control.example.test?q=1", "https://control.example.test#part", "file:///tmp/runner", "bad"])("invalid origin %s withholds the command", (origin) => {
  expect(runnerEnrollmentCommand(origin, imageHostingCapabilities).command).toBeUndefined();
});
test("shell quoting preserves literal apostrophes, substitutions, and line breaks", () => {
  const value = "literal ' $(exit 17) `exit 18`\nsecond line";
  expect(execFileSync("/bin/sh", ["-c", `printf %s ${shellQuote(value)}`], { encoding: "utf8" })).toBe(value);
});
test("missing returned metadata cannot certify a command", () => {
  expect(runnerEnrollmentCommand("https://control.example.test", undefined).command).toBeUndefined();
});

test.each(["http://control.example.test", "http://127.0.0.1:18182"])("runtime logs alone require HTTPS at %s", (origin) => {
  const result = runnerEnrollmentCommand(origin, ["hosting_runtime_logs_v1"]);
  expect(result.command).toBeUndefined(); expect(result.issues.join(" ")).toContain("HTTPS");
});

test("readiness preset derives matching execution flags from persisted grants without a token or one-shot mode", async () => {
  const { readinessImageHostingCapabilities } = await import("./runner-enrollment-command");
  const result = runnerEnrollmentCommand("https://control.example.test", readinessImageHostingCapabilities);
  expect(result.issues).toEqual([]); expect(result.command).toContain("--hosting-readiness"); expect(result.command).toContain("--execute"); expect(result.command).toContain("hosting_readiness_v1");
  for (const forbidden of ["--once", "--complete-status", "--token", "shell"]) expect(result.command).not.toContain(forbidden);
  for (const capabilities of [["hosting_readiness_v1", "shell"], ["hosting_readiness_v1", "unknown"]]) expect(runnerEnrollmentCommand("https://control.example.test", capabilities).command).toBeUndefined();
  expect(runnerEnrollmentCommand("http://localhost:18182", ["hosting_readiness_v1"]).command).toBeUndefined();
});
