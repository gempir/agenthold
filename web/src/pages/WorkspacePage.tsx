import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import {
  canManage,
  meQuery,
  membersQuery,
  workspaceQuery,
  type WorkspaceRole,
} from '@/api/queries';
import { InvitesPanel } from '@/components/InvitesPanel';
import { MembersList } from '@/components/MembersList';
import { SettingsPanel } from '@/components/SettingsPanel';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';

type Tab = 'members' | 'invites' | 'settings';

export function WorkspacePage({ workspaceId }: { workspaceId: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [tab, setTab] = useState<Tab>('members');
  const me = useQuery(meQuery);
  const ws = useQuery(workspaceQuery(workspaceId));
  const members = useQuery(membersQuery(workspaceId));

  const path = { workspaceId };
  const refreshMembers = () =>
    qc.invalidateQueries({ queryKey: ['workspaces', workspaceId, 'members'] });

  const changeRole = useMutation({
    mutationFn: (v: { userId: string; role: WorkspaceRole }) =>
      unwrap(
        api.PATCH('/workspaces/{workspaceId}/members/{userId}', {
          params: { path: { ...path, userId: v.userId } },
          body: { role: v.role },
        }),
      ),
    onSuccess: async () => {
      await refreshMembers();
      await qc.invalidateQueries({ queryKey: ['workspaces', workspaceId] });
    },
  });

  const remove = useMutation({
    mutationFn: (userId: string) =>
      unwrap(
        api.DELETE('/workspaces/{workspaceId}/members/{userId}', {
          params: { path: { ...path, userId } },
        }),
      ),
    onSuccess: async (_d, userId) => {
      if (userId === me.data?.user.id) {
        await qc.invalidateQueries({ queryKey: ['workspaces'] });
        await navigate({ to: '/' });
      } else {
        await refreshMembers();
      }
    },
  });

  if (ws.isPending || me.isPending) return <Loading />;
  if (ws.error) return <ErrorText error={ws.error} />;
  if (!ws.data || !me.data) return null;

  const w = ws.data;
  const tabs: { id: Tab; label: string; show: boolean }[] = [
    { id: 'members', label: 'Members', show: true },
    { id: 'invites', label: 'Invites', show: canManage(w.role) },
    { id: 'settings', label: 'Settings', show: true },
  ];

  return (
    <div className="flex flex-col gap-6">
      <header className="flex items-center gap-3">
        <h1 className="text-2xl font-semibold">{w.name}</h1>
        <Badge>{w.role}</Badge>
        {w.personal && <Badge variant="outline">personal</Badge>}
      </header>

      <div role="tablist" className="flex gap-1 border-b">
        {tabs
          .filter((t) => t.show)
          .map((t) => (
            <button
              key={t.id}
              role="tab"
              aria-selected={tab === t.id}
              onClick={() => setTab(t.id)}
              className={cn(
                '-mb-px border-b-2 px-3 py-2 text-sm font-medium',
                tab === t.id
                  ? 'border-primary'
                  : 'border-transparent text-muted-foreground hover:text-foreground',
              )}
            >
              {t.label}
            </button>
          ))}
      </div>

      {tab === 'members' && (
        <section className="flex flex-col gap-4">
          {members.isPending && <Loading />}
          <ErrorText error={members.error} />
          <ErrorText error={changeRole.error} />
          <ErrorText error={remove.error} />
          {members.data && (
            <MembersList
              members={members.data}
              myId={me.data.user.id}
              myRole={w.role}
              personal={w.personal}
              busy={changeRole.isPending || remove.isPending}
              onChangeRole={(userId, role) =>
                changeRole.mutate({ userId, role })
              }
              onRemove={(userId) => remove.mutate(userId)}
              onLeave={() => remove.mutate(me.data.user.id)}
            />
          )}
          <section className="rounded-lg border border-dashed p-4">
            <h2 className="text-sm font-semibold">Agents</h2>
            <p className="text-sm text-muted-foreground">Agents arrive in M2</p>
          </section>
        </section>
      )}
      {tab === 'invites' && canManage(w.role) && (
        <InvitesPanel workspaceId={workspaceId} />
      )}
      {tab === 'settings' && <SettingsPanel key={w.name} workspace={w} />}
    </div>
  );
}
