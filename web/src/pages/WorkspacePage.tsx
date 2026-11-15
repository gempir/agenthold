import { WorkspaceToolsPanel } from '@/components/ToolsPanel';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import {
  Bot,
  KeyRound,
  MailPlus,
  Plug,
  Settings2,
  Users,
  type LucideIcon,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  canManage,
  meQuery,
  membersQuery,
  workspaceQuery,
  type WorkspaceRole,
} from '@/api/queries';
import { AgentsSection } from '@/components/AgentsSection';
import { InvitesPanel } from '@/components/InvitesPanel';
import { MembersList } from '@/components/MembersList';
import { SecretsPanel } from '@/components/SecretsPanel';
import { SettingsPanel } from '@/components/SettingsPanel';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { WorkspaceBadge } from '@/components/WorkspaceBadge';
import {
  SettingsHeader,
  SettingsLayout,
  SettingsNavItem,
} from '@/components/layout/SettingsLayout';
import { Crumb, CrumbSep, TopBar } from '@/components/layout/TopBar';

export type WorkspaceTab =
  'agents' | 'members' | 'invites' | 'secrets' | 'tools' | 'settings';

const TABS: {
  id: WorkspaceTab;
  label: string;
  hint: string;
  icon: LucideIcon;
  managers?: boolean;
}[] = [
  {
    id: 'agents',
    label: 'Agents',
    hint: 'Agents in this workspace',
    icon: Bot,
  },
  {
    id: 'members',
    label: 'Members',
    hint: 'People in this workspace',
    icon: Users,
  },
  {
    id: 'invites',
    label: 'Invites',
    hint: 'Invite links for new members',
    icon: MailPlus,
    managers: true,
  },
  {
    id: 'secrets',
    label: 'Secrets',
    hint: 'Credentials the egress proxy injects for agents',
    icon: KeyRound,
  },
  {
    id: 'tools',
    label: 'Tools',
    hint: 'MCP servers and skills shared by agents',
    icon: Plug,
  },
  {
    id: 'settings',
    label: 'Settings',
    hint: 'Name and lifecycle of the workspace',
    icon: Settings2,
  },
];

export function WorkspacePage({
  workspaceId,
  tab = 'agents',
}: {
  workspaceId: string;
  tab?: WorkspaceTab;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
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

  if (ws.isPending || me.isPending)
    return (
      <>
        <TopBar />
        <Loading />
      </>
    );
  if (ws.error)
    return (
      <>
        <TopBar />
        <div className="p-6">
          <ErrorText error={ws.error} />
        </div>
      </>
    );
  if (!ws.data || !me.data) return null;

  const w = ws.data;
  const tabs = TABS.filter((t) => !t.managers || canManage(w.role));
  const current = tabs.find((t) => t.id === tab) ?? tabs[0];

  return (
    <SettingsLayout
      navLabel="Workspace settings"
      crumbs={
        <>
          <Crumb>
            <WorkspaceBadge id={w.id} name={w.name} />
            <span className="truncate">{w.name}</span>
          </Crumb>
          <CrumbSep />
          <Crumb current>Workspace settings</Crumb>
        </>
      }
      nav={tabs.map((t) => (
        <SettingsNavItem key={t.id} active={t.id === current.id}>
          <Link
            to="/w/$workspaceId"
            params={{ workspaceId }}
            search={{ tab: t.id }}
          >
            <t.icon className="size-4 shrink-0" aria-hidden />
            {t.label}
          </Link>
        </SettingsNavItem>
      ))}
    >
      <SettingsHeader title={current.label} description={current.hint}>
        <div className="flex items-center gap-2">
          <Badge>{w.role}</Badge>
          {w.personal && <Badge variant="outline">personal</Badge>}
        </div>
      </SettingsHeader>
      {current.id === 'agents' && <AgentsSection workspaceId={workspaceId} />}
      {current.id === 'members' && (
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
        </section>
      )}
      {current.id === 'invites' && <InvitesPanel workspaceId={workspaceId} />}
      {current.id === 'secrets' && <SecretsPanel workspaceId={workspaceId} />}
      {current.id === 'tools' && (
        <WorkspaceToolsPanel
          workspaceId={workspaceId}
          admin={canManage(w.role)}
        />
      )}
      {current.id === 'settings' && (
        <SettingsPanel key={w.name} workspace={w} />
      )}
    </SettingsLayout>
  );
}
