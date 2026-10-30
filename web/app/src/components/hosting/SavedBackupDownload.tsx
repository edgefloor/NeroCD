import { ApiError } from "@/api/errors";
import { Button } from "@/components/ui/button";
import { useEffect, useEffectEvent, useState } from "react";

type Props = {
  identity: string;
  path: string;
  noun: string;
  title: string;
  reasons: Record<string, string>;
  load: (
    signal: AbortSignal,
  ) => Promise<{ available: boolean; reason_code: string }>;
  onDenied: () => void;
};
export function SavedBackupDownload(props: Props) {
  const { identity, onDenied } = props;
  const [result, setResult] = useState<{
    identity: string;
    available: boolean;
    message: string;
  }>();
  const [refresh, setRefresh] = useState(0);
  const deny = useEffectEvent(onDenied);
  // The keyed session pins the scope and archive evidence for this request.
  const [request] = useState(() => props);
  useEffect(() => {
    const controller = new AbortController();
    setResult(undefined);
    void request
      .load(controller.signal)
      .then((value) => {
        if (!controller.signal.aborted)
          setResult({
            identity,
            available: value.available,
            message: request.reasons[value.reason_code] ?? "",
          });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        if (error instanceof ApiError && [401, 403].includes(error.status))
          deny();
        const message =
          error instanceof ApiError && [401, 403].includes(error.status)
            ? `${request.title} download access is unavailable. Reload to verify access.`
            : error instanceof ApiError && error.status === 404
              ? `The local ${request.noun} copy is unavailable.`
              : `${request.title} download availability could not be verified. Try again.`;
        setResult({ identity, available: false, message });
      });
    return () => controller.abort();
    // Primitive identity includes every authority and archive field used by the request.
  }, [identity, refresh, request]);
  const current = result?.identity === identity ? result : undefined;
  return (
    <div className="space-y-2 text-sm">
      {current?.available ? (
        <>
          <Button
            asChild
            variant="outline"
            className="h-auto max-w-full whitespace-normal"
          >
            <a href={request.path} target="_blank" rel="noopener">
              Download {request.noun}
            </a>
          </Button>
          <p>
            The browser saves the {request.noun}. If its local copy or access
            changes, the download opens an error in a new tab.
          </p>
        </>
      ) : (
        <>
          <Button
            variant="outline"
            disabled
            className="h-auto max-w-full whitespace-normal"
          >
            Download {request.noun}
          </Button>
          <p role="status">
            {current?.message ??
              `Checking ${request.noun} download availability…`}
          </p>
        </>
      )}
      {current ? (
        <Button
          variant="outline"
          className="h-auto max-w-full whitespace-normal"
          onClick={() => {
            setResult(undefined);
            setRefresh((value) => value + 1);
          }}
        >
          Refresh download availability
        </Button>
      ) : null}
    </div>
  );
}
