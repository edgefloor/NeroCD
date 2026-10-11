import type { ActionError } from "@/api/mutations";

export function ActionErrorMessage({ error, actionKey, label }: { error?: ActionError; actionKey: string; label: string }) {
  if (error?.key !== actionKey) return null;
  return <p role="alert" className="min-w-0 max-w-lg break-words text-sm text-destructive [overflow-wrap:anywhere]">{label}: {error.message}</p>;
}
