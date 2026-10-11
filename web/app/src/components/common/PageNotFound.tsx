import { Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";

export function PageNotFound() {
  return (
    <section className="grid min-w-0 gap-3 p-6" aria-labelledby="page-not-found-title">
      <h1 id="page-not-found-title" className="text-lg font-semibold">Page not found</h1>
      <p className="text-sm text-muted-foreground">Check the address or return to the dashboard.</p>
      <div><Button asChild><Link to="/">Go to dashboard</Link></Button></div>
    </section>
  );
}
