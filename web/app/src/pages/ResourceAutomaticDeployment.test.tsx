import { StrictMode, useState } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { AutomaticDeploymentConfiguration, AutomaticDeploymentOptions, AutomaticDeploymentStatus } from "@/api/hosting-github-push";
import { AutomaticDeploymentSummary, ResourceAutomaticDeployment, type AutomaticDeploymentDraft } from "./ResourceAutomaticDeployment";

afterEach(cleanup);
const options: AutomaticDeploymentOptions = { available: true, reason: "eligible", baseline: { healthy_operation_id: "hop_a", loaded_proxy_operation_id: "pop_a", loaded_generation: 7, runtime_configuration_generation: 2, container_port: 8080, readiness: { path: "/ready", timeout_seconds: 10 }, manual_epoch: 1, routes: [{ route_id: "internal_route_a", hostname: "web.example.test", path_prefix: "/app", version: 3, selected_release_operation_id: "hop_a" }] } };
const saved: AutomaticDeploymentConfiguration = { mode: "automatic_unprotected", expected_healthy_operation_id: "hop_a", expected_loaded_proxy_operation_id: "pop_a", expected_loaded_generation: 7, expected_runtime_configuration_generation: 2, container_port: 8080, readiness: { path: "/ready", timeout_seconds: 10 }, routes: [{ route_id: "internal_route_a", expected_version: 3, expected_selected_release_operation_id: "hop_a" }] };

test("build-only default and explicit automatic selection use route labels and readonly healthy settings", () => {
  const changed = vi.fn();
  function Harness() {
    const [draft, setDraft] = useState<AutomaticDeploymentDraft>({ mode: "build_only", routeIDs: [], baselineKey: "" });
    return <ResourceAutomaticDeployment options={options} draft={draft} locked={false} onChange={(next) => { setDraft(next); changed(next); }} />;
  }
  render(<StrictMode><Harness /></StrictMode>);
  expect(screen.getByRole<HTMLInputElement>("radio", { name: /Build only/ }).checked).toBe(true);
  expect(screen.queryByRole("checkbox")).toBeNull();
  fireEvent.click(screen.getByRole("radio", { name: "Automatic deployment without approval" }));
  expect(screen.getByText(/does not configure or enforce GitHub branch protection/)).toBeTruthy();
  expect(screen.getByText(/Container port 8080; readiness \/ready, timeout 10 seconds/)).toBeTruthy();
  fireEvent.click(screen.getByRole("checkbox", { name: "https://web.example.test/app" }));
  expect(changed).toHaveBeenLastCalledWith({ mode: "automatic_unprotected", routeIDs: ["internal_route_a"], baselineKey: JSON.stringify(options.baseline) });
  expect(screen.queryByText("internal_route_a")).toBeNull();
  expect(screen.queryByRole("textbox")).toBeNull();
});

test("baseline drift requires explicit review and clears selections without mutating the retained saved policy", () => {
  const changed = vi.fn();
  const draft: AutomaticDeploymentDraft = { mode: "automatic_unprotected", routeIDs: ["internal_route_a"], baselineKey: JSON.stringify(options.baseline) };
  const view = render(<ResourceAutomaticDeployment options={options} saved={saved} draft={draft} locked={false} onChange={changed} />);
  const drifted: AutomaticDeploymentOptions = { ...options, baseline: { ...options.baseline!, loaded_generation: 8 } };
  view.rerender(<ResourceAutomaticDeployment options={drifted} saved={saved} draft={draft} locked={false} onChange={changed} />);
  expect(screen.getByRole("alert").textContent).toMatch(/baseline changed/);
  expect(screen.queryByRole("checkbox")).toBeNull();
  expect(changed).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Review current automatic deployment baseline" }));
  expect(changed).toHaveBeenCalledWith({ mode: "automatic_unprotected", routeIDs: [], baselineKey: JSON.stringify(drifted.baseline) });
  expect(saved.expected_loaded_generation).toBe(7);
});

test("unavailable options retain saved automatic mode and allow only an explicit build-only choice", () => {
  const changed = vi.fn();
  render(<ResourceAutomaticDeployment options={{ available: false, reason: "restore_hold" }} saved={saved} draft={{ mode: "automatic_unprotected", routeIDs: ["internal_route_a"], baselineKey: "retained" }} locked={false} onChange={changed} />);
  expect(screen.getByRole<HTMLInputElement>("radio", { name: "Automatic deployment without approval" }).checked).toBe(true);
  expect(screen.getByRole("status").textContent).toMatch(/restore hold.*saved automatic configuration is retained/);
  expect(screen.queryByRole("checkbox")).toBeNull(); expect(changed).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("radio", { name: /Build only/ }));
  expect(changed).toHaveBeenCalledWith({ mode: "build_only", routeIDs: [], baselineKey: "" });
});

test.each(["waiting_deployment", "waiting_switch", "succeeded", "failed", "blocked", "expired"] as const)("%s summary reports recorded operations without an uptime or generic reset claim", (state) => {
  const status: AutomaticDeploymentStatus = { mode: "automatic_unprotected", state, reason: state === "succeeded" ? "https_verified" : state === "blocked" ? "switch_hold" : "build_pending", manual_epoch: 1, desired_commit: "a".repeat(40), build_id: "build_b", deployment_operation_id: "deployment_b", switch_operation_id: "switch_b", attempts: 2, observed_loaded_generation: state === "succeeded" ? 8 : 0 };
  render(<AutomaticDeploymentSummary status={status} />);
  expect(screen.getByText(/last operation result. Check runtime status for current health/)).toBeTruthy();
  expect(screen.getByText("deployment_b")).toBeTruthy(); expect(screen.getByText("switch_b")).toBeTruthy();
  expect(screen.getByText(/does not reset this automatic deployment's attempt budget/)).toBeTruthy();
  expect(screen.queryByRole("button")).toBeNull();
});
