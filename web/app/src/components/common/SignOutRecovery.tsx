import { Button } from "@/components/ui/button";

export function SignOutRecovery({ pending, onRetry }: { pending: boolean; onRetry: () => Promise<void> }) {
  return <main className="min-h-screen bg-background p-6" aria-label="Confirming sign-out">
    <div className="mx-auto grid max-w-lg gap-3">
      <h1 className="text-lg font-semibold">{pending ? "Signing out" : "Sign-out not confirmed"}</h1>
      {pending ? <p role="status">Waiting for the server to end your session...</p> : <>
        <p role="alert" className="text-sm text-muted-foreground">Your server session may still be active. Private data has been cleared from this page. Retry sign-out before leaving this browser.</p>
        <div><Button onClick={() => void onRetry()}>Retry sign-out</Button></div>
      </>}
    </div>
  </main>;
}
