import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Check, Copy } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { invitesQuery } from '@/api/queries';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { ErrorText, Loading } from '@/components/Spinner';

export function InvitesPanel({ workspaceId }: { workspaceId: string }) {
  const qc = useQueryClient();
  const invites = useQuery(invitesQuery(workspaceId, true));
  const [role, setRole] = useState<'admin' | 'member'>('member');
  const [email, setEmail] = useState('');
  const [created, setCreated] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const refresh = () =>
    qc.invalidateQueries({ queryKey: ['workspaces', workspaceId, 'invites'] });

  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/workspaces/{workspaceId}/invites', {
          params: { path: { workspaceId } },
          body: { role, ...(email.trim() ? { email: email.trim() } : {}) },
        }),
      ),
    onSuccess: async (inv) => {
      setCreated(inv.url);
      setCopied(false);
      setEmail('');
      await refresh();
    },
  });

  const revoke = useMutation({
    mutationFn: (inviteId: string) =>
      unwrap(
        api.DELETE('/workspaces/{workspaceId}/invites/{inviteId}', {
          params: { path: { workspaceId, inviteId } },
        }),
      ),
    onSuccess: refresh,
  });

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <form
        className="flex flex-col gap-3 rounded-lg border p-4"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <h3 className="text-sm font-semibold">Create invite</h3>
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1 text-xs text-muted-foreground">
            Role
            <Select
              aria-label="Invite role"
              value={role}
              onChange={(e) => setRole(e.target.value as 'admin' | 'member')}
            >
              <option value="member">member</option>
              <option value="admin">admin</option>
            </Select>
          </label>
          <label className="flex min-w-48 flex-1 flex-col gap-1 text-xs text-muted-foreground">
            Email (optional)
            <Input
              type="email"
              placeholder="person@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </label>
          <Button type="submit" disabled={create.isPending}>
            Create invite
          </Button>
        </div>
        <ErrorText error={create.error} />
        {created && (
          <div className="flex flex-col gap-1 rounded-md bg-muted p-3">
            <p className="text-xs text-muted-foreground">
              Share this link. It will not be shown again.
            </p>
            <div className="flex items-center gap-2">
              <code className="min-w-0 flex-1 break-all text-xs">
                {created}
              </code>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => copy(created)}
              >
                {copied ? (
                  <Check className="size-4" />
                ) : (
                  <Copy className="size-4" />
                )}
                {copied ? 'Copied' : 'Copy'}
              </Button>
            </div>
          </div>
        )}
      </form>

      <section>
        <h3 className="mb-2 text-sm font-semibold">Pending invites</h3>
        {invites.isPending && <Loading />}
        <ErrorText error={invites.error} />
        <ErrorText error={revoke.error} />
        {invites.data?.length === 0 && (
          <p className="text-sm text-muted-foreground">No pending invites.</p>
        )}
        {!!invites.data?.length && (
          <ul className="divide-y rounded-lg border">
            {invites.data.map((i) => (
              <li
                key={i.id}
                className="flex flex-wrap items-center gap-3 p-3 text-sm"
              >
                <Badge>{i.role}</Badge>
                <span className="min-w-0 flex-1 truncate">
                  {i.email || 'Anyone with the link'}
                  <span className="ml-2 text-xs text-muted-foreground">
                    by {i.createdBy}, expires{' '}
                    {new Date(i.expiresAt).toLocaleDateString()}
                  </span>
                </span>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={revoke.isPending}
                  onClick={() => revoke.mutate(i.id)}
                >
                  Revoke
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
