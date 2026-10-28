import type { ServerReadinessStatus } from "@/api/server-readiness";
export const readinessReasons: Record<ServerReadinessStatus["reason"], string> =
  {
    no_sample:
      "No sample has been accepted. Enroll a dedicated runner with hosting_readiness_v1 and start it with --hosting-readiness and --execute.",
    stale:
      "The server reports an expired sample. Check the runner connection and readiness collector, then refresh.",
    authority_unavailable:
      "Current runner authority is unavailable. Verify the active credential and its matching consumed enrollment.",
    restore_hold:
      "Restore ownership is held. Resolve the restore hold through the existing recovery workflow. This diagnostic cannot release it.",
    daemon_mismatch:
      "Docker identity differs from a current binding pin. Verify the intended Docker daemon and resolve ownership through the existing recovery workflow.",
    prerequisite_failed:
      "Profile prerequisites failed. Review the recorded checks below and correct the host configuration.",
    qualified:
      "The server qualified the accepted sample for basic image hosting prerequisites. Deployment still requires its own authorization and checks.",
  };
