import { FormEvent, ReactNode } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";

export function SignIn({ error, bootstrapRequired = false, onSubmit }: { error: string; bootstrapRequired?: boolean; onSubmit: (email: string, password: string) => Promise<void> }): ReactNode {
  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    await onSubmit(String(form.get("email") ?? ""), String(form.get("password") ?? ""));
  }

  return (
    <main className="min-h-screen bg-background flex items-center justify-center p-4">
      <div className="w-full max-w-[380px] space-y-8">
        {/* Logo */}
        <div className="text-center space-y-3">
          <img
            src="/logo.png"
            alt="NeroCD"
            className="mx-auto h-52 w-52 object-contain"
          />
          <div className="space-y-1">
            <h1 className="text-xl font-semibold tracking-tight">NeroCD</h1>
            <p className="text-sm text-muted-foreground">Automation operations platform</p>
          </div>
        </div>
        <Card className="border shadow-sm">
          <CardHeader className="space-y-1 pb-4 pt-5">
            <CardTitle className="text-base font-semibold">Sign in</CardTitle>
          </CardHeader>
          <CardContent className="pb-5">
            {bootstrapRequired ? (
              <div className="space-y-3" role="status" aria-label="Administrator bootstrap required">
                <p className="text-sm font-medium">Administrator bootstrap required</p>
                <p className="text-sm text-muted-foreground">Bootstrap is intentionally CLI-only.</p>
                <p className="text-sm text-muted-foreground">Create the first administrator with the bootstrap command, then return here to sign in.</p>
              </div>
            ) : <form className="space-y-4" onSubmit={(event) => void submit(event)}>
              <div className="space-y-1.5">
                <label className="text-sm font-medium">Email</label>
                <Input 
                  name="email" 
                  type="email" 
                  autoComplete="email" 
                  required 
                  className="h-9"
                />
              </div>
              <div className="space-y-1.5">
                <label className="text-sm font-medium">Password</label>
                <Input 
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
            </form>}
          </CardContent>
        </Card>
      </div>
    </main>
  );
}
