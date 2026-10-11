import { Button } from "@/components/ui/button";

export function SessionCheck({ failed }: { failed: boolean }) {
  return <main className="min-h-screen bg-background p-6" aria-label={failed ? "Session unavailable" : "Checking session"}>
    {failed ? <div className="grid gap-3">
      <h1 className="text-lg font-semibold">Session unavailable</h1>
      <p role="alert" className="text-sm text-muted-foreground">Unable to check your session. Reload this page to try again.</p>
      <div><Button onClick={() => window.location.reload()}>Reload page</Button></div>
    </div> : <p role="status" className="text-sm text-muted-foreground">Checking session...</p>}
  </main>;
}
