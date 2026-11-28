import { useCallback, useEffect, useId, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Link,
  useNavigate,
  useParams,
  useRouterState,
  useSearch,
} from '@tanstack/react-router';
import {
  AppWindow,
  Bot,
  Check,
  ChevronLeft,
  ChevronRight,
  FolderGit2,
  GitPullRequest,
  Workflow,
  ChartNoAxesColumn,
  GitBranch,
  Layers,
  Loader2,
  LogOut,
  Moon,
  PenLine,
  Plus,
  Search,
  Settings,
  SquarePen,
  Sun,
  Undo2,
  UserRound,
  Users,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { appsQuery, meQuery, type SessionGit } from '@/api/queries';
import { ROLE_LABEL } from '@/lib/roles';
import type { PanelTab } from '@/components/agent/AgentPanel';
import { AgentStateDot } from '@/components/AgentStateBadge';
import { AgentBadge } from '@/components/AgentBadge';
import { Avatar } from '@/components/Avatar';
import { HarnessIcon } from '@/components/HarnessIcon';
import { ErrorText } from '@/components/Spinner';
import { Menu, MenuItem, MenuSeparator } from '@/components/ui/menu';
import { readPref, useShell, writePref } from '@/components/layout/shell';
import { useChatIndex, type ChatEntry } from '@/hooks/useChatIndex';
import { useJustFinished } from '@/hooks/useJustFinished';
import { useLogout } from '@/hooks/useLogout';
import { UndoToast, type UndoAction } from '@/components/layout/UndoToast';
import { useSettle } from '@/hooks/useSettle';
import { getTheme, setTheme, type Theme } from '@/lib/theme';
import { useHasDraft } from '@/lib/drafts';
import { relTime, useNow } from '@/lib/time';
import { cn } from '@/lib/utils';

const SETTLED_PREVIEW = 10;
const SETTLED_PAGE = 50;
/** localStorage key (agenthold.<key>) of the Settled section's fold. */
export const SETTLED_OPEN_PREF = 'sidebar.settledOpen';

type SidebarView = 'threads' | 'agents' | 'apps';

/** Who may settle: whoever may send prompts to the agent. */
const canUse = (c: ChatEntry) =>
  c.agent.role === 'owner' || c.agent.role === 'shared';

export function Sidebar() {
  const me = useQuery(meQuery);
  const apps = useQuery(appsQuery);
  const index = useChatIndex();
  const params = useParams({ strict: false }) as {
    agentId?: string;
    appId?: string;
  };
  const search = useSearch({ strict: false }) as { s?: string };
  const [view, setView] = useState<SidebarView>(() =>
    params.appId ? 'apps' : 'threads',
  );
  const [query, setQuery] = useState('');
  const [settledLimit, setSettledLimit] = useState(SETTLED_PREVIEW);
  const [settledOpen, setSettledOpenState] = useState(
    () => readPref(SETTLED_OPEN_PREF, '') === '1',
  );
  const settle = useSettle();
  const qc = useQueryClient();
  const [undo, setUndo] = useState<UndoAction | null>(null);
  const closeUndo = useCallback(() => setUndo(null), []);
  const [grouped, setGrouped] = useState(
    () => readPref('sidebar.groupProjects', '') === '1',
  );
  const [working, setWorking] = useState(
    () => readPref('sidebar.working', '1') === '1',
  );
  useEffect(() => {
    const toggle = () =>
      setGrouped((v) => {
        writePref('sidebar.groupProjects', v ? '' : '1');
        return !v;
      });
    window.addEventListener('agenthold:group-projects', toggle);
    return () => window.removeEventListener('agenthold:group-projects', toggle);
  }, []);
  const change = async (
    c: ChatEntry,
    action: 'pin' | 'unpin' | 'snooze' | 'unsnooze',
    until?: string,
    record = true,
  ) => {
    try {
      const params = { path: { sessionId: c.session.id } };
      const result =
        action === 'snooze'
          ? await unwrap(
              api.POST('/sessions/{sessionId}/snooze', {
                params,
                body: { until },
              }),
            )
          : await unwrap(
              api.POST(
                action === 'pin'
                  ? '/sessions/{sessionId}/pin'
                  : action === 'unpin'
                    ? '/sessions/{sessionId}/unpin'
                    : '/sessions/{sessionId}/unsnooze',
                { params },
              ),
            );
      qc.setQueryData<ChatEntry['session'][]>(
        ['agents', c.agent.id, 'sessions'],
        (list) => list?.map((s) => (s.id === result.id ? result : s)),
      );
      await qc.invalidateQueries({
        queryKey: ['agents', c.agent.id, 'sessions'],
      });
      if (record)
        setUndo({
          label: 'Thread updated',
          undo: () => {
            void change(
              c,
              action === 'pin'
                ? 'unpin'
                : action === 'unpin'
                  ? 'pin'
                  : c.session.snoozed
                    ? 'snooze'
                    : 'unsnooze',
              c.session.snoozedUntil,
              false,
            );
          },
        });
    } catch (error) {
      setActionError(error);
    }
  };
  const [actionError, setActionError] = useState<unknown>(null);
  const settleThread = (c: ChatEntry, value: boolean) =>
    settle.mutate(
      { session: c.session, settle: value },
      {
        onSuccess: () =>
          setUndo({
            label: value ? 'Thread settled' : 'Thread unsettled',
            undo: () => settle.mutate({ session: c.session, settle: !value }),
          }),
      },
    );

  const settledListId = useId();
  const now = useNow(30_000);
  const done = useJustFinished(
    index.active.map((c) => c.session),
    search.s,
  );

  const setSettledOpen = (o: boolean) => {
    setSettledOpenState(o);
    writePref(SETTLED_OPEN_PREF, o ? '1' : '');
  };

  const q = query.trim().toLowerCase();
  const matches = (...xs: string[]) =>
    !q || xs.some((x) => x.toLowerCase().includes(q));

  const agents = index.agents.filter(({ agent }) =>
    matches(agent.name, agent.owner.name),
  );
  const threadMatches = ({ session, agent }: ChatEntry) =>
    matches(
      session.title,
      agent.name,
      session.git?.project ?? '',
      session.git?.branch ?? '',
      session.project?.name ?? '',
    );
  const allActive = index.active.filter(threadMatches);
  const pinned = allActive.filter(
    (c) => c.session.pinnedAt && !c.session.snoozed,
  );
  const snoozed = allActive.filter((c) => c.session.snoozed);
  const running = allActive.filter(
    (c) =>
      !c.session.pinnedAt && !c.session.snoozed && c.session.running && working,
  );
  const active = allActive.filter(
    (c) =>
      !c.session.pinnedAt &&
      !c.session.snoozed &&
      !(working && c.session.running),
  );
  const settled = index.settled.filter(threadMatches);
  // Group same-agent sub-threads underneath their active parent thread so
  // nested work is visible at a glance without cluttering the top-level list.
  const activeIds = new Set(active.map((c) => c.session.id));
  const childrenByParent = new Map<string, ChatEntry[]>();
  const topActive: ChatEntry[] = [];
  for (const c of active) {
    const parentId =
      c.session.origin?.agentId === c.agent.id
        ? c.session.origin.sessionId
        : '';
    if (parentId && activeIds.has(parentId)) {
      const list = childrenByParent.get(parentId) ?? [];
      list.push(c);
      childrenByParent.set(parentId, list);
    } else {
      topActive.push(c);
    }
  }
  for (const list of childrenByParent.values()) {
    list.sort((a, b) => a.session.createdAt.localeCompare(b.session.createdAt));
  }
  // A search looks into settled threads too.
  const showSettled = settledOpen || (!!q && settled.length > 0);

  const latestSession = (agentId: string) =>
    (
      index.active.find((c) => c.agent.id === agentId) ??
      index.chats.find((c) => c.agent.id === agentId)
    )?.session.id;

  return (
    <nav
      aria-label="Sidebar"
      className="flex h-full w-full flex-col bg-sidebar text-sm"
    >
      <div className="flex h-13 shrink-0 items-center gap-2 px-3">
        <Link
          to="/"
          aria-label="agenthold"
          title="agenthold"
          onClick={() => setView('threads')}
          className="shrink-0 px-1"
        >
          <span
            aria-hidden
            className="flex size-7 items-center justify-center rounded-md bg-zinc-900 text-zinc-50 ring-1 ring-white/15"
          >
            <Layers className="size-4" />
          </span>
        </Link>
        <div className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-lg px-2 text-muted-foreground focus-within:bg-accent/60 hover:bg-accent/40">
          <button
            type="button"
            aria-label="Open command palette"
            onClick={() => window.dispatchEvent(new Event('agenthold:search'))}
          >
            <Search className="size-4 shrink-0" aria-hidden />
          </button>
          <input
            type="search"
            aria-label="Search threads, agents and apps"
            placeholder="Search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="min-w-0 flex-1 bg-transparent text-foreground placeholder:text-muted-foreground focus:outline-none"
          />
        </div>
        <NewThreadLink />
      </div>

      {view === 'threads' && (
        <>
          <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-3">
            <ErrorText error={index.error ?? settle.error} />

            <ErrorText error={actionError} />
            <div className="flex flex-wrap gap-2 px-2 py-2 text-xs text-muted-foreground">
              <label>
                <input
                  type="checkbox"
                  checked={grouped}
                  onChange={() =>
                    window.dispatchEvent(new Event('agenthold:group-projects'))
                  }
                />{' '}
                Group by project
              </label>
              <label>
                <input
                  type="checkbox"
                  checked={working}
                  onChange={() => {
                    setWorking(!working);
                    writePref('sidebar.working', working ? '' : '1');
                  }}
                />{' '}
                Working section
              </label>
            </div>
            {[
              { label: 'Pinned', list: pinned, folded: false },
              { label: 'Active', list: topActive, folded: false },
              { label: 'Working', list: running, folded: true },
              { label: 'Snoozed', list: snoozed, folded: true },
            ]
              .filter((section) => section.label !== 'Working' || working)
              .map((section) => {
                const render = (list: ChatEntry[]) => (
                  <ul
                    aria-label={
                      section.label === 'Active'
                        ? 'Threads'
                        : section.label + ' threads'
                    }
                    className="grid gap-1"
                  >
                    {list.length === 0 && (
                      <li className="px-2 text-muted-foreground">
                        No active threads.
                      </li>
                    )}
                    {list.map((c) => (
                      <li key={c.session.id} className="group/thread">
                        <ThreadCard
                          chat={c}
                          subthreads={childrenByParent.get(c.session.id)}
                          activeSessionId={search.s}
                          now={now}
                          active={search.s === c.session.id}
                          done={done.has(c.session.id)}
                          doneIds={done}
                          me={me.data?.user.id}
                          onSettle={
                            canUse(c) ? () => settleThread(c, true) : undefined
                          }
                          settling={settle.isPending}
                        />
                        {canUse(c) && (
                          <div className="flex justify-end gap-2 px-2 text-xs text-muted-foreground opacity-0 group-hover/thread:opacity-100 group-focus-within/thread:opacity-100 [@media(hover:none)]:opacity-100">
                            <button
                              title={c.session.pinnedAt ? 'Unpin' : 'Pin'}
                              onClick={() =>
                                void change(
                                  c,
                                  c.session.pinnedAt ? 'unpin' : 'pin',
                                )
                              }
                            >
                              {c.session.pinnedAt ? 'Unpin' : 'Pin'}
                            </button>
                            {c.session.snoozed ? (
                              <button
                                onClick={() => void change(c, 'unsnooze')}
                              >
                                Wake
                              </button>
                            ) : (
                              <Menu
                                align="end"
                                trigger={({ toggle }) => (
                                  <button
                                    aria-label={`Actions for ${c.session.title || 'New thread'}`}
                                    onClick={toggle}
                                  >
                                    Actions
                                  </button>
                                )}
                              >
                                {(close) => (
                                  <>
                                    <MenuItem
                                      onClick={() => {
                                        void change(
                                          c,
                                          c.session.pinnedAt ? 'unpin' : 'pin',
                                        );
                                        close();
                                      }}
                                    >
                                      {c.session.pinnedAt ? 'Unpin' : 'Pin'}
                                    </MenuItem>
                                    <MenuItem
                                      onClick={() => {
                                        settleThread(c, true);
                                        close();
                                      }}
                                    >
                                      Settle
                                    </MenuItem>
                                    <MenuSeparator />
                                    <span className="px-2 text-xs text-muted-foreground">
                                      Snooze
                                    </span>
                                    {[
                                      '1 hour',
                                      '3 hours',
                                      'Tomorrow 9:00 AM',
                                      'Indefinitely',
                                    ].map((label, i) => (
                                      <MenuItem
                                        key={label}
                                        onClick={() => {
                                          const tomorrow = new Date();
                                          tomorrow.setDate(
                                            tomorrow.getDate() + 1,
                                          );
                                          tomorrow.setHours(9, 0, 0, 0);
                                          void change(
                                            c,
                                            'snooze',
                                            i === 3
                                              ? undefined
                                              : i === 2
                                                ? tomorrow.toISOString()
                                                : new Date(
                                                    Date.now() +
                                                      (i === 0 ? 1 : 3) *
                                                        3600000,
                                                  ).toISOString(),
                                          );
                                          close();
                                        }}
                                      >
                                        {label}
                                      </MenuItem>
                                    ))}
                                  </>
                                )}
                              </Menu>
                            )}
                          </div>
                        )}
                      </li>
                    ))}
                  </ul>
                );
                const groups = new Map<string, ChatEntry[]>();
                for (const c of section.list) {
                  const name = c.session.project?.name ?? 'No project';
                  groups.set(name, [...(groups.get(name) ?? []), c]);
                }
                const content =
                  grouped && section.label === 'Active'
                    ? [...groups].map(([name, list]) => (
                        <details key={name} open>
                          <summary className="px-2 py-1 text-xs">
                            {name}
                          </summary>
                          {render(list)}
                        </details>
                      ))
                    : render(section.list);
                return section.folded ? (
                  <details key={section.label}>
                    <summary className="flex cursor-pointer gap-2 px-2 py-2 text-xs text-muted-foreground">
                      {section.label === 'Working' && (
                        <Loader2 className="size-3 animate-spin" />
                      )}
                      {section.label} ({section.list.length})
                    </summary>
                    {content}
                  </details>
                ) : (
                  <section key={section.label}>
                    <h2 className="px-2 py-2 text-xs text-muted-foreground">
                      {section.label}
                    </h2>
                    {content}
                  </section>
                );
              })}
          </div>

          {index.settled.length > 0 && (
            <div className="shrink-0 border-t px-3 py-1">
              <button
                type="button"
                aria-expanded={showSettled}
                aria-controls={settledListId}
                onClick={() => setSettledOpen(!showSettled)}
                className="flex h-8 w-full items-center gap-2 rounded-md px-2 text-xs font-medium text-muted-foreground hover:text-foreground"
              >
                <span className="flex-1 text-left">Settled</span>
                <span className="tabular-nums">
                  {q ? settled.length : index.settled.length}
                </span>
                <ChevronRight
                  aria-hidden
                  className={cn(
                    'size-3.5 transition-transform',
                    showSettled && 'rotate-90',
                  )}
                />
              </button>
              {showSettled && (
                <ul
                  id={settledListId}
                  aria-label="Settled threads"
                  className="grid max-h-[40vh] grid-cols-1 gap-px overflow-y-auto pb-1"
                >
                  {settled.length === 0 && (
                    <li className="px-2 py-1.5 text-muted-foreground">
                      No matching settled threads.
                    </li>
                  )}
                  {settled.slice(0, settledLimit).map((c) => (
                    <SettledRow
                      key={c.session.id}
                      chat={c}
                      now={now}
                      active={search.s === c.session.id}
                      onUnsettle={
                        canUse(c) ? () => settleThread(c, false) : undefined
                      }
                    />
                  ))}
                  {settled.length > settledLimit && (
                    <li>
                      <button
                        type="button"
                        onClick={() => setSettledLimit((l) => l + SETTLED_PAGE)}
                        className="px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
                      >
                        Show{' '}
                        {Math.min(settled.length - settledLimit, SETTLED_PAGE)}{' '}
                        more
                      </button>
                    </li>
                  )}
                </ul>
              )}
            </div>
          )}
        </>
      )}

      {view === 'agents' && (
        <AgentsSidebarView
          agents={agents}
          loading={index.loading}
          error={index.error}
          query={q}
          currentAgentId={params.agentId}
          latestSession={latestSession}
          onBack={() => setView('threads')}
        />
      )}

      {view === 'apps' && (
        <AppsSidebarView query={q} onBack={() => setView('threads')} />
      )}

      <UndoToast action={undo} onClose={closeUndo} />
      <Footer
        view={view}
        onView={setView}
        agentsCount={index.agents.length}
        appsCount={apps.data?.length ?? 0}
      />
    </nav>
  );
}

function AgentsSidebarView({
  agents,
  loading,
  error,
  query: q,
  currentAgentId,
  latestSession,
  onBack,
}: {
  agents: ReturnType<typeof useChatIndex>['agents'];
  loading: boolean;
  error: unknown;
  query: string;
  currentAgentId?: string;
  latestSession: (agentId: string) => string | undefined;
  onBack: () => void;
}) {
  const shell = useShell();
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-9 shrink-0 items-center gap-1 px-3">
        <button
          type="button"
          onClick={onBack}
          aria-label="Back to threads"
          className="flex items-center gap-1 rounded-md px-1.5 py-1 text-xs font-medium text-muted-foreground hover:bg-accent/50 hover:text-foreground"
        >
          <ChevronLeft className="size-3.5" aria-hidden />
          <span>Agents</span>
        </button>
        <button
          type="button"
          onClick={shell.openNewAgent}
          aria-label="New agent"
          title="New agent"
          className="ml-auto flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          <Plus className="size-3.5" />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-3">
        <ErrorText error={error} />
        <ul aria-label="Agents" className="grid grid-cols-1 gap-1">
          {loading && agents.length === 0 && <SkeletonRows n={3} />}
          {!loading && agents.length === 0 && (
            <li className="px-2 py-1.5 text-muted-foreground">
              {q ? 'No matching agents.' : 'No agents yet.'}
            </li>
          )}
          {agents.map(({ agent }) => {
            const current = currentAgentId === agent.id;
            return (
              <li key={agent.id} className="group relative">
                <Link
                  to="/a/$agentId"
                  params={{ agentId: agent.id }}
                  search={{ s: latestSession(agent.id) }}
                  aria-current={current ? 'page' : undefined}
                  className={cn(
                    'flex h-9 items-center gap-2 rounded-lg pr-9 pl-2 hover:bg-accent/50',
                    current && 'bg-accent/70',
                  )}
                >
                  <AgentStateDot state={agent.state} />
                  <span className="min-w-0 flex-1 truncate">{agent.name}</span>
                  {agent.role === 'shared' && (
                    <Users
                      className="size-3.5 shrink-0 text-muted-foreground"
                      aria-label={`shared by ${agent.owner.name}`}
                    />
                  )}
                </Link>
                <Link
                  to="/a/$agentId"
                  params={{ agentId: agent.id }}
                  aria-label={`New thread with ${agent.name}`}
                  title="New thread"
                  className="absolute top-1.5 right-1.5 flex size-6 items-center justify-center rounded-md text-muted-foreground opacity-0 group-hover:opacity-100 hover:bg-accent hover:text-foreground focus-visible:opacity-100 [@media(hover:none)]:opacity-100"
                >
                  <Plus className="size-3.5" />
                </Link>
              </li>
            );
          })}
        </ul>
      </div>
    </div>
  );
}

