import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import {
  accessQuery,
  membersQuery,
  type AgentRole,
  type Grant,
} from '@/api/queries';
import { Avatar } from '@/components/MembersList';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Select } from '@/components/ui/select';
import { ROLE_HELP, VIA_LABEL } from '@/lib/audit';

const ROLES: AgentRole[] = ['viewer', 'operator', 'admin'];

export function SharingTab({
  agentId,
  workspaceId,
  admin,
}: {
  agentId: string;
  workspaceId: string;
  admin: boolean;
}) {
  const qc = useQueryClient();
  const q = useQuery(accessQuery(agentId));
  const members = useQuery({ ...membersQuery(workspaceId), enabled: admin });
  const [subject, setSubject] = useState('workspace');
  const [role, setRole] = useState<AgentRole>('viewer');

  const refresh = () =>
    qc.invalidateQueries({ queryKey: ['agents', agentId, 'grants'] });

  const set = useMutation({
    mutationFn: (b: {
      subjectType: 'user' | 'workspace';
      userId?: string;
      role: AgentRole;
    }) =>
      unwrap(
        api.POST('/agents/{agentId}/grants', {
          params: { path: { agentId } },
          body: b,
        }),
      ),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: (grantId: string) =>
      unwrap(
        api.DELETE('/agents/{agentId}/grants/{grantId}', {
          params: { path: { agentId, grantId } },
        }),
      ),
    onSuccess: refresh,
  });

  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorText error={q.error} />;
  const { effective, grants } = q.data;

  const implicit = new Set(
    effective
      .filter((e) => e.via === 'owner' || e.via === 'workspace_admin')
      .map((e) => e.user.id),
  );
  const candidates = (members.data ?? []).filter(
    (m) => !implicit.has(m.user.id),
  );
  const busy = set.isPending || remove.isPending;

  const changeRole = (g: Grant, r: AgentRole) =>
    set.mutate(
      g.subjectType === 'workspace'
        ? { subjectType: 'workspace', role: r }
        : { subjectType: 'user', userId: g.user?.id, role: r },
    );

  return (
    <div className="grid gap-6">
      <section className="grid gap-2">
        <h2 className="text-sm font-semibold">People with access</h2>
        <ul
          aria-label="People with access"
          className="divide-y rounded-lg border"
        >
          {effective.map((e) => (
            <li
              key={e.user.id}
              className="flex flex-wrap items-center gap-3 p-3"
            >
              <Avatar name={e.user.name} url={e.user.avatarUrl} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">
                  {e.user.name}
                </div>
                <div className="truncate text-xs text-muted-foreground">
                  {e.user.email}
                </div>
              </div>
              <span className="text-xs text-muted-foreground">
                {VIA_LABEL[e.via] ?? e.via}
              </span>
              <Badge>{e.role}</Badge>
            </li>
          ))}
        </ul>
      </section>

      <section className="grid gap-2">
        <h2 className="text-sm font-semibold">Grants</h2>
        {grants.length === 0 && (
          <p className="text-sm text-muted-foreground">
            Nothing has been shared yet.
          </p>
        )}
        <ul aria-label="Grants" className="divide-y rounded-lg border">
          {grants.map((g) => {
            const who =
              g.subjectType === 'workspace'
                ? 'Everyone in this workspace'
                : (g.user?.name ?? 'Unknown user');
            return (
              <li key={g.id} className="flex flex-wrap items-center gap-3 p-3">
                <span className="min-w-0 flex-1 truncate text-sm">{who}</span>
                {admin ? (
                  <>
                    <Select
                      aria-label={`Role for ${who}`}
                      value={g.role}
                      disabled={busy}
                      onChange={(e) =>
                        changeRole(g, e.target.value as AgentRole)
                      }
                    >
                      {ROLES.map((r) => (
                        <option key={r} value={r}>
                          {r}
                        </option>
                      ))}
                    </Select>
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={busy}
                      aria-label={`Remove ${who}`}
                      onClick={() => remove.mutate(g.id)}
                    >
                      Remove
                    </Button>
                  </>
                ) : (
                  <Badge>{g.role}</Badge>
                )}
              </li>
            );
          })}
        </ul>
        <ErrorText error={remove.error} />
      </section>

      {admin && (
        <form
          className="grid gap-3 rounded-lg border p-4"
          onSubmit={(e) => {
            e.preventDefault();
            set.mutate(
              subject === 'workspace'
                ? { subjectType: 'workspace', role }
                : { subjectType: 'user', userId: subject, role },
            );
          }}
        >
          <h2 className="text-sm font-semibold">Share</h2>
          <label className="grid gap-1 text-sm">
            With
            <Select
              value={subject}
              onChange={(e) => setSubject(e.target.value)}
            >
              <option value="workspace">Everyone in this workspace</option>
              {candidates.map((m) => (
                <option key={m.user.id} value={m.user.id}>
                  {m.user.name} ({m.user.email})
                </option>
              ))}
            </Select>
          </label>
          <label className="grid gap-1 text-sm">
            Role
            <Select
              value={role}
              onChange={(e) => setRole(e.target.value as AgentRole)}
            >
              {ROLES.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </Select>
          </label>
          <p className="text-xs text-muted-foreground">
            {ROLES.map((r) => (
              <span key={r} className="block">
                <strong>{r}</strong>: {ROLE_HELP[r]}
              </span>
            ))}
          </p>
          <ErrorText error={set.error} />
          <div>
            <Button type="submit" disabled={busy}>
              Share
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}
