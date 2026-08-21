import { FormEvent, ReactNode } from "react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";

export function SignIn({ error, bootstrapRequired, onSubmit }: { error: string; bootstrapRequired: boolean; onSubmit: (email: string, password: string) => Promise<void> }): ReactNode {
  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    await onSubmit(String(form.get("email") ?? ""), String(form.get("password") ?? ""));
  }

  return (
    <main className="min-h-screen bg-background flex items-center justify-center p-4">
      <div className="w-full max-w-[360px] space-y-8">
        {/* Logo */}
        <div className="text-center space-y-3">
          <img
            src="/logo.png"
            alt="NeroCD"
            className="mx-auto h-16 w-16 object-contain"
          />
          <div>
            <h1 className="text-lg font-semibold tracking-tight">NeroCD</h1>
            <p className="text-sm text-muted-foreground">Automation operations platform</p>
          </div>
        </div>
        <Card className="border shadow-sm">
          <CardHeader className="pb-4">
            <CardTitle className="text-base font-semibold">Sign in</CardTitle>
          </CardHeader>
          <CardContent>
            <form className="space-y-4" onSubmit={(event) => void submit(event)}>
              <div className="space-y-1.5">
                <label htmlFor="sign-in-email" className="text-sm font-medium">Email</label>
                <Input 
                  id="sign-in-email"
                  name="email" 
                  type="email" 
                  autoComplete="email" 
                  required 
                  className="h-9"
                />
              </div>
              <div className="space-y-1.5">
                <label htmlFor="sign-in-password" className="text-sm font-medium">Password</label>
                <Input 
                  id="sign-in-password"
                  name="password" 
                  type="password" 
                  autoComplete="current-password" 
                  required 
                  className="h-9"
                />
              </div>
              <Button className="w-full h-9" type="submit">
                Sign in
              </Button>
              {error ? (
                <p className="text-sm text-destructive">{error}</p>
              ) : null}
            </form>
            {bootstrapRequired ? <aside className="mt-5 rounded-md border border-warning/30 bg-warning/5 p-3 text-sm" role="status" aria-label="Administrator bootstrap required"><p className="font-medium">Administrator bootstrap required</p><p className="mt-1 text-muted-foreground">Run <code>nerocd bootstrap-admin</code> on the server with a strict password file or standard input, then sign in. Bootstrap is intentionally CLI-only.</p></aside> : null}
          </CardContent>
        </Card>
      </div>
    </main>
  );
}
