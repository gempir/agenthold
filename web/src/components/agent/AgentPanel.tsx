import { lazy, Suspense, useEffect, type ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import {
  ArrowUpLeft,
  ChevronRight,
  FileText,
  FolderGit2,
  GitBranch,
  Loader2,
  Maximize2,
  MessagesSquare,
  Minimize2,
  Plus,
  TriangleAlert,
  X,
} from 'lucide-react';
import { sessionsQuery, type Agent, type Session } from '@/api/queries';
import { FilesTab } from '@/components/FilesTab';
import { GitTab } from '@/components/GitTab';
import { LiveLog } from '@/components/chat/LiveLog';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { AgentControls, AgentDetails } from '@/components/agent/AgentControls';
import { useHarnessName } from '@/components/agent/HarnessMeta';
import { AGENT_SECTIONS, type Access } from '@/components/agent/sections';
import {
  isBuiltinTab,
  openPanelTab,
  parseTab,
  usePanelTabs,
  type BuiltinTab,
  type DynamicTab,
  type PanelTab,
} from '@/components/agent/panelTabs';
import { DiffPane } from '@/components/workspace/DiffPane';
import { FileViewer } from '@/components/workspace/FileViewer';
import { composerKey } from '@/lib/composerContext';
import { baseName, resolveFileRef } from '@/lib/files';
import { useAgentActions } from '@/hooks/useAgentActions';
import { relTime, useNow } from '@/lib/time';
import { cn } from '@/lib/utils';

const TerminalTab = lazy(() =>
  import('@/components/TerminalTab').then((m) => ({ default: m.TerminalTab })),
);
const ChatView = lazy(() => import('@/components/chat/ChatView'));

export {
  isPanelTab,
  panelTabNeedsUse,
  type PanelTab,
} from '@/components/agent/panelTabs';

/** What the panel's tabs can open: files, other threads, the main view. */
export type PanelActions = {
  /** Opens a file (relative to the home) in a tab, at a line. */
  onOpenFile: (path: string, line?: number) => void;
  /** Opens a thread in a tab beside the open one. */
  onOpenThread: (sessionId: string) => void;
  /** Shows a thread in the main view instead of the panel. */
  onOpenInMain: (sessionId: string) => void;
  /** Closes a file or thread tab. */
  onCloseTab: (tab: DynamicTab) => void;
};

/**
 * The floating right-hand panel next to a thread: a tabbed workspace with the
 * changes, files, terminal and live activity of the agent, the agent's status
 * and other conversations, and tabs for open files and threads beside this
 * one.
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
  actions,
}: {
  agent: Agent;
  access: Access;
  tab: PanelTab;
  sessionId?: string;
  wide: boolean;
  onTab: (t: PanelTab) => void;
  onWide: (w: boolean) => void;
  onClose: () => void;
  actions: PanelActions;
}) {
  const tabs: { id: BuiltinTab; label: string }[] = [
    { id: 'overview', label: 'Overview' },
    { id: 'diff', label: 'Diff' },
    { id: 'files', label: 'Files' },
    { id: 'git', label: 'Git' },
    ...(access.canUse
      ? [
          { id: 'terminal' as const, label: 'Terminal' },
          { id: 'activity' as const, label: 'Activity' },
        ]
      : []),
  ];
  const open = usePanelTabs(agent.id, tab);
  // A link straight to a file or thread tab keeps it open like any other.
  useEffect(() => {
    if (!isBuiltinTab(tab)) openPanelTab(agent.id, tab);
  }, [agent.id, tab]);
  const sessions = useQuery(sessionsQuery(agent.id));
  const session = sessions.data?.find((s) => s.id === sessionId);
  const active = parseTab(tab);
  const fills =
    tab === 'activity' || tab === 'diff' || active.kind !== 'builtin';
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
            <div
              role="tablist"
              aria-label="Workspace"
              className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto"
            >
              {tabs.map((t) => (
                <button
                  key={t.id}
                  role="tab"
                  type="button"
                  aria-selected={tab === t.id}
                  onClick={() => onTab(t.id)}
                  className={cn(
                    'shrink-0 rounded-lg px-2.5 py-1 text-sm',
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
          {open.length > 0 && (
            <div
              role="tablist"
              aria-label="Open tabs"
              className="flex shrink-0 items-center gap-1 overflow-x-auto border-b px-2 py-1.5"
            >
              {open.map((t) => (
                <OpenTab
                  key={t}
                  tab={t}
                  active={t === tab}
                  title={tabTitle(t, sessions.data)}
                  onSelect={() => onTab(t)}
                  onClose={() => actions.onCloseTab(t)}
                />
              ))}
            </div>
          )}
          <div
            className={cn(
              'min-h-0 flex-1',
              // These scroll on their own and take the panel's height.
              fills ? 'flex flex-col overflow-hidden' : 'overflow-y-auto p-4',
              tab === 'activity' && 'p-4',
            )}
          >
            {tab === 'overview' && (
              <Overview agent={agent} access={access} sessionId={sessionId} />
            )}
            {tab === 'diff' && (
              <PanelDiff
                agent={agent}
                access={access}
                repoPath={session?.git?.repoPath ?? session?.cwd}
                composer={composerKey(agent.id, sessionId)}
                onOpenFile={actions.onOpenFile}
              />
            )}
            {tab === 'git' && <PanelGit agent={agent} access={access} />}
            {tab === 'files' && (
              <PanelFiles
                agent={agent}
                access={access}
                onOpenFile={(p) => actions.onOpenFile(p)}
              />
            )}
            {active.kind === 'file' && (
              <FileViewer
                key={active.path}
                agentId={agent.id}
                path={active.path}
                line={active.line}
                canUse={access.canUse}
                composerKey={
                  access.canUse ? composerKey(agent.id, sessionId) : undefined
                }
                onOpenFile={actions.onOpenFile}
              />
            )}
            {active.kind === 'thread' && (
              <ThreadTab
                key={active.sessionId}
                agent={agent}
                access={access}
                sessionId={active.sessionId}
                sessions={sessions.data}
                actions={actions}
              />
            )}
            {tab === 'activity' && access.canUse && (
              <LiveLog key={agent.id} agentId={agent.id} live fill />
            )}
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

function tabTitle(t: DynamicTab, sessions: Session[] | undefined): string {
  const p = parseTab(t);
  if (p.kind === 'file')
    return `${baseName(p.path)}${p.line ? `:${p.line}` : ''}`;
  if (p.kind === 'thread')
    return sessions?.find((s) => s.id === p.sessionId)?.title || 'Thread';
  return t;
}

function OpenTab({
  tab,
  active,
  title,
  onSelect,
  onClose,
}: {
  tab: DynamicTab;
  active: boolean;
  title: string;
  onSelect: () => void;
  onClose: () => void;
}) {
  const p = parseTab(tab);
  const Icon = p.kind === 'thread' ? MessagesSquare : FileText;
  return (
    <span
      className={cn(
        'group flex max-w-52 shrink-0 items-center rounded-md border text-xs',
        active
          ? 'border-border bg-accent text-foreground'
          : 'border-transparent text-muted-foreground hover:text-foreground',
      )}
    >
      <button
        type="button"
        role="tab"
        aria-selected={active}
        title={p.kind === 'file' ? p.path : title}
        onClick={onSelect}
        onAuxClick={(e) => {
          if (e.button === 1) {
            e.preventDefault();
            onClose();
          }
        }}
        className="flex min-w-0 items-center gap-1.5 py-1 pr-1 pl-2"
      >
        <Icon className="size-3.5 shrink-0" aria-hidden />
        <span className="truncate">{title}</span>
      </button>
      <button
        type="button"
        aria-label={`Close ${title}`}
        title="Close tab"
        onClick={onClose}
        className="mr-0.5 rounded p-0.5 opacity-60 hover:bg-background hover:opacity-100"
      >
        <X className="size-3" aria-hidden />
      </button>
    </span>
  );
}

/** Another thread beside the open one: watch it and talk to it. */
function ThreadTab({
  agent,
  access,
  sessionId,
  sessions,
  actions,
}: {
  agent: Agent;
  access: Access;
  sessionId: string;
  sessions?: Session[];
  actions: PanelActions;
}) {
  const s = sessions?.find((x) => x.id === sessionId);
  const children = (sessions ?? []).filter(
    (x) => x.origin?.agentId === agent.id && x.origin.sessionId === sessionId,
  );
  return (
    <div data-testid="panel-thread" className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b px-3 py-1.5">
        {s?.running ? (
          <Loader2
            role="img"
            aria-label="running"
            className="size-3.5 shrink-0 animate-spin text-sky-400"
          />
        ) : (
          <MessagesSquare
            className="size-3.5 shrink-0 text-muted-foreground"
            aria-hidden
          />
        )}
        <span className="min-w-0 flex-1 truncate text-sm font-medium">
          {s?.title || 'Thread'}
        </span>
        <Button
          variant="outline"
          size="sm"
          className="h-7 px-2 text-xs"
          onClick={() => actions.onOpenInMain(sessionId)}
        >
          <ArrowUpLeft className="size-3.5" aria-hidden />
          Open in main view
        </Button>
      </div>
      <Suspense fallback={<Loading />}>
        <ChatView
          sessionId={sessionId}
          agentId={agent.id}
          subthreads={children}
          canUse={access.canUse}
          agentState={agent.state}
          initialRunning={s?.running}
          compact
          onOpenFile={(p, line) =>
            actions.onOpenFile(resolveFileRef(p, s?.cwd), line)
          }
          onOpenSubthread={actions.onOpenThread}
          onOpenSubthreadInMain={actions.onOpenInMain}
        />
      </Suspense>
    </div>
  );
}

function PanelDiff({
  agent,
  access,
  repoPath,
  composer,
  onOpenFile,
}: {
  agent: Agent;
  access: Access;
  repoPath?: string;
  composer: string;
  onOpenFile: (path: string, line?: number) => void;
}) {
  const { action, busy } = useAgentActions(agent);
  return (
    <>
      <DiffPane
        agentId={agent.id}
        canUse={access.canUse}
        repoPath={repoPath}
        composerKey={access.canUse ? composer : undefined}
        onOpenFile={onOpenFile}
        starting={busy}
        onStart={() => action.mutate('start')}
      />
      <ErrorText error={action.error} />
    </>
  );
}

function PanelGit({ agent, access }: { agent: Agent; access: Access }) {
  const { action, busy } = useAgentActions(agent);
  return (
    <>
      <GitTab
        agentId={agent.id}
        canUse={access.canUse}
        starting={busy}
        onStart={() => action.mutate('start')}
      />
      <ErrorText error={action.error} />
    </>
  );
}

function PanelFiles({
  agent,
  access,
  onOpenFile,
}: {
  agent: Agent;
  access: Access;
  onOpenFile: (path: string) => void;
}) {
  const { action, busy } = useAgentActions(agent);
  return (
    <>
      <FilesTab
        agentId={agent.id}
        canUse={access.canUse}
        starting={busy}
        onStart={() => action.mutate('start')}
        onOpenFile={onOpenFile}
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
        </dl>
        {a.credentialsInSandbox && (
          <p className="flex items-center gap-1.5 text-xs text-amber-600 dark:text-amber-400">
            <TriangleAlert className="size-3" aria-hidden />
            credentials inside sandbox
          </p>
        )}
      </Block>

      <Block title="Threads">
        {sessions.isPending && <Loading />}
        <ErrorText error={sessions.error} />
        <ul aria-label="Threads" className="-mx-2 grid grid-cols-1 gap-px">
          {access.canUse && (
            <li>
              <Link
                to="/a/$agentId"
                params={{ agentId: a.id }}
                search={(p) => ({ panel: p.panel })}
                className="flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-muted-foreground hover:bg-accent/60 hover:text-foreground"
              >
                <Plus className="size-3.5" aria-hidden />
                New thread
              </Link>
            </li>
          )}
          {sessions.data?.length === 0 && (
            <li className="px-2 py-1 text-sm text-muted-foreground">
              No threads yet.
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
                <span className="grid min-w-0 flex-1">
                  <span className="truncate">{s.title || 'New thread'}</span>
                  {(s.git?.project || s.git?.branch) && (
                    <span
                      title={[s.git.project, s.git.branch]
                        .filter(Boolean)
                        .join(' · ')}
                      className="flex min-w-0 items-center gap-2 truncate text-[11px] text-muted-foreground"
                    >
                      {s.git.project && (
                        <span className="flex min-w-0 items-center gap-1">
                          <FolderGit2 className="size-3 shrink-0" aria-hidden />
                          <span className="truncate">{s.git.project}</span>
                        </span>
                      )}
                      {s.git.branch && (
                        <span className="flex min-w-0 items-center gap-1 font-mono">
                          <GitBranch className="size-3 shrink-0" aria-hidden />
                          <span className="truncate">{s.git.branch}</span>
                        </span>
                      )}
                    </span>
                  )}
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
