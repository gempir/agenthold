import { lazy, Suspense, type ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import {
  ChevronRight,
  Loader2,
  Maximize2,
  Minimize2,
  Plus,
  TriangleAlert,
  X,
} from 'lucide-react';
import { sessionsQuery, type Agent } from '@/api/queries';
import { FilesTab } from '@/components/FilesTab';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { AgentControls, AgentDetails } from '@/components/agent/AgentControls';
import { useHarnessName } from '@/components/agent/HarnessMeta';
import { AGENT_SECTIONS, type Access } from '@/components/agent/sections';
import { useAgentActions } from '@/hooks/useAgentActions';
import { relTime, useNow } from '@/lib/time';
import { cn } from '@/lib/utils';

const TerminalTab = lazy(() =>
  import('@/components/TerminalTab').then((m) => ({ default: m.TerminalTab })),
);

export type PanelTab = 'overview' | 'files' | 'terminal';
export const isPanelTab = (v: unknown): v is PanelTab =>
  v === 'overview' || v === 'files' || v === 'terminal';

/**
 * The floating right-hand panel next to a chat: the agent's status, its other
 * conversations, quick files and terminal, and the way into its settings.
 */
export function AgentPanel({
  agent,
  access,
  tab,
  sessionId,
  wide,
  onTab,
  onWide,
  onClose,
}: {
  agent: Agent;
  access: Access;
  tab: PanelTab;
  sessionId?: string;
  wide: boolean;
  onTab: (t: PanelTab) => void;
  onWide: (w: boolean) => void;
  onClose: () => void;
}) {
  const tabs: { id: PanelTab; label: string }[] = [
    { id: 'overview', label: 'Overview' },
    { id: 'files', label: 'Files' },
    ...(access.canUse ? [{ id: 'terminal' as const, label: 'Terminal' }] : []),
  ];
  return (
    <>
      <div
        aria-hidden
        onClick={onClose}
        className="fixed inset-0 z-30 bg-black/50 xl:hidden"
      />
      <aside
        aria-label="Agent panel"
        className={cn(
          'fixed inset-y-0 right-0 z-30 flex w-[min(440px,100vw)] p-2 xl:static xl:z-auto xl:shrink-0 xl:pl-0',
          wide && 'w-[min(720px,100vw)] xl:w-[min(720px,50vw)]',
        )}
      >
        <div className="flex min-h-0 w-full flex-col overflow-hidden rounded-2xl border bg-card shadow-2xl shadow-black/30 xl:shadow-none">
          <div className="flex shrink-0 items-center gap-1 border-b px-2 py-2">
            <div role="tablist" className="flex flex-1 items-center gap-0.5">
              {tabs.map((t) => (
                <button
                  key={t.id}
                  role="tab"
                  type="button"
                  aria-selected={tab === t.id}
                  onClick={() => onTab(t.id)}
                  className={cn(
                    'rounded-lg px-2.5 py-1 text-sm',
                    tab === t.id
                      ? 'bg-accent font-medium text-foreground'
                      : 'text-muted-foreground hover:text-foreground',
                  )}
                >
                  {t.label}
                </button>
              ))}
            </div>
            <Button
              variant="ghost"
              size="icon-sm"
              className="hidden text-muted-foreground sm:inline-flex"
              aria-label={wide ? 'Narrow panel' : 'Widen panel'}
              title={wide ? 'Narrow panel' : 'Widen panel'}
              onClick={() => onWide(!wide)}
            >
              {wide ? (
                <Minimize2 className="size-4" />
              ) : (
                <Maximize2 className="size-4" />
              )}
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              className="text-muted-foreground"
              aria-label="Close panel"
              onClick={onClose}
            >
              <X className="size-4" />
            </Button>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-4">
            {tab === 'overview' && (
              <Overview agent={agent} access={access} sessionId={sessionId} />
            )}
            {tab === 'files' && <PanelFiles agent={agent} access={access} />}
            {tab === 'terminal' && access.canUse && (
              <>
                {agent.state === 'sleeping' && (
                  <p className="mb-2 text-sm text-muted-foreground">
                    Opening the terminal wakes the agent
                  </p>
                )}
                <Suspense fallback={<Loading />}>
                  <TerminalTab agentId={agent.id} />
                </Suspense>
              </>
            )}
          </div>
        </div>
      </aside>
    </>
  );
}

function PanelFiles({ agent, access }: { agent: Agent; access: Access }) {
  const { action, busy } = useAgentActions(agent);
  return (
    <>
      <FilesTab
        agentId={agent.id}
        canUse={access.canUse}
        starting={busy}
        onStart={() => action.mutate('start')}
      />
      <ErrorText error={action.error} />
    </>
  );
}

function Block({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="grid gap-2 border-t pt-4 first:border-t-0 first:pt-0">
      <h3 className="text-xs font-medium text-muted-foreground">{title}</h3>
      {children}
    </section>
  );
}

function Overview({
  agent: a,
  access,
  sessionId,
}: {
  agent: Agent;
  access: Access;
  sessionId?: string;
}) {
  const harness = useHarnessName(a.harness);
  const sessions = useQuery(sessionsQuery(a.id));
  const now = useNow(30_000);
  return (
    <div className="grid gap-4">
      <Block title="Status">
        <AgentControls agent={a} canUse={access.canUse} compact />
      </Block>

      <Block title="Harness">
        <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 text-sm">
          <dt className="text-muted-foreground">Harness</dt>
          <dd>{harness}</dd>
          <dt className="text-muted-foreground">Model</dt>
          <dd>{a.model || 'default'}</dd>
          <dt className="text-muted-foreground">Permissions</dt>
          <dd>{a.permissionPolicy}</dd>
        </dl>
        {a.credentialsInSandbox && (
          <p className="flex items-center gap-1.5 text-xs text-amber-600 dark:text-amber-400">
            <TriangleAlert className="size-3" aria-hidden />
            credentials inside sandbox
          </p>
        )}
      </Block>

      <Block title="Chats">
        {sessions.isPending && <Loading />}
        <ErrorText error={sessions.error} />
        <ul aria-label="Sessions" className="-mx-2 grid grid-cols-1 gap-px">
          {access.canUse && (
            <li>
              <Link
                to="/a/$agentId"
                params={{ agentId: a.id }}
                search={(p) => ({ panel: p.panel })}
                className="flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-muted-foreground hover:bg-accent/60 hover:text-foreground"
              >
                <Plus className="size-3.5" aria-hidden />
                New chat
              </Link>
            </li>
          )}
          {sessions.data?.length === 0 && (
            <li className="px-2 py-1 text-sm text-muted-foreground">
              No sessions yet.
            </li>
          )}
          {sessions.data?.map((s) => (
            <li key={s.id}>
              <Link
                to="/a/$agentId"
                params={{ agentId: a.id }}
                search={(p) => ({ s: s.id, panel: p.panel })}
                aria-current={s.id === sessionId ? 'page' : undefined}
                className={cn(
                  'flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm hover:bg-accent/60',
                  s.id === sessionId && 'bg-accent',
                )}
              >
                <span className="min-w-0 flex-1 truncate">
                  {s.title || 'New session'}
                </span>
                {s.running ? (
                  <Loader2
                    role="img"
                    aria-label="running"
                    className="size-3.5 shrink-0 animate-spin text-sky-400"
                  />
                ) : (
                  <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                    {relTime(s.updatedAt, now)}
                  </span>
                )}
              </Link>
            </li>
          ))}
        </ul>
      </Block>

      <Block title="Sandbox">
        <AgentDetails agent={a} />
      </Block>

      <Block title="Manage">
        <ul className="-mx-2 grid grid-cols-1 gap-px">
          {AGENT_SECTIONS.filter((s) => s.show(access)).map((s) => (
            <li key={s.id}>
              <Link
                to="/a/$agentId/settings"
                params={{ agentId: a.id }}
                search={{ tab: s.id }}
                className="group flex items-center gap-3 rounded-lg px-2 py-1.5 text-sm hover:bg-accent/60"
              >
                <s.icon
                  className="size-4 shrink-0 text-muted-foreground"
                  aria-hidden
                />
                <span className="min-w-0 flex-1">
                  {s.label}
                  <span className="block truncate text-xs text-muted-foreground">
                    {s.hint}
                  </span>
                </span>
                <ChevronRight
                  className="size-3.5 text-muted-foreground opacity-0 group-hover:opacity-100"
                  aria-hidden
                />
              </Link>
            </li>
          ))}
        </ul>
      </Block>
    </div>
  );
}
