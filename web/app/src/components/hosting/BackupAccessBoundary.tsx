import { useCallback, useState, type ReactNode } from "react";

// Denial retires the entire request session, including secret drafts and saved
// mutation bodies. Removing query data cannot make that session writable again.
export function BackupAccessBoundary({
  children,
  onDenied,
  message,
}: {
  children: (deny: () => void) => ReactNode;
  onDenied?: () => void;
  message: string;
}) {
  const [denied, setDenied] = useState(false);
  const deny = useCallback(() => {
    setDenied(true);
    onDenied?.();
  }, [onDenied]);
  return denied ? <p role="alert">{message}</p> : children(deny);
}
