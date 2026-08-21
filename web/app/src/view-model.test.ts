import { expect, test } from "vitest";
import { statusTone, summarizeOverview } from "./view-model";
import type { Project, RunLog, TaskRun, TaskTemplate } from "./api";

const projects: Project[] = [
    { id: "proj_platform", name: "Platform", description: "Platform automation", created_at: "2026-06-05T00:00:00Z" },
  ];
const templates: TaskTemplate[] = [
    { id: "tpl_plan", project_id: "proj_platform", name: "Plan", kind: "opentofu", run_spec: { type: "opentofu", inputs: {} }, workflow: { steps: [] }, runner_tags: ["tofu"], requires_ack: false },
    { id: "tpl_patch", project_id: "proj_platform", name: "Patch", kind: "ansible", run_spec: { type: "ansible", inputs: {} }, workflow: { steps: [] }, runner_tags: ["linux"], requires_ack: true },
  ];
const runs: TaskRun[] = [
    {
      id: "run_done",
      project_id: "proj_platform",
      template_id: "tpl_plan",
      run_spec: { type: "opentofu", inputs: {} },
      workflow: { steps: [] },
      workflow_state: { steps: [] },
      runner_tags: ["tofu"],
      status: "succeeded",
      requested_by: "usr_bootstrap",
      started_at: "2026-06-05T00:00:00Z",
      finished_at: "2026-06-05T00:10:00Z",
    },
    {
      id: "run_waiting",
      project_id: "proj_platform",
      template_id: "tpl_patch",
      run_spec: { type: "ansible", inputs: {} },
      workflow: { steps: [] },
      workflow_state: { steps: [] },
      runner_tags: ["linux"],
      status: "waiting_approval",
      requested_by: "usr_bootstrap",
      started_at: "2026-06-05T00:12:00Z",
    },
  ];
const logs: RunLog[] = [
    { id: "log_001", run_id: "run_done", sequence: 1, stream: "stdout", message: "ok", created_at: "2026-06-05T00:01:00Z" },
  ];

test("summarizeOverview counts contract-backed surface data", () => {
  expect(summarizeOverview(projects, templates, runs, logs)).toEqual({
    projectCount: 1,
    templateCount: 2,
    approvalTemplateCount: 1,
    liveRunCount: 1,
    logCount: 1,
  });
});

test("statusTone maps API statuses to UI tones", () => {
  expect(statusTone("succeeded")).toBe("good");
  expect(statusTone("waiting_approval")).toBe("pending");
  expect(statusTone("failed")).toBe("danger");
  expect(statusTone("unknown")).toBe("neutral");
});
