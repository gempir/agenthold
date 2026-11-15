import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, Outlet, useNavigate, useParams } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import { meQuery, workspacesQuery } from '@/api/queries';
import { Avatar } from '@/components/MembersList';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';

function NewWorkspaceDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState('');
  const create = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/workspaces', { body: { name: name.trim() } })),
    onSuccess: async (ws) => {
      await qc.invalidateQueries({ queryKey: ['workspaces'] });
      setName('');
      onOpenChange(false);
      await navigate({ to: '/w/$workspaceId', params: { workspaceId: ws.id } });
    },
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>New workspace</DialogTitle>
            <DialogDescription>You will become its owner.</DialogDescription>
          </DialogHeader>
          <label className="flex flex-col gap-1 text-sm font-medium">
            Name
            <Input
              value={name}
              maxLength={100}
              required
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <ErrorText error={create.error} />
          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={create.isPending || !name.trim()}>
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function AppShell() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const me = useQuery(meQuery);
  const workspaces = useQuery(workspacesQuery);
  const params = useParams({ strict: false }) as { workspaceId?: string };
  const [newOpen, setNewOpen] = useState(false);

  const logout = useMutation({
    mutationFn: async () => {
      await fetch('/api/v1/auth/logout', {
        method: 'POST',
        credentials: 'same-origin',
      });
    },
    onSettled: async () => {
      qc.clear();
      await navigate({ to: '/login', search: {} });
    },
  });

  if (me.isPending) return <Loading />;
  if (me.error) return <ErrorText error={me.error} />;

  return (
    <div className="min-h-screen">
      <header className="border-b">
        <div className="mx-auto flex max-w-4xl flex-wrap items-center gap-3 p-3">
          <Link to="/" className="font-semibold">
            agenthold
          </Link>
          <Select
            aria-label="Workspace"
            value={params.workspaceId ?? ''}
            onChange={(e) =>
              navigate({
                to: '/w/$workspaceId',
                params: { workspaceId: e.target.value },
              })
            }
          >
            {!params.workspaceId && <option value="">Select workspace</option>}
            {workspaces.data?.map((w) => (
              <option key={w.id} value={w.id}>
                {w.name}
              </option>
            ))}
          </Select>
          <Button variant="outline" size="sm" onClick={() => setNewOpen(true)}>
            New workspace
          </Button>
          <div className="ml-auto flex items-center gap-2">
            <Avatar name={me.data.user.name} url={me.data.user.avatarUrl} />
            <span className="text-sm">{me.data.user.name}</span>
            <Link
              to="/settings/tokens"
              className="text-sm text-muted-foreground hover:underline"
            >
              API tokens
            </Link>
            <Link
              to="/settings/providers"
              className="text-sm text-muted-foreground hover:underline"
            >
              Provider logins
            </Link>
            <Button variant="ghost" size="sm" onClick={() => logout.mutate()}>
              Sign out
            </Button>
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-4xl p-4">
        <Outlet />
      </main>
      <NewWorkspaceDialog open={newOpen} onOpenChange={setNewOpen} />
    </div>
  );
}
