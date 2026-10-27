import { useState } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { GitApplicationFields, emptyGitDraft, type GitApplicationDraft } from "./GitApplicationFields";
afterEach(cleanup);
function Editor({ initial = emptyGitDraft(), disabled = false }: { initial?: GitApplicationDraft; disabled?: boolean } = {}) { const [value, setValue] = useState<GitApplicationDraft>(initial); return <><GitApplicationFields value={value} onChange={setValue} disabled={disabled} /><output>{JSON.stringify(value)}</output></>; }
test("public is explicit by default and private access survives all builder switches", () => { render(<Editor />); expect(screen.getByLabelText<HTMLSelectElement>("Git source access").value).toBe("public"); fireEvent.change(screen.getByLabelText("Git source access"), { target: { value: "https_basic" } }); for (const strategy of ["dockerfile", "railpack", "static", "compose"]) { fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: strategy } }); expect(screen.getByLabelText<HTMLSelectElement>("Git source access").value).toBe("https_basic"); expect(screen.getByRole("status").textContent).toContain(`"credential_kind":"https_basic"`); } expect(screen.queryByLabelText(/password|secret value/i)).toBeNull(); });


test("Compose fields use context-relative paths and clear other builder settings", () => {
  const initial = { repository_url: "https://github.com/team/app", requested_ref: "release", credential_kind: "https_basic", settings: { strategy: "dockerfile", context_dir: "services/api", dockerfile_path: "build/Dockerfile", build_target: "production" } } as GitApplicationDraft;
  render(<Editor initial={initial} />);
  fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: "compose" } });
  expect(screen.queryByLabelText("Dockerfile path")).toBeNull();
  expect(screen.getByLabelText<HTMLInputElement>("Compose file").required).toBe(true);
  expect(screen.getByLabelText<HTMLInputElement>("Public Compose service").required).toBe(true);
  fireEvent.change(screen.getByLabelText("Compose file"), { target: { value: "deploy/compose.yaml" } });
  fireEvent.change(screen.getByLabelText("Public Compose service"), { target: { value: "web" } });
  fireEvent.change(screen.getByLabelText("Public Compose service port"), { target: { value: "3456" } });
  expect(screen.getByText("services/api/deploy/compose.yaml")).toBeTruthy();
  const draft = JSON.parse(screen.getByRole("status").textContent!);
  expect(draft).toEqual({ ...initial, settings: { strategy: "compose", context_dir: "services/api", compose_file: "deploy/compose.yaml", public_component: "web", container_port: 3456 } });
});

test.each(["dockerfile", "railpack", "static"])("switching Compose to %s omits all Compose fields", strategy => {
  const initial = { ...emptyGitDraft(), settings: { strategy: "compose", context_dir: "app", compose_file: "compose.yaml", public_component: "web", container_port: 80 } } as GitApplicationDraft;
  render(<Editor initial={initial} />);
  fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: strategy } });
  expect(JSON.parse(screen.getByRole("status").textContent!).settings).toEqual({ strategy, context_dir: "app" });
  expect(screen.queryByLabelText("Compose file")).toBeNull();
  expect(screen.queryByLabelText("Public Compose service")).toBeNull();
});

test("switching static to Compose removes publish and SPA settings", () => {
  render(<Editor initial={{ ...emptyGitDraft(), settings: { strategy: "static", publish_dir: "dist", spa_fallback: true } }} />);
  fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: "compose" } });
  expect(JSON.parse(screen.getByRole("status").textContent!).settings).toEqual({ strategy: "compose", compose_file: "docker-compose.yaml", public_component: "", container_port: 80 });
  expect(screen.getByText("docker-compose.yaml")).toBeTruthy();
});

test("saved Compose settings remain readable and locked while source writes are disabled", () => {
  render(<Editor disabled initial={{ ...emptyGitDraft(), settings: { strategy: "compose", compose_file: "compose.yaml", public_component: "web", container_port: 8080 } }} />);
  for (const label of ["Compose file", "Public Compose service", "Public Compose service port", "Git branch, tag, or commit", "Context directory"]) expect(screen.getByLabelText<HTMLInputElement>(label).disabled).toBe(true);
});
