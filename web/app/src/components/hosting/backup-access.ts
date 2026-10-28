import { ApiError } from "@/api/errors";

// Only the response from a live read can retire its authorization session.
// Aborted reads belong to a previous account, resource, or mounted session.
export async function readBackupMetadata<T>(
  signal: AbortSignal,
  read: () => T,
  onDenied: () => void,
  deniedStatuses: readonly number[] = [401, 403],
): Promise<Awaited<T>> {
  try {
    return await read();
  } catch (error) {
    if (
      !signal.aborted &&
      error instanceof ApiError &&
      deniedStatuses.includes(error.status)
    ) {
      onDenied();
    }
    throw error;
  }
}
