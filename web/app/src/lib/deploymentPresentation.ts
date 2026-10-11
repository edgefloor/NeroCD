import type { Deployment, Revision } from "@/api";

export function newDeploymentIntentKey(): string {
  return globalThis.crypto?.randomUUID?.() ?? `deployment-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export function deploymentStateMessage(deployment: Deployment): string {
  const code = deployment.failure_code?.toLowerCase() ?? "";
  if (deployment.status === "failed") {
    if (code.includes("secret")) return "Deployment failed because a required secret was unavailable. Restore the approved binding before retrying.";
    if (code.includes("policy")) return "Deployment failed a policy check. Review the policy and immutable revision provenance before retrying.";
    if (code.includes("runner") || code.includes("expired")) return "Deployment failed after a runner or lease error. Inspect runner connectivity before retrying.";
  }
  const messages: Record<string, string> = {
    queued: "Queued for an eligible runner.", waiting_confirmation: "Waiting for a maintainer confirmation.", assigned: "Awaiting runner preparation.", preparing: "Preparing the immutable deployment plan.", applying: "Applying the declared Compose configuration.", verifying: "Verifying health against the declared policy.", cancel_requested: "Cancellation receipt accepted; the runner will reconcile safely.", rolling_back: "Restoring the previous healthy revision.", rolled_back: "Rollback completed; the previous healthy revision remains active.", rollback_failed: "Rollback failed. Operator intervention is required.", manual_intervention: "Automatic recovery is unsafe. Operator intervention is required.", succeeded: "Healthy revision verified.", failed: "Deployment failed before a safe terminal recovery.", canceled: "Canceled before terminal success.",
  };
  return messages[deployment.status] ?? "Deployment state is being reconciled.";
}

export function revisionLabel(revision: Revision, abbreviated = false): string {
  const ref = revision.requested_ref?.trim() || revision.id;
  const commit = revision.git_commit?.trim();
  return commit ? `${ref} (${abbreviated ? commit.slice(0, 12) : commit})` : `${ref} (commit unavailable)`;
}

