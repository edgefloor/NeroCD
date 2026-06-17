import { FormEvent, ReactNode } from "react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";

export function SignIn({ error, onSubmit }: { error: string; onSubmit: (email: string, password: string) => Promise<void> }): ReactNode {
  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    await onSubmit(String(form.get("email") ?? ""), String(form.get("password") ?? ""));
  }

  return (
    <main className="min-h-screen bg-background flex items-center justify-center p-4">
      <div className="w-full max-w-[400px] space-y-6">
        {/* Logo */}
        <div className="text-center space-y-4">
          <img
            src="/logo.png"
            alt="NeroCD"
            className="mx-auto h-64 w-64 object-contain"
          />
          <h1 className="text-xl font-semibold tracking-tight">NeroCD</h1>
        </div>
        <Card className="border shadow-sm">
          <CardHeader className="space-y-1 pb-4">
            <CardTitle className="text-base font-semibold">Sign in</CardTitle>
          </CardHeader>
          <CardContent>
            <form className="space-y-4" onSubmit={(event) => void submit(event)}>
              <div className="space-y-1.5">
                <label className="text-sm font-medium">Email</label>
                <Input 
                  name="email" 
                  type="email" 
                  defaultValue="admin@example.local" 
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
                  defaultValue="admin" 
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
          </CardContent>
        </Card>
      </div>
    </main>
  );
}
