import { useEffect, useRef, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ShieldOff, Users } from "lucide-react";
import { toast } from "sonner";
import { listUsers, offboardUser, type User, type UserListResponse } from "@/api/users";
import { PageNavigation } from "@/components/common/PageNavigation";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

export const usersPageSize = 10;

export function UsersSettingsPanel({ readOnly = false }: { readOnly?: boolean }): ReactNode {
  const client = useQueryClient();
  const [offset, setOffset] = useState(0);
  const page = { limit: usersPageSize, offset };
  const users = useQuery({
    queryKey: ["users", page],
    queryFn: ({ signal }) => listUsers(page, { signal }),
  });
  const offboard = useMutation({
    mutationFn: (userID: string) => offboardUser(userID),
    onSuccess: async (result) => {
      client.setQueriesData<UserListResponse>({ queryKey: ["users"] }, (cached) => cached ? {
        ...cached,
        items: cached.items.map((user) => user.id === result.user.id ? result.user : user),
      } : cached);
      await Promise.all([
        client.invalidateQueries({ queryKey: ["users"] }),
        client.invalidateQueries({ queryKey: ["service-accounts"] }),
      ]);
      toast.success(`User offboarded; revoked ${result.revoked_sessions} sessions, ${result.revoked_api_tokens} API tokens, and ${result.revoked_service_accounts} service accounts`);
    },
    onError: (error) => toast.error(error.message),
  });
  const result = users.data;
  return <div className="min-w-0 space-y-3 lg:col-span-2">
    {users.isError ? <section className="space-y-3">
      <p role="alert">{result ? "Unable to refresh users. Showing previously loaded users." : "Unable to load users."}</p>
      <div className="flex flex-wrap gap-2">
        <Button disabled={users.isFetching} onClick={() => void users.refetch()}>Retry users</Button>
        {!result && offset > 0 ? <Button variant="outline" onClick={() => setOffset(0)}>First users page</Button> : null}
      </div>
    </section> : null}
    {result || !users.isError ? <UsersSettings
      users={result?.items ?? []}
      page={result}
      loading={users.isPending}
      busy={offboard.isPending}
      readOnly={readOnly || users.isError}
      onFirst={() => setOffset(0)}
      onPrevious={() => setOffset((current) => Math.max(0, current - usersPageSize))}
      onNext={() => setOffset((current) => current + usersPageSize)}
      onOffboard={(userID) => offboard.mutateAsync(userID)}
    /> : null}
  </div>;
}

type UsersSettingsProps = {
  users: User[];
  page?: Pick<UserListResponse, "count" | "limit" | "offset" | "total">;
  loading?: boolean;
  busy?: boolean;
  readOnly?: boolean;
  onFirst?: () => void;
  onPrevious?: () => void;
  onNext?: () => void;
  onOffboard: (userID: string) => Promise<unknown> | unknown;
};

export function UsersSettings({ users, page, loading, busy, readOnly = false, onFirst, onPrevious, onNext, onOffboard }: UsersSettingsProps): ReactNode {
  const [selected, setSelected] = useState<User>();
  const [offboardError, setOffboardError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const submittingRef = useRef(false);
  useEffect(() => {
    if (!selected || loading || submitting) return;
    const current = users.find((user) => user.id === selected.id);
    if (!current || current.status !== "active") {
      setSelected(undefined);
      setOffboardError("");
    } else if (current !== selected) {
      setSelected(current);
    }
  }, [users, selected, loading, submitting]);
  const metadata = page ?? { count: users.length, limit: users.length, offset: 0, total: users.length };
  return (
    <Card className="min-w-0 lg:col-span-2">
      <CardHeader>
        <CardTitle><Users className="mr-2 inline h-4 w-4" />Users</CardTitle>
      </CardHeader>
      <CardContent className="grid min-w-0 gap-3" aria-busy={loading}>
        {loading ? (
          <p className="text-sm text-muted-foreground" role="status">Loading users…</p>
        ) : users.length === 0 ? (
          <p className="text-sm text-muted-foreground">No users on this page.</p>
        ) : users.map((user) => (
          <div key={user.id} className="flex min-w-0 flex-wrap items-center justify-between gap-3 rounded border p-3">
            <div className="min-w-0">
              <p className="break-words font-medium">{user.name}</p>
              <p className="break-all text-sm text-muted-foreground">{user.email}</p>
              <p className="break-all font-mono text-xs text-muted-foreground">{user.id}</p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant={user.status === "active" ? "success" : "secondary"}>{user.status}</Badge>
              <Badge variant="outline">{user.global_role}</Badge>
              {user.status === "active" ? (
                <Button aria-label={`Offboard ${user.email}`} variant="destructive" size="sm" disabled={busy || readOnly || submitting} onClick={() => { setOffboardError(""); setSelected(user); }}>
                  <ShieldOff className="h-4 w-4" />Offboard
                </Button>
              ) : null}
            </div>
          </div>
        ))}
        <PageNavigation page={metadata} label="users" loading={loading || busy || submitting}
          onFirst={() => onFirst?.()} onPrevious={() => onPrevious?.()} onNext={() => onNext?.()} />
        <Dialog open={Boolean(selected)} onOpenChange={(open) => { if (!open && !submittingRef.current) { setSelected(undefined); setOffboardError(""); } }}>
          <DialogContent showCloseButton={!submitting} className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
            <DialogHeader className="min-w-0 pr-6">
              <DialogTitle className="break-all">Offboard {selected?.email}?</DialogTitle>
              <DialogDescription>This disables local and OIDC sign-in, revokes every session and active API token issued by this user, and disables every service account they own. Identity mappings and history remain.</DialogDescription>
            </DialogHeader>
            {offboardError ? <p role="alert" className="text-sm text-destructive">{offboardError}</p> : null}
            <DialogFooter>
              <Button variant="outline" disabled={submitting} onClick={() => { setSelected(undefined); setOffboardError(""); }}>Keep user active</Button>
              <Button
                variant="destructive"
                disabled={busy || readOnly || submitting}
                onClick={async () => {
                  if (!selected || busy || readOnly || submittingRef.current) return;
                  submittingRef.current = true;
                  setSubmitting(true);
                  setOffboardError("");
                  try {
                    await onOffboard(selected.id);
                    setSelected(undefined);
                  } catch {
                    setOffboardError("Unable to offboard this user. Try again.");
                  } finally {
                    submittingRef.current = false;
                    setSubmitting(false);
                  }
                }}
              >
                {submitting ? "Offboarding..." : "Offboard user"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  );
}
