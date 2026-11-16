import { lazy, Suspense } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { MessageSquare } from 'lucide-react';
import { agentQuery, type Agent } from '@/api/queries';
import { ActivityTab } from '@/components/ActivityTab';
import { AgentSecretsTab } from '@/components/AgentSecretsTab';
import { AgentStateDot } from '@/components/AgentStateBadge';
import { FilesTab } from '@/components/FilesTab';
import { HarnessPanel } from '@/components/HarnessPanel';
import { PreviewTab } from '@/components/PreviewTab';
import { SharingTab } from '@/components/SharingTab';
import { ErrorText, Loading } from '@/components/Spinner';
import { AgentToolsPanel } from '@/components/ToolsPanel';
import { TriggersTab } from '@/components/TriggersTab';
import { Button } from '@/components/ui/button';
import {
  AgentControls,
  AgentDetails,
  AgentSettingsForm,
  DeleteAgent,
} from '@/components/agent/AgentControls';
import { AgentLoadError } from '@/components/agent/AgentLoadError';
import { AGENT_SECTIONS, type AgentSection } from '@/components/agent/sections';
import {
  SettingsHeader,
  SettingsLayout,
  SettingsNavItem,
} from '@/components/layout/SettingsLayout';
import { Crumb, CrumbSep, TopBar } from '@/components/layout/TopBar';
import { useAgentActions } from '@/hooks/useAgentActions';
import { accessOf } from '@/pages/AgentPage';

const TerminalTab = lazy(() =>
  import('@/components/TerminalTab').then((m) => ({ default: m.TerminalTab })),
);

/** Everything about an agent that is not the chat, one section at a time. */
export function AgentSettingsPage({
  agentId,
  tab = 'general',
}: {
  agentId: string;
  tab?: AgentSection;
}) {
  const q = useQuery(agentQuery(agentId));

  if (q.isPending)
    return (
      <>
        <TopBar />
        <Loading />
      </>
    );
  if (q.error)
    return (
      <>
        <TopBar />
        <AgentLoadError error={q.error} />
      </>
    );
  const a = q.data;
  const access = accessOf(a);
  const sections = AGENT_SECTIONS.filter((s) => s.show(access));
  const current = sections.find((s) => s.id === tab) ?? sections[0];

  return (
    <SettingsLayout
      navLabel="Agent settings"
      crumbs={
        <>
          <Crumb>
            <AgentStateDot state={a.state} />
            <Link
              to="/a/$agentId"
              params={{ agentId }}
              className="truncate hover:text-foreground"
            >
              {a.name}
            </Link>
          </Crumb>
          <CrumbSep />
          <Crumb current>Settings</Crumb>
        </>
      }
      actions={
        <Button
          asChild
          variant="ghost"
          size="sm"
          className="text-muted-foreground"
        >
          <Link to="/a/$agentId" params={{ agentId }}>
            <MessageSquare className="size-4" />
            Back to chat
          </Link>
        </Button>
      }
      nav={sections.map((s) => (
        <SettingsNavItem key={s.id} active={s.id === current.id}>
          <Link
            to="/a/$agentId/settings"
            params={{ agentId }}
            search={{ tab: s.id }}
          >
            <s.icon className="size-4 shrink-0" aria-hidden />
            {s.label}
          </Link>
        </SettingsNavItem>
      ))}
    >
      <SettingsHeader title={current.label} description={current.hint} />
      <Section agent={a} section={current.id} />
    </SettingsLayout>
  );
}

function Section({
  agent: a,
  section,
}: {
  agent: Agent;
  section: AgentSection;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { action, busy } = useAgentActions(a);
  const { canUse, isOwner } = accessOf(a);
  const setAgent = (n: Agent) => qc.setQueryData(['agents', a.id], n);
  switch (section) {
    case 'general':
      return (
        <div className="grid gap-6">
          <AgentControls agent={a} canUse={canUse} />
          <AgentDetails agent={a} className="rounded-xl border p-4" />
          {isOwner && (
            <AgentSettingsForm key={a.updatedAt} agent={a} onSaved={setAgent} />
          )}
          {isOwner && <DeleteAgent agent={a} />}
        </div>
      );
    case 'harness':
      return <HarnessPanel agent={a} isOwner={isOwner} onSaved={setAgent} />;
    case 'files':
      return (
        <>
          <FilesTab
            agentId={a.id}
            canUse={canUse}
            starting={busy}
            onStart={() => action.mutate('start')}
          />
          <ErrorText error={action.error} />
        </>
      );
    case 'terminal':
      return canUse ? (
        <>
          {a.state === 'sleeping' && (
            <p className="mb-2 text-sm text-muted-foreground">
              Opening the terminal wakes the agent
            </p>
          )}
          <Suspense fallback={<Loading />}>
            <TerminalTab agentId={a.id} />
          </Suspense>
        </>
      ) : null;
    case 'preview':
      return <PreviewTab agentId={a.id} isOwner={isOwner} />;
    case 'secrets':
      return <AgentSecretsTab agentId={a.id} isOwner={isOwner} />;
    case 'tools':
      return <AgentToolsPanel agentId={a.id} isOwner={isOwner} />;
    case 'triggers':
      return (
        <TriggersTab
          agentId={a.id}
          isOwner={isOwner}
          canUse={canUse}
          onOpenSession={(id) =>
            void navigate({
              to: '/a/$agentId',
              params: { agentId: a.id },
              search: { s: id },
            })
          }
        />
      );
    case 'sharing':
      return <SharingTab agentId={a.id} isOwner={isOwner} />;
    case 'activity':
      return isOwner ? <ActivityTab agentId={a.id} /> : null;
  }
}
