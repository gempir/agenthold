import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import { accessQuery, agentsQuery, meQuery } from '@/api/queries';
import { Avatar } from '@/components/Avatar';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { SHARED_HELP } from '@/lib/audit';

/** Who can use the agent: its owner and the users it is shared with. */
export function SharingTab({
  agentId,
  isOwner,
}: {
  agentId: string;
  isOwner: boolean;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const me = useQuery(meQuery);
  const q = useQuery(accessQuery(agentId));
  const [email, setEmail] = useState('');

  const refresh = () =>
    qc.invalidateQueries({ queryKey: accessQuery(agentId).queryKey });

  const share = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/agents/{agentId}/shares', {
          params: { path: { agentId } },
          body: { email: email.trim() },
        }),
      ),
    onSuccess: async () => {
      setEmail('');
      await refresh();
    },
  });
  const remove = useMutation({
    mutationFn: (userId: string) =>
      unwrap(
        api.DELETE('/agents/{agentId}/shares/{userId}', {
          params: { path: { agentId, userId } },
        }),
      ),
    onSuccess: async (_d, userId) => {
      if (userId === me.data?.user.id) {
        // Left the agent: it is gone from this user's view.
        await qc.invalidateQueries({ queryKey: agentsQuery.queryKey });
        await navigate({ to: '/' });
        return;
      }
      await refresh();
    },
  });

  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorText error={q.error} />;
  const { owner, shares } = q.data;
  const myId = me.data?.user.id;

  return (
    <div className="grid gap-6">
      <section className="grid gap-2">
        <h2 className="text-sm font-semibold">People with access</h2>
        <ul
          aria-label="People with access"
          className="divide-y rounded-lg border"
        >
          <li className="flex flex-wrap items-center gap-3 p-3">
            <Avatar name={owner.name} url={owner.avatarUrl} />
            <Person name={owner.name} email={owner.email} />
            <Badge>owner</Badge>
          </li>
          {shares.map((s) => {
            const self = s.user.id === myId;
            return (
              <li
                key={s.user.id}
                className="flex flex-wrap items-center gap-3 p-3"
              >
                <Avatar name={s.user.name} url={s.user.avatarUrl} />
                <Person name={s.user.name} email={s.user.email} />
                <Badge variant="outline">shared</Badge>
                {(isOwner || self) && (
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={remove.isPending}
                    aria-label={self ? 'Leave' : `Remove ${s.user.name}`}
                    onClick={() => remove.mutate(s.user.id)}
                  >
                    {self ? 'Leave' : 'Remove'}
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
        {shares.length === 0 && (
          <p className="text-sm text-muted-foreground">
            Not shared with anyone.
          </p>
        )}
        <ErrorText error={remove.error} />
      </section>

      {isOwner && (
        <form
          className="grid gap-3 rounded-lg border p-4"
          onSubmit={(e) => {
            e.preventDefault();
            share.mutate();
          }}
        >
          <h2 className="text-sm font-semibold">Share</h2>
          <label className="grid gap-1 text-sm">
            Email
            <Input
              type="email"
              required
              placeholder="someone@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </label>
          <p className="text-xs text-muted-foreground">{SHARED_HELP}</p>
          <ErrorText error={share.error} />
          <div>
            <Button type="submit" disabled={!email.trim() || share.isPending}>
              Share
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}

function Person({ name, email }: { name: string; email: string }) {
  return (
    <div className="min-w-0 flex-1">
      <div className="truncate text-sm font-medium">{name}</div>
      {email && (
        <div className="truncate text-xs text-muted-foreground">{email}</div>
      )}
    </div>
  );
}