/** Apps: machines without a harness, reached at their URLs. */
function AppsSidebarView({
  query: q,
  onBack,
}: {
  query: string;
  onBack: () => void;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { appId?: string };
  const apps = useQuery(appsQuery);
  const create = useMutation({
    mutationFn: (name: string) => unwrap(api.POST('/apps', { body: { name } })),
    onSuccess: async (app) => {
      await qc.invalidateQueries({ queryKey: appsQuery.queryKey });
      await navigate({ to: '/apps/$appId', params: { appId: app.id } });
    },
  });
  const all = apps.data ?? [];
  const list = all.filter((a) => !q || a.name.toLowerCase().includes(q));
  const promptNewApp = () => {
    const name = globalThis.prompt?.('Name of the new app')?.trim();
    if (name) create.mutate(name);
  };
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-9 shrink-0 items-center gap-1 px-3">
        <button
          type="button"
          onClick={onBack}
          aria-label="Back to threads"
          className="flex items-center gap-1 rounded-md px-1.5 py-1 text-xs font-medium text-muted-foreground hover:bg-accent/50 hover:text-foreground"
        >
          <ChevronLeft className="size-3.5" aria-hidden />
          <span>Apps</span>
        </button>
        <button
          type="button"
          disabled={create.isPending}
          onClick={promptNewApp}
          aria-label="New app"
          title="New app"
          className="ml-auto flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-50"
        >
          <Plus className="size-3.5" />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-3">
        <ErrorText error={apps.error ?? create.error} />
        <ul aria-label="Apps" className="grid grid-cols-1 gap-1">
          {apps.isPending && <SkeletonRows n={3} />}
          {apps.isSuccess && list.length === 0 && (
            <li className="px-2 py-1.5 text-muted-foreground">
              {q
                ? 'No matching apps.'
                : 'No apps yet. Agents create them with deploy_app.'}
            </li>
          )}
          {list.map((a) => (
            <li key={a.id}>
              <Link
                to="/apps/$appId"
                params={{ appId: a.id }}
                aria-current={params.appId === a.id ? 'page' : undefined}
                title={a.url || a.name}
                className={cn(
                  'grid grid-cols-1 rounded-lg px-2 py-1.5 hover:bg-accent/50',
                  params.appId === a.id && 'bg-accent/70',
                )}
              >
                <span className="flex min-w-0 items-center gap-2">
                  <AgentStateDot state={a.state} />
                  <span className="min-w-0 flex-1 truncate">{a.name}</span>
                </span>
                {a.url && (
                  <span className="truncate pl-4 text-xs text-muted-foreground">
                    {a.url.replace(/^https?:\/\//, '').replace(/\/$/, '')}
                  </span>
                )}
              </Link>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

/** An active thread: agent, title, and how it stands; Settle on hover. */
function ThreadCard({
  chat: { session, agent },
  subthreads,
  activeSessionId,
  now,
  active,
  done,
  doneIds,
  me,
  onSettle,
  settling,
}: {
  chat: ChatEntry;
  subthreads?: ChatEntry[];
  activeSessionId?: string;
  now: number;
  active: boolean;
  done: boolean;
  doneIds: Set<string>;
  me?: string;
  onSettle?: () => void;
  settling: boolean;
}) {
  const title = session.title || 'New thread';
  const hasDraft = useHasDraft(session.id);
  const by =
    session.origin != null
      ? session.origin.agentId === agent.id
        ? 'sub-thread'
        : `from ${session.origin.agentName}`
      : me && session.createdBy.id !== me
        ? `by ${session.createdBy.name}`
        : null;
  const status = hasDraft ? (
    <span className="flex shrink-0 items-center gap-1 font-medium text-amber-600 dark:text-amber-400">
      <PenLine className="size-3" aria-hidden />
      Draft
    </span>
  ) : session.running ? (
    <Loader2
      role="img"
      aria-label="running"
      className="size-3.5 shrink-0 animate-spin text-sky-500 dark:text-sky-400"
    />
  ) : done ? (
    <span className="flex shrink-0 items-center gap-0.5 font-medium text-emerald-600 dark:text-emerald-400">
      <Check className="size-3" aria-hidden />
      Done
    </span>
  ) : (
    <span className="shrink-0 tabular-nums">
      {relTime(session.updatedAt, now)}
    </span>
  );
  return (
    <div>
      <div className="group relative">
        <Link
          to="/a/$agentId"
          params={{ agentId: agent.id }}
          search={{ s: session.id }}
          aria-current={active ? 'page' : undefined}
          className={cn(
            'grid grid-cols-1 gap-0.5 rounded-lg bg-accent/25 px-2 py-1.5 hover:bg-accent/50',
            active && 'bg-accent/70 hover:bg-accent/70',
          )}
        >
          <span className="truncate text-[13px] leading-5 font-medium text-foreground">
            {title}
          </span>
          <span className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
            <AgentBadge name={agent.name} />
            <span className="min-w-0 truncate">
              {[agent.name, by].filter(Boolean).join(' · ')}
            </span>
            {session.origin && <Bot className="size-3 shrink-0" aria-hidden />}
            {agent.role === 'shared' && (
              <Users
                className="size-3 shrink-0"
                aria-label={`shared by ${agent.owner.name}`}
              />
            )}
            <span
              className={cn(
                'ml-auto flex shrink-0 items-center',
                onSettle &&
                  'group-focus-within:invisible group-hover:invisible [@media(hover:none)]:invisible',
              )}
            >
              {status}
            </span>
          </span>
          {hasGit(gitOf(session)) && <GitLine git={gitOf(session)!} />}
        </Link>
        {onSettle && (
          <button
            type="button"
            onClick={onSettle}
            disabled={settling}
            aria-label={`Settle thread ${title}`}
            title="Settle thread"
            className="absolute right-1.5 bottom-1.5 flex h-5 items-center gap-1 rounded-md border bg-background px-1.5 text-[11px] font-medium text-muted-foreground opacity-0 shadow-xs group-focus-within:opacity-100 group-hover:opacity-100 hover:text-foreground focus-visible:opacity-100 disabled:opacity-50 [@media(hover:none)]:opacity-100"
          >
            <Check className="size-3" aria-hidden />
            Settle
          </button>
        )}
      </div>
      {subthreads && subthreads.length > 0 && (
        <ul
          aria-label={`Sub-threads of ${title}`}
          className="mt-1 ml-3 grid grid-cols-1 gap-0.5 border-l pl-2"
        >
          {subthreads.map(({ session: sub, agent: subAgent }) => {
            const subActive = activeSessionId === sub.id;
            const subDone = doneIds.has(sub.id);
            const subTitle = sub.title || 'Sub-thread';
            return (
              <li key={sub.id}>
                <Link
                  to="/a/$agentId"
                  params={{ agentId: subAgent.id }}
                  search={{ s: sub.id }}
                  aria-current={subActive ? 'page' : undefined}
                  title={subTitle}
                  className={cn(
                    'flex h-7 items-center gap-1.5 rounded-md px-2 text-xs text-muted-foreground hover:bg-accent/50 hover:text-foreground',
                    subActive && 'bg-accent/70 text-foreground',
                  )}
                >
                  <HarnessIcon
                    harness={sub.harness || subAgent.harness}
                    className="size-3 shrink-0"
                  />
                  <span className="min-w-0 flex-1 truncate">{subTitle}</span>
                  {sub.running ? (
                    <Loader2
                      role="img"
                      aria-label="running"
                      className="size-3 shrink-0 animate-spin text-sky-500 dark:text-sky-400"
                    />
                  ) : subDone ? (
                    <Check
                      className="size-3 shrink-0 text-emerald-600 dark:text-emerald-400"
                      aria-hidden
                    />
                  ) : (
                    <span className="shrink-0 text-[11px] tabular-nums">
                      {relTime(sub.updatedAt, now)}
                    </span>
                  )}
                </Link>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

/** A settled thread: compact, with Unsettle on hover. */
function SettledRow({
  chat: { session, agent },
  now,
  active,
  onUnsettle,
}: {
  chat: ChatEntry;
  now: number;
  active: boolean;
  onUnsettle?: () => void;
}) {
  const title = session.title || 'New thread';
  const hasDraft = useHasDraft(session.id);
  return (
    <li className="group relative">
      <Link
        to="/a/$agentId"
        params={{ agentId: agent.id }}
        search={{ s: session.id }}
        aria-current={active ? 'page' : undefined}
        title={[title, agent.name, session.git?.project, session.git?.branch]
          .filter(Boolean)
          .join(' · ')}
        className={cn(
          'flex h-8 items-center gap-2 rounded-lg bg-accent/25 px-2 hover:bg-accent/50',
          active && 'bg-accent/70',
        )}
      >
        <AgentBadge name={agent.name} />
        <span className="min-w-0 flex-1 truncate text-muted-foreground group-hover:text-foreground">
          {title}
        </span>
        {session.git?.branch && (
          <span
            data-testid="chat-branch"
            className="flex max-w-[40%] min-w-0 shrink items-center gap-1 font-mono text-[11px] text-muted-foreground/70"
          >
            <GitBranch className="size-3 shrink-0" aria-hidden />
            <span className="truncate">{session.git.branch}</span>
          </span>
        )}
        <span
          className={cn(
            'shrink-0 text-xs text-muted-foreground tabular-nums',
            onUnsettle &&
              'group-focus-within:invisible group-hover:invisible [@media(hover:none)]:invisible',
          )}
        >
          {hasDraft ? (
            <span className="flex items-center gap-1 font-medium text-amber-600 dark:text-amber-400">
              <PenLine className="size-3" aria-hidden />
              Draft
            </span>
          ) : (
            relTime(session.updatedAt, now)
          )}
        </span>
      </Link>
      {onUnsettle && (
        <button
          type="button"
          onClick={onUnsettle}
          aria-label={`Unsettle thread ${title}`}
          title="Unsettle thread"
          className="absolute top-1 right-1 flex size-6 items-center justify-center rounded-md text-muted-foreground opacity-0 group-focus-within:opacity-100 group-hover:opacity-100 hover:bg-accent hover:text-foreground focus-visible:opacity-100 [@media(hover:none)]:opacity-100"
        >
          <Undo2 className="size-3.5" />
        </button>
      )}
    </li>
  );
}

const hasGit = (g?: SessionGit): g is SessionGit =>
  !!g && !!(g.project || g.branch);

/** Where a thread works: its repository, or for a project thread the
 * project and the thread's branch. */
const gitOf = (s: ChatEntry['session']): SessionGit | undefined =>
  s.git ??
  (s.project && { project: s.project.name, branch: s.project.branch ?? '' });

/** The project and branch a thread is on, one compact truncating line. */
function GitLine({ git }: { git: SessionGit }) {
  return (
    <span
      data-testid="chat-git"
      title={[git.project, git.branch].filter(Boolean).join(' · ')}
      className="flex min-w-0 items-center gap-2 truncate text-xs text-muted-foreground"
    >
      {git.project && (
        <span className="flex max-w-[50%] min-w-0 items-center gap-1">
          <FolderGit2 className="size-3 shrink-0" aria-hidden />
          <span className="truncate">{git.project}</span>
        </span>
      )}
      {git.branch && (
        <span className="flex min-w-0 items-center gap-1 font-mono text-[11px]">
          <GitBranch className="size-3 shrink-0" aria-hidden />
          <span className="truncate">{git.branch}</span>
        </span>
      )}
    </span>
  );
}

function SkeletonRows({ n, tall }: { n: number; tall?: boolean }) {
  return (
    <>
      {Array.from({ length: n }, (_, i) => (
        <li
          key={i}
          aria-hidden
          className={cn(
            'mx-2 my-1 animate-pulse rounded-md bg-accent/50',
            tall ? 'h-9' : 'h-5',
          )}
        />
      ))}
    </>
  );
}

/** Bottom menu bar: profile icon, settings button, and Agents/Apps view switchers. */
function Footer({
  view,
  onView,
  agentsCount,
  appsCount,
}: {
  view: SidebarView;
  onView: (v: SidebarView) => void;
  agentsCount: number;
  appsCount: number;
}) {
  const shell = useShell();
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const me = useQuery(meQuery);
  const logout = useLogout();
  const [theme, setThemeState] = useState<Theme>(getTheme);
  const user = me.data?.user;
  const inSettings = pathname.startsWith('/settings');

  return (
    <div className="@container shrink-0 border-t px-2.5 py-2">
      <div className="flex items-center gap-1">
        <Menu
          side="top"
          align="start"
          trigger={({ open, toggle }) => (
            <button
              type="button"
              aria-haspopup="menu"
              aria-expanded={open}
              aria-label="Account menu"
              title={user?.name ?? 'Account'}
              onClick={toggle}
              className={cn(
                'flex size-8 shrink-0 items-center justify-center rounded-lg transition-colors hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
                open && 'bg-accent',
              )}
            >
              {user ? (
                <Avatar name={user.name} url={user.avatarUrl} size="sm" />
              ) : (
                <UserRound
                  className="size-4 text-muted-foreground"
                  aria-hidden
                />
              )}
            </button>
          )}
        >
          {(close) => (
            <>
              {user && (
                <>
                  <div className="flex items-center gap-2 px-2 py-1.5">
                    <Avatar name={user.name} url={user.avatarUrl} size="sm" />
                    <div className="grid min-w-0 leading-tight">
                      <span className="truncate text-sm font-medium">
                        {user.name}
                      </span>
                      {me.data?.role && (
                        <span className="text-xs text-muted-foreground">
                          {ROLE_LABEL[me.data.role]}
                        </span>
                      )}
                    </div>
                  </div>
                  <MenuSeparator />
                </>
              )}
              <MenuItem
                onClick={() => {
                  close();
                  shell.openNewAgent();
                }}
              >
                <Bot className="size-4 text-muted-foreground" aria-hidden />
                New agent
              </MenuItem>
              <MenuItem
                onClick={() => {
                  close();
                  void navigate({ to: '/settings/profile' });
                }}
              >
                <UserRound
                  className="size-4 text-muted-foreground"
                  aria-hidden
                />
                Profile
              </MenuItem>
              <MenuItem
                onClick={() => {
                  const next = theme === 'dark' ? 'light' : 'dark';
                  setTheme(next);
                  setThemeState(next);
                }}
              >
                {theme === 'dark' ? (
                  <Sun className="size-4 text-muted-foreground" aria-hidden />
                ) : (
                  <Moon className="size-4 text-muted-foreground" aria-hidden />
                )}
                {theme === 'dark' ? 'Light theme' : 'Dark theme'}
              </MenuItem>
              <MenuSeparator />
              <MenuItem
                onClick={() => {
                  close();
                  logout.mutate();
                }}
              >
                <LogOut className="size-4 text-muted-foreground" aria-hidden />
                Sign out
              </MenuItem>
            </>
          )}
        </Menu>

        <Link
          to="/settings"
          aria-label="Settings"
          title="Settings"
          aria-current={inSettings ? 'page' : undefined}
          className={cn(
            'flex size-8 shrink-0 items-center justify-center rounded-lg transition-colors focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
            inSettings
              ? 'bg-accent text-foreground'
              : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
          )}
        >
          <Settings className="size-4" aria-hidden />
        </Link>

        <Link
          to="/projects"
          title="Projects"
          aria-label="Projects"
          className="p-2"
        >
          <FolderGit2 className="size-4" />
        </Link>
        <Link
          to="/pulls"
          title="Pull requests"
          aria-label="Pull requests"
          aria-current={pathname.startsWith('/pulls') ? 'page' : undefined}
          className={cn(
            'flex size-8 shrink-0 items-center justify-center rounded-lg transition-colors focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
            pathname.startsWith('/pulls')
              ? 'bg-accent text-foreground'
              : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
          )}
        >
          <GitPullRequest className="size-4" aria-hidden />
        </Link>
        <div className="ml-auto flex items-center gap-1">
          {(
            [
              ['Automations', Workflow],
              ['Usage', ChartNoAxesColumn],
            ] as const
          ).map(([label, Icon]) => (
            <button
              key={String(label)}
              type="button"
              disabled
              title={String(label) + ' (coming soon)'}
              aria-label={String(label)}
              className="hidden size-6 items-center justify-center text-muted-foreground/40 @[360px]:flex"
            >
              <Icon className="size-3.5" />
            </button>
          ))}
          <button
            type="button"
            aria-pressed={view === 'agents'}
            aria-label={`Agents (${agentsCount})`}
            title="Agents"
            onClick={() => onView(view === 'agents' ? 'threads' : 'agents')}
            className={cn(
              'flex h-8 items-center gap-1.5 rounded-lg px-2 text-xs font-medium transition-colors focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
              view === 'agents'
                ? 'bg-accent text-foreground'
                : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
            )}
          >
            <Bot className="size-3.5 shrink-0" aria-hidden />

            <span
              className={cn(
                'rounded-md px-1.5 py-0.5 text-[11px] leading-none tabular-nums',
                view === 'agents'
                  ? 'bg-background/70 text-foreground'
                  : 'bg-accent/70 text-muted-foreground',
              )}
            >
              {agentsCount}
            </span>
          </button>

          {appsCount > 0 && (
            <button
              type="button"
              aria-pressed={view === 'apps'}
              aria-label={`Apps (${appsCount})`}
              title="Apps"
              onClick={() => onView(view === 'apps' ? 'threads' : 'apps')}
              className={cn(
                'flex h-8 items-center gap-1.5 rounded-lg px-2 text-xs font-medium transition-colors focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
                view === 'apps'
                  ? 'bg-accent text-foreground'
                  : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
              )}
            >
              <AppWindow className="size-3.5 shrink-0" aria-hidden />

              <span
                className={cn(
                  'rounded-md px-1.5 py-0.5 text-[11px] leading-none tabular-nums',
                  view === 'apps'
                    ? 'bg-background/70 text-foreground'
                    : 'bg-accent/70 text-muted-foreground',
                )}
              >
                {appsCount}
              </span>
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

/** New thread: with the agent in view, else the new-thread page. */
function NewThreadLink() {
  const { agentId } = useParams({ strict: false }) as { agentId?: string };
  const { panel } = useSearch({ strict: false }) as { panel?: PanelTab };
  const cls =
    'inline-flex size-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground';
  return agentId ? (
    <Link
      to="/a/$agentId"
      params={{ agentId }}
      search={{ panel }}
      aria-label="New thread"
      title="New thread"
      className={cls}
    >
      <SquarePen className="size-4" />
    </Link>
  ) : (
    <Link to="/new" aria-label="New thread" title="New thread" className={cls}>
      <SquarePen className="size-4" />
    </Link>
  );
}
