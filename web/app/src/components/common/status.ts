export function statusTone(status: string): "good" | "pending" | "danger" | "neutral" {
  if (["ok", "active", "succeeded", "scaffolded", "rolled_back"].includes(status)) {
    return "good";
  }
  if (["waiting_approval", "planned", "queued", "running", "pending", "waiting_confirmation", "assigned", "preparing", "applying", "verifying", "cancel_requested", "rolling_back"].includes(status)) {
    return "pending";
  }
  if (["failed", "error", "canceled", "revoked", "archived", "rollback_failed", "manual_intervention"].includes(status)) {
    return "danger";
  }
  return "neutral";
}

export function statusLabel(status: string): string {
  const labels: Record<string, string> = {
    waiting_approval: "Waiting",
    succeeded: "Succeeded",
    queued: "Queued",
    running: "Running",
    failed: "Failed",
    error: "Error",
    pending: "Pending",
    approved: "Approved",
    planned: "Planned",
    scaffolded: "Scaffolded",
    ok: "Ok",
  };
  return labels[status] ?? status.replaceAll("_", " ");
}
