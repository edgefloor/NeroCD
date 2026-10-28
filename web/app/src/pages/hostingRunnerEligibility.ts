import type { Runner } from "@/api";
export function eligibleHostingRunner(runner: Runner): boolean {
  const capabilities = runner.capabilities ?? [];
  return (
    runner.status === "active" &&
    !capabilities.includes("shell") &&
    (capabilities.includes("compose-deploy") ||
      capabilities.includes("compose_deploy"))
  );
}
