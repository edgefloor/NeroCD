import { Button } from "@/components/ui/button";

// Keep this fallback in the entry bundle so a failed page download cannot hide it.
export function PageUnavailable() {
  return (
    <section className="grid min-w-0 gap-3 p-6" aria-labelledby="page-unavailable-title">
      <h1 id="page-unavailable-title" className="text-lg font-semibold">Page unavailable</h1>
      <p role="alert" className="text-sm text-muted-foreground">This page could not be loaded. Reload it to try again.</p>
      <div><Button onClick={() => window.location.reload()}>Reload page</Button></div>
    </section>
  );
}
