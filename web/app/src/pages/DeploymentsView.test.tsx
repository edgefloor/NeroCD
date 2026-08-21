import { expect, test } from "vitest";
import { deploymentStateMessage, newDeploymentIntentKey } from "./DeploymentsView";

test("deployment lifecycle has explicit safe operator messages", () => {
  const fixture = (status: string, failure_code?: string) => ({ status, failure_code }) as never;
  expect(deploymentStateMessage(fixture("waiting_confirmation"))).toMatch(/confirmation/i);
  expect(deploymentStateMessage(fixture("applying"))).toMatch(/applying/i);
  expect(deploymentStateMessage(fixture("cancel_requested"))).toMatch(/cancellation/i);
  expect(deploymentStateMessage(fixture("rolled_back"))).toMatch(/rollback/i);
  expect(deploymentStateMessage(fixture("rollback_failed"))).toMatch(/intervention/i);
  expect(deploymentStateMessage(fixture("manual_intervention"))).toMatch(/intervention/i);
  expect(deploymentStateMessage(fixture("failed", "missing_secret"))).toMatch(/secret/i);
  expect(deploymentStateMessage(fixture("failed", "policy_denied"))).toMatch(/policy/i);
  expect(deploymentStateMessage(fixture("failed", "runner_expired"))).toMatch(/runner/i);
});

test("operator intent keys are fresh, opaque browser-generated values", () => {
  expect(newDeploymentIntentKey()).not.toBe(newDeploymentIntentKey());
});
