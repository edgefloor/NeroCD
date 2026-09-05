import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ShieldOff, Users } from "lucide-react";
import { toast } from "sonner";
import { listUsers, offboardUser, type User } from "@/api/users";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

export const usersQueryKey = ["users", { limit: 100, offset: 0 }] as const;

export function UsersSettingsPanel(): ReactNode {
  const client = useQueryClient();
  const users = useQuery({ queryKey: usersQueryKey, queryFn: ({ signal }) => listUsers({ signal }) });
  const offboard = useMutation({
    mutationFn: (userID: string) => offboardUser(userID),
    onSuccess: async (result) => {
      await client.invalidateQueries({ queryKey: ["users"] });
      toast.success(`User offboarded; revoked ${result.revoked_sessions} sessions and ${result.revoked_api_tokens} API tokens`);
    },
    onError: (error) => toast.error(error.message),
  });
  if (users.isError) return <p role="alert">{users.error.message}</p>;
  return <UsersSettings users={users.data?.items ?? []} loading={users.isPending} busy={offboard.isPending} onOffboard={(userID) => offboard.mutateAsync(userID)} />;
}

export function UsersSettings({ users, loading, busy, onOffboard }: { users: User[]; loading?: boolean; busy?: boolean; onOffboard: (userID: string) => Promise<unknown> | unknown }): ReactNode {
  const [selected, setSelected] = useState<User>();
  return <Card className="min-w-0 lg:col-span-2"><CardHeader><CardTitle><Users className="mr-2 inline h-4 w-4" />Users</CardTitle></CardHeader><CardContent className="grid min-w-0 gap-3" aria-busy={loading}>{loading ? <p className="text-sm text-muted-foreground" role="status">Loading users…</p> : users.length === 0 ? <p className="text-sm text-muted-foreground">No users are available.</p> : users.map((user) => <div key={user.id} className="flex min-w-0 flex-wrap items-center justify-between gap-3 rounded border p-3"><div className="min-w-0"><p className="break-words font-medium">{user.name}</p><p className="break-all text-sm text-muted-foreground">{user.email}</p><p className="break-all font-mono text-xs text-muted-foreground">{user.id}</p></div><div className="flex flex-wrap items-center gap-2"><Badge variant={user.status === "active" ? "success" : "secondary"}>{user.status}</Badge><Badge variant="outline">{user.global_role}</Badge>{user.status === "active" ? <Button variant="destructive" size="sm" disabled={busy} onClick={() => setSelected(user)}><ShieldOff className="h-4 w-4" />Offboard {user.email}</Button> : null}</div></div>)}<Dialog open={Boolean(selected)} onOpenChange={(open) => !open && setSelected(undefined)}><DialogContent><DialogHeader><DialogTitle>Offboard {selected?.email}?</DialogTitle><DialogDescription>This disables local and OIDC sign-in, revokes every session, and revokes every active API token issued by this user, including automation credentials. Identity mappings and history remain.</DialogDescription></DialogHeader><DialogFooter><Button variant="outline" onClick={() => setSelected(undefined)}>Keep user active</Button><Button variant="destructive" disabled={busy} onClick={async () => { if (!selected) return; await onOffboard(selected.id); setSelected(undefined); }}>Offboard user</Button></DialogFooter></DialogContent></Dialog></CardContent></Card>;
}
