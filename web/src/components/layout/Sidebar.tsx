import { useState, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Link,
  useNavigate,
  useParams,
  useSearch,
} from '@tanstack/react-router';
import {
  AppWindow,
  Bot,
  ChevronRight,
  FolderGit2,
  GitBranch,
  Layers,
  LayoutDashboard,
  Loader2,
  LogOut,
  Moon,
  PanelLeftClose,
  Plus,
  Search,
  Settings,
  SquarePen,
  Sun,
  Users,
  X,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { appsQuery, meQuery, type SessionGit } from '@/api/queries';
import { ROLE_LABEL } from '@/lib/roles';
import { AgentStateDot } from '@/components/AgentStateBadge';
import { Avatar } from '@/components/Avatar';
import { ErrorText } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { useShell } from '@/components/layout/shell';
import { useChatIndex, type ChatEntry } from '@/hooks/useChatIndex';
import { getTheme, setTheme, type Theme } from '@/lib/theme';
import { relTime, useNow } from '@/lib/time';
import { cn } from '@/lib/utils';

const CHATS_PAGE = 40;
const AGENTS_PREVIEW = 6;

export function Sidebar({ onClose }: { onClose?: () => void }) {
  const shell = useShell();
  const me = useQuery(meQuery);
  const index = useChatIndex();
  const params = useParams({ strict: false }) as { agentId?: string };
  const search = useSearch({ strict: false }) as { s?: string };
  const navigate = useNavigate();
  const [query, setQuery] = useState('');
  const [chatLimit, setChatLimit] = useState(CHATS_PAGE);
  const [agentsOpen, setAgentsOpen] = useState(true);
  const [allAgents, setAllAgents] = useState(false);
  const [workingOpen, setWorkingOpen] = useState(true);
  const now = useNow(30_000);

  const q = query.trim().toLowerCase();
  const matches = (...xs: string[]) =>
    !q || xs.some((x) => x.toLowerCase().includes(q));

  const agents = index.agents.filter(({ agent }) =>
    matches(agent.name, agent.owner.name),
  );
  const chats = index.chats.filter(({ session, agent }) =>
    matches(
      session.title,
      agent.name,
      session.git?.project ?? '',
      session.git?.branch ?? '',
    ),
  );
  const working = chats.filter((c) => c.session.running);
  const shownAgents = allAgents || q ? agents : agents.slice(0, AGENTS_PREVIEW);

  const latestSession = (agentId: string) =>
    index.chats.find((c) => c.agent.id === agentId)?.session.id;

  const newChat = () =>
    params.agentId
      ? navigate({ to: '/a/$agentId', params: { agentId: params.agentId } })
      : navigate({ to: '/new' });

  return (
    <nav
      aria-label="Sidebar"
      className="flex h-full w-full flex-col bg-sidebar text-sm"
    >
      <div className="flex h-13 shrink-0 items-center gap-1 px-3">
        <Link
          to="/"
          className="flex min-w-0 flex-1 items-center gap-2 px-1 font-semibold tracking-tight"
        >
          <span
            aria-hidden
            className="flex size-6 items-center justify-center rounded-md bg-foreground text-background"
          >
            <Layers className="size-3.5" />
          </span>
          agenthold
        </Link>
        {onClose ? (
          <Button
            variant="ghost"
            size="icon-sm"
            className="text-muted-foreground"
            aria-label="Close sidebar"
            onClick={onClose}
          >
            <X className="size-4" />
          </Button>
        ) : (
          <Button
            variant="ghost"
            size="icon-sm"
            className="text-muted-foreground"
            aria-label="Hide sidebar"
            title="Hide sidebar"
            onClick={() => shell.setCollapsed(true)}
          >
            <PanelLeftClose className="size-4" />
          </Button>
        )}
      </div>

      <div className="grid gap-1 px-3 pb-2">
        <div className="flex items-center gap-1">
          <label className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-lg px-2 text-muted-foreground focus-within:bg-accent/60 hover:bg-accent/40">
            <Search className="size-4 shrink-0" aria-hidden />
            <input
              type="search"
              aria-label="Search chats and agents"
              placeholder="Search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="min-w-0 flex-1 bg-transparent text-foreground placeholder:text-muted-foreground focus:outline-none"
            />
          </label>
          <Button
            variant="ghost"
            size="icon-sm"
            className="text-muted-foreground"
            aria-label="New agent"
            title="New agent"
            onClick={shell.openNewAgent}
          >
            <Bot className="size-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            className="text-muted-foreground"
            aria-label="New chat"
            title="New chat"
            onClick={() => void newChat()}
          >
            <SquarePen className="size-4" />
          </Button>
        </div>
      </div>

      {me.data?.admin && (
        <div className="px-3 pb-2">
          <Link
            to="/overview"
            className="flex h-8 items-center gap-2 rounded-lg px-2 text-muted-foreground hover:bg-accent/50 hover:text-foreground data-[status=active]:bg-accent/70 data-[status=active]:text-foreground"
          >
            <LayoutDashboard className="size-4 shrink-0" aria-hidden />
            Overview
          </Link>
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-3">
        <ErrorText error={index.error} />

        <SectionHeader
          open={agentsOpen}
          onToggle={() => setAgentsOpen((o) => !o)}
          count={index.loading ? undefined : agents.length}
        >
          Agents
        </SectionHeader>
        {agentsOpen && (
          <ul aria-label="Agents" className="mb-3 grid grid-cols-1 gap-px">
            {index.loading && agents.length === 0 && <SkeletonRows n={3} />}
            {!index.loading && agents.length === 0 && (
              <li className="px-2 py-1.5 text-muted-foreground">
                {q ? 'No matching agents.' : 'No agents yet.'}
              </li>
            )}
            {shownAgents.map(({ agent }) => {
              const active = params.agentId === agent.id;
              return (
                <li key={agent.id} className="group relative">
                  <Link
                    to="/a/$agentId"
                    params={{ agentId: agent.id }}
                    search={{ s: latestSession(agent.id) }}
                    aria-current={active ? 'page' : undefined}
                    className={cn(
                      'flex h-8 items-center gap-2 rounded-lg pr-9 pl-2 hover:bg-accent/50',
                      active && 'bg-accent/70',
                    )}
                  >
                    <AgentStateDot state={agent.state} />
                    <span className="min-w-0 flex-1 truncate">
                      {agent.name}
                    </span>
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
                    aria-label={`New chat with ${agent.name}`}
                    title="New chat"
                    className="absolute top-1 right-1 flex size-6 items-center justify-center rounded-md text-muted-foreground opacity-0 group-hover:opacity-100 hover:bg-accent hover:text-foreground focus-visible:opacity-100"
                  >
                    <Plus className="size-3.5" />
                  </Link>
                </li>
              );
            })}
            {!q && agents.length > AGENTS_PREVIEW && (
              <li>
                <button
                  type="button"
                  onClick={() => setAllAgents((a) => !a)}
                  className="px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
                >
                  {allAgents
                    ? 'Show fewer'
                    : `Show all ${agents.length} agents`}
                </button>
              </li>
            )}
          </ul>
        )}

        <AppsSection query={q} />

        <SectionHeader>Chats</SectionHeader>
        <ul aria-label="Chats" className="grid grid-cols-1 gap-px">
          {index.loading && chats.length === 0 && <SkeletonRows n={4} tall />}
          {!index.loading && chats.length === 0 && (
            <li className="px-2 py-1.5 text-muted-foreground">
              {q ? 'No matching chats.' : 'No chats yet.'}
            </li>
          )}
          {chats.slice(0, chatLimit).map((c) => (
            <ChatRow
              key={c.session.id}
              chat={c}
              now={now}
              active={search.s === c.session.id}
              me={me.data?.user.id}
            />
          ))}
          {chats.length > chatLimit && (
            <li>
              <button
                type="button"
                onClick={() => setChatLimit((l) => l + CHATS_PAGE)}
                className="px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
              >
                Show more
              </button>
            </li>
          )}
        </ul>
      </div>

      {working.length > 0 && (
        <div className="shrink-0 border-t px-3 pt-2 pb-1">
          <SectionHeader
            open={workingOpen}
            onToggle={() => setWorkingOpen((o) => !o)}
            count={working.length}
          >
            Working
          </SectionHeader>
          {workingOpen && (
            <ul
              aria-label="Working"
              className="grid max-h-48 grid-cols-1 gap-1 overflow-y-auto"
            >
              {working.map((c) => (
                <li key={c.session.id}>
                  <Link
                    to="/a/$agentId"
                    params={{ agentId: c.agent.id }}
                    search={{ s: c.session.id }}
                    className={cn(
                      'flex items-center gap-2 rounded-lg px-2 py-1.5 hover:bg-accent/50',
                      search.s === c.session.id && 'bg-accent/70',
                    )}
                  >
                    <Loader2
                      className="size-3.5 shrink-0 animate-spin text-sky-400"
                      aria-hidden
                    />
                    <span className="grid min-w-0 flex-1">
                      <span className="truncate">{c.session.title}</span>
                      {hasGit(c.session.git) && (
                        <GitLine git={c.session.git} compact />
                      )}
                    </span>
                    <span className="max-w-[40%] truncate text-xs text-muted-foreground">
                      {c.agent.name}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      <Footer />
    </nav>
  );
}

function SectionHeader({
  children,
  count,
  open,
  onToggle,
}: {
  children: ReactNode;
  count?: number;
  open?: boolean;
  onToggle?: () => void;
}) {
  const inner = (
    <>
      <span className="flex-1 text-left">{children}</span>
      {count != null && <span className="tabular-nums">{count}</span>}
      {onToggle && (
        <ChevronRight
          aria-hidden
          className={cn('size-3.5 transition-transform', open && 'rotate-90')}
        />
      )}
    </>
  );
  const cls =
    'flex h-7 w-full items-center gap-2 px-2 text-xs font-medium text-muted-foreground';
  return onToggle ? (
    <button
      type="button"
      aria-expanded={open}
      onClick={onToggle}
      className={cn(cls, 'rounded-md hover:text-foreground')}
    >
      {inner}
    </button>
  ) : (
    <div className={cls}>{inner}</div>
  );
}

function ChatRow({
  chat: { session, agent },
  now,
  active,
  me,
}: {
  chat: ChatEntry;
  now: number;
  active: boolean;
  me?: string;
}) {
  const by =
    session.origin != null
      ? `from ${session.origin.agentName}`
      : me && session.createdBy.id !== me
        ? `by ${session.createdBy.name}`
        : null;
  return (
    <li>
      <Link
        to="/a/$agentId"
        params={{ agentId: agent.id }}
        search={{ s: session.id }}
        aria-current={active ? 'page' : undefined}
        className={cn(
          'grid grid-cols-1 gap-0.5 rounded-lg px-2 py-2 hover:bg-accent/50',
          active && 'bg-accent/70 hover:bg-accent/70',
        )}
      >
        <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <span className="min-w-0 flex-1 truncate">{agent.name}</span>
          <span className="shrink-0 tabular-nums">
            {relTime(session.updatedAt, now)}
          </span>
        </span>
        <span className="flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-[13.5px] text-foreground">
            {session.title || 'New session'}
          </span>
          {session.running && (
            <Loader2
              role="img"
              aria-label="running"
              className="size-3.5 shrink-0 animate-spin text-sky-400"
            />
          )}
        </span>
        {hasGit(session.git) && <GitLine git={session.git} />}
        {by && (
          <span className="flex items-center gap-1 truncate text-xs text-muted-foreground">
            {session.origin && <Bot className="size-3" aria-hidden />}
            {by}
          </span>
        )}
      </Link>
    </li>
  );
}

const hasGit = (g?: SessionGit): g is SessionGit =>
  !!g && !!(g.project || g.branch);

/** The project and branch a chat is on, one compact truncating line. */
function GitLine({ git, compact }: { git: SessionGit; compact?: boolean }) {
  return (
    <span
      data-testid="chat-git"
      title={[git.project, git.branch].filter(Boolean).join(' · ')}
      className={cn(
        'flex min-w-0 items-center gap-2 truncate text-muted-foreground',
        compact ? 'text-[11px]' : 'text-xs',
      )}
    >
      {git.project && (
        <span className="flex min-w-0 items-center gap-1">
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

function Footer() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const me = useQuery(meQuery);
  const [theme, setThemeState] = useState<Theme>(getTheme);
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
  const user = me.data?.user;
  return (
    <div className="flex shrink-0 items-center gap-1 border-t px-3 py-2">
      <Link
        to="/settings"
        className="flex min-w-0 flex-1 items-center gap-2 rounded-lg px-1 py-1 hover:bg-accent/40"
        title="Account settings"
      >
        {user && <Avatar name={user.name} url={user.avatarUrl} size="sm" />}
        <span className="grid min-w-0 leading-tight">
          <span className="truncate">{user?.name}</span>
          {me.data?.role && (
            <span className="text-xs text-muted-foreground">
              {ROLE_LABEL[me.data.role]}
            </span>
          )}
        </span>
      </Link>
      <Button
        asChild
        variant="ghost"
        size="icon-sm"
        className="text-muted-foreground"
      >
        <Link to="/settings" aria-label="Settings" title="Settings">
          <Settings className="size-4" />
        </Link>
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        className="text-muted-foreground"
        aria-label={theme === 'dark' ? 'Light theme' : 'Dark theme'}
        title={theme === 'dark' ? 'Light theme' : 'Dark theme'}
        onClick={() => {
          const next = theme === 'dark' ? 'light' : 'dark';
          setTheme(next);
          setThemeState(next);
        }}
      >
        {theme === 'dark' ? (
          <Sun className="size-4" />
        ) : (
          <Moon className="size-4" />
        )}
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        className="text-muted-foreground"
        aria-label="Sign out"
        title="Sign out"
        onClick={() => logout.mutate()}
      >
        <LogOut className="size-4" />
      </Button>
    </div>
  );
}

/** Apps: machines without a harness, reached at their URLs. */
function AppsSection({ query: q }: { query: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { appId?: string };
  const [open, setOpen] = useState(true);
  const apps = useQuery(appsQuery);
  const create = useMutation({
    mutationFn: (name: string) => unwrap(api.POST('/apps', { body: { name } })),
    onSuccess: async (app) => {
      await qc.invalidateQueries({ queryKey: appsQuery.queryKey });
      await navigate({ to: '/apps/$appId', params: { appId: app.id } });
    },
  });
  const list = (apps.data ?? []).filter(
    (a) => !q || a.name.toLowerCase().includes(q),
  );
  return (
    <>
      <div className="flex items-center">
        <div className="flex-1">
          <SectionHeader
            open={open}
            onToggle={() => setOpen((o) => !o)}
            count={apps.data?.length}
          >
            Apps
          </SectionHeader>
        </div>
        <button
          type="button"
          aria-label="New app"
          title="New app"
          disabled={create.isPending}
          onClick={() => {
            const name = globalThis.prompt?.('Name of the new app')?.trim();
            if (name) create.mutate(name);
          }}
          className="flex size-6 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          <Plus className="size-3.5" />
        </button>
      </div>
      <ErrorText error={apps.error ?? create.error} />
      {open && (
        <ul aria-label="Apps" className="mb-3 grid grid-cols-1 gap-px">
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
                className={cn(
                  'flex h-8 items-center gap-2 rounded-lg px-2 hover:bg-accent/50',
                  params.appId === a.id && 'bg-accent/70',
                )}
              >
                <AgentStateDot state={a.state} />
                <AppWindow
                  className="size-3.5 shrink-0 text-muted-foreground"
                  aria-hidden
                />
                <span className="min-w-0 flex-1 truncate">{a.name}</span>
                {a.url && (
                  <span className="max-w-[45%] truncate text-xs text-muted-foreground">
                    {a.url.replace(/^https?:\/\//, '').replace(/\/$/, '')}
                  </span>
                )}
              </Link>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}
