import { useId, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Link,
  useNavigate,
  useParams,
  useSearch,
} from "@tanstack/react-router";
import {
  Bot,
  Check,
  ChevronRight,
  ChevronsUpDown,
  FolderGit2,
  GitBranch,
  Layers,
  Loader2,
  LogOut,
  Moon,
  PanelLeftClose,
  Plus,
  Search,
  Settings,
  Sun,
  Undo2,
  Users,
  X,
} from "lucide-react";
import { api, unwrap } from "@/api/client";
import { appsQuery, meQuery, type SessionGit } from "@/api/queries";
import { ROLE_LABEL } from "@/lib/roles";
import { AgentStateDot } from "@/components/AgentStateBadge";
import { AgentBadge } from "@/components/AgentBadge";
import { Avatar } from "@/components/Avatar";
import { ErrorText } from "@/components/Spinner";
import { Button } from "@/components/ui/button";
import { Menu, MenuItem, MenuSeparator } from "@/components/ui/menu";
import { readPref, useShell, writePref } from "@/components/layout/shell";
import { useChatIndex, type ChatEntry } from "@/hooks/useChatIndex";
import { useJustFinished } from "@/hooks/useJustFinished";
import { useLogout } from "@/hooks/useLogout";
import { useSettle } from "@/hooks/useSettle";
import { getTheme, setTheme, type Theme } from "@/lib/theme";
import { relTime, useNow } from "@/lib/time";
import { cn } from "@/lib/utils";

const THREADS_PAGE = 40;
const SETTLED_PREVIEW = 10;
const SETTLED_PAGE = 50;
const AGENTS_PREVIEW = 6;
/** localStorage key (agenthold.<key>) of the Settled section's fold. */
export const SETTLED_OPEN_PREF = "sidebar.settledOpen";
/** localStorage key of the Apps section's fold. */
export const APPS_OPEN_PREF = "sidebar.appsOpen";

/** Who may settle: whoever may send prompts to the agent. */
const canUse = (c: ChatEntry) =>
  c.agent.role === "owner" || c.agent.role === "shared";

export function Sidebar({ onClose }: { onClose?: () => void }) {
  const shell = useShell();
  const me = useQuery(meQuery);
  const index = useChatIndex();
  const params = useParams({ strict: false }) as {
    agentId?: string;
    appId?: string;
  };
  const search = useSearch({ strict: false }) as { s?: string };
  const [query, setQuery] = useState("");
  const apps = useQuery(appsQuery);
  const [appsOpen, setAppsOpenState] = useState(
    () => !!params.appId || readPref(APPS_OPEN_PREF, "") === "1",
  );
  const setAppsOpen = (o: boolean) => {
    setAppsOpenState(o);
    writePref(APPS_OPEN_PREF, o ? "1" : "");
  };
  const [threadLimit, setThreadLimit] = useState(THREADS_PAGE);
  const [settledLimit, setSettledLimit] = useState(SETTLED_PREVIEW);
  const [settledOpen, setSettledOpenState] = useState(
    () => readPref(SETTLED_OPEN_PREF, "") === "1",
  );
  const [agentsOpen, setAgentsOpen] = useState(true);
  const [allAgents, setAllAgents] = useState(false);
  const settle = useSettle();
  const settledListId = useId();
  const now = useNow(30_000);
  const done = useJustFinished(
    index.active.map((c) => c.session),
    search.s,
  );

  const setSettledOpen = (o: boolean) => {
    setSettledOpenState(o);
    writePref(SETTLED_OPEN_PREF, o ? "1" : "");
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
      session.git?.project ?? "",
      session.git?.branch ?? "",
    );
  const active = index.active.filter(threadMatches);
  const settled = index.settled.filter(threadMatches);
  // A search looks into settled threads too.
  const showSettled = settledOpen || (!!q && settled.length > 0);
  const shownAgents = allAgents || q ? agents : agents.slice(0, AGENTS_PREVIEW);

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
          className="shrink-0 px-1"
        >
          <span
            aria-hidden
            className="flex size-7 items-center justify-center rounded-md bg-zinc-900 text-zinc-50 ring-1 ring-white/15"
          >
            <Layers className="size-4" />
          </span>
        </Link>
        <label className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-lg px-2 text-muted-foreground focus-within:bg-accent/60 hover:bg-accent/40">
          <Search className="size-4 shrink-0" aria-hidden />
          <input
            type="search"
            aria-label="Search threads, agents and apps"
            placeholder="Search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="min-w-0 flex-1 bg-transparent text-foreground placeholder:text-muted-foreground focus:outline-none"
          />
        </label>
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

      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-3">
        <ErrorText error={index.error ?? settle.error} />

        <ul aria-label="Threads" className="mb-3 grid grid-cols-1 gap-1">
          {index.loading && active.length === 0 && <SkeletonRows n={4} tall />}
          {!index.loading && active.length === 0 && (
            <li className="px-2 py-1.5 text-muted-foreground">
              {q
                ? "No matching active threads."
                : index.settled.length > 0
                  ? "No active threads. Settled ones are below."
                  : "No threads yet."}
            </li>
          )}
          {active.slice(0, threadLimit).map((c) => (
            <ThreadCard
              key={c.session.id}
              chat={c}
              now={now}
              active={search.s === c.session.id}
              done={done.has(c.session.id)}
              me={me.data?.user.id}
              onSettle={
                canUse(c)
                  ? () => settle.mutate({ session: c.session, settle: true })
                  : undefined
              }
              settling={
                settle.isPending &&
                settle.variables?.session.id === c.session.id
              }
            />
          ))}
          {active.length > threadLimit && (
            <li>
              <button
                type="button"
                onClick={() => setThreadLimit((l) => l + THREADS_PAGE)}
                className="px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
              >
                Show more
              </button>
            </li>
          )}
        </ul>

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
                {q ? "No matching agents." : "No agents yet."}
              </li>
            )}
            {shownAgents.map(({ agent }) => {
              const current = params.agentId === agent.id;
              return (
                <li key={agent.id} className="group relative">
                  <Link
                    to="/a/$agentId"
                    params={{ agentId: agent.id }}
                    search={{ s: latestSession(agent.id) }}
                    aria-current={current ? "page" : undefined}
                    className={cn(
                      "flex h-8 items-center gap-2 rounded-lg pr-9 pl-2 hover:bg-accent/50",
                      current && "bg-accent/70",
                    )}
                  >
                    <AgentStateDot state={agent.state} />
                    <span className="min-w-0 flex-1 truncate">
                      {agent.name}
                    </span>
                    {agent.role === "shared" && (
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
                    className="absolute top-1 right-1 flex size-6 items-center justify-center rounded-md text-muted-foreground opacity-0 group-hover:opacity-100 hover:bg-accent hover:text-foreground focus-visible:opacity-100 [@media(hover:none)]:opacity-100"
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
                    ? "Show fewer"
                    : `Show all ${agents.length} agents`}
                </button>
              </li>
            )}
          </ul>
        )}
      </div>

      <div className="shrink-0 border-t px-3 pt-1 pb-1 empty:hidden">
        <AppsSection
          query={q}
          open={appsOpen}
          onToggle={() => setAppsOpen(!appsOpen)}
        />
        {index.settled.length > 0 && (
          <>
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
                  "size-3.5 transition-transform",
                  showSettled && "rotate-90",
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
                      canUse(c)
                        ? () =>
                            settle.mutate({ session: c.session, settle: false })
                        : undefined
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
                      Show{" "}
                      {Math.min(settled.length - settledLimit, SETTLED_PAGE)}{" "}
                      more
                    </button>
                  </li>
                )}
              </ul>
            )}
          </>
        )}
      </div>

      <Footer />
    </nav>
  );
}

function SidebarTab({
  selected,
  onSelect,
  count,
  children,
}: {
  selected: boolean;
  onSelect: () => void;
  count?: number;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={selected}
      onClick={onSelect}
      className={cn(
        "flex h-7 items-center justify-center gap-1.5 rounded-md",
        selected
          ? "bg-background text-foreground shadow-xs"
          : "text-muted-foreground hover:text-foreground",
      )}
    >
      {children}
      {count != null && count > 0 && (
        <span className="text-muted-foreground tabular-nums">{count}</span>
      )}
    </button>
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
          className={cn("size-3.5 transition-transform", open && "rotate-90")}
        />
      )}
    </>
  );
  const cls =
    "flex h-7 w-full items-center gap-2 px-2 text-xs font-medium text-muted-foreground";
  return onToggle ? (
    <button
      type="button"
      aria-expanded={open}
      onClick={onToggle}
      className={cn(cls, "rounded-md hover:text-foreground")}
    >
      {inner}
    </button>
  ) : (
    <div className={cls}>{inner}</div>
  );
}

/** An active thread: agent, title, and how it stands; Settle on hover. */
function ThreadCard({
  chat: { session, agent },
  now,
  active,
  done,
  me,
  onSettle,
  settling,
}: {
  chat: ChatEntry;
  now: number;
  active: boolean;
  done: boolean;
  me?: string;
  onSettle?: () => void;
  settling: boolean;
}) {
  const title = session.title || "New thread";
  const by =
    session.origin != null
      ? `from ${session.origin.agentName}`
      : me && session.createdBy.id !== me
        ? `by ${session.createdBy.name}`
        : null;
  const status = session.running ? (
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
    <li className="group relative">
      <Link
        to="/a/$agentId"
        params={{ agentId: agent.id }}
        search={{ s: session.id }}
        aria-current={active ? "page" : undefined}
        className={cn(
          "grid grid-cols-1 gap-0.5 rounded-lg bg-accent/25 px-2 py-1.5 hover:bg-accent/50",
          active && "bg-accent/70 hover:bg-accent/70",
        )}
      >
        <span className="truncate text-[13px] leading-5 font-medium text-foreground">
          {title}
        </span>
        <span className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
          <AgentBadge name={agent.name} />
          <span className="min-w-0 truncate">
            {[agent.name, by].filter(Boolean).join(" · ")}
          </span>
          {session.origin && <Bot className="size-3 shrink-0" aria-hidden />}
          {agent.role === "shared" && (
            <Users
              className="size-3 shrink-0"
              aria-label={`shared by ${agent.owner.name}`}
            />
          )}
          <span
            className={cn(
              "ml-auto flex shrink-0 items-center",
              onSettle &&
                "group-focus-within:invisible group-hover:invisible [@media(hover:none)]:invisible",
            )}
          >
            {status}
          </span>
        </span>
        {hasGit(session.git) && <GitLine git={session.git} />}
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
    </li>
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
  const title = session.title || "New thread";
  return (
    <li className="group relative">
      <Link
        to="/a/$agentId"
        params={{ agentId: agent.id }}
        search={{ s: session.id }}
        aria-current={active ? "page" : undefined}
        title={[title, agent.name, session.git?.project, session.git?.branch]
          .filter(Boolean)
          .join(" · ")}
        className={cn(
          "flex h-8 items-center gap-2 rounded-lg bg-accent/25 px-2 hover:bg-accent/50",
          active && "bg-accent/70",
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
            "shrink-0 text-xs text-muted-foreground tabular-nums",
            onUnsettle &&
              "group-focus-within:invisible group-hover:invisible [@media(hover:none)]:invisible",
          )}
        >
          {relTime(session.updatedAt, now)}
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

/** The project and branch a thread is on, one compact truncating line. */
function GitLine({ git }: { git: SessionGit }) {
  return (
    <span
      data-testid="chat-git"
      title={[git.project, git.branch].filter(Boolean).join(" · ")}
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
            "mx-2 my-1 animate-pulse rounded-md bg-accent/50",
            tall ? "h-9" : "h-5",
          )}
        />
      ))}
    </>
  );
}

/** You: a menu with new agent, settings, theme and sign out. */
function Footer() {
  const shell = useShell();
  const navigate = useNavigate();
  const me = useQuery(meQuery);
  const logout = useLogout();
  const [theme, setThemeState] = useState<Theme>(getTheme);
  const user = me.data?.user;
  return (
    <div className="shrink-0 border-t px-2 py-1.5">
      <Menu
        side="top"
        className="w-full"
        trigger={({ open, toggle }) => (
          <button
            type="button"
            aria-haspopup="menu"
            aria-expanded={open}
            aria-label="Account menu"
            onClick={toggle}
            className="flex w-full min-w-0 items-center gap-2 rounded-lg px-1.5 py-1 text-left hover:bg-accent/40"
          >
            {user && <Avatar name={user.name} url={user.avatarUrl} size="sm" />}
            <span className="grid min-w-0 flex-1 leading-tight">
              <span className="truncate">{user?.name}</span>
              {me.data?.role && (
                <span className="text-xs text-muted-foreground">
                  {ROLE_LABEL[me.data.role]}
                </span>
              )}
            </span>
            <ChevronsUpDown
              className="size-3.5 shrink-0 text-muted-foreground"
              aria-hidden
            />
          </button>
        )}
      >
        {(close) => (
          <>
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
                void navigate({ to: "/settings" });
              }}
            >
              <Settings className="size-4 text-muted-foreground" aria-hidden />
              Settings
            </MenuItem>
            <MenuItem
              onClick={() => {
                const next = theme === "dark" ? "light" : "dark";
                setTheme(next);
                setThemeState(next);
              }}
            >
              {theme === "dark" ? (
                <Sun className="size-4 text-muted-foreground" aria-hidden />
              ) : (
                <Moon className="size-4 text-muted-foreground" aria-hidden />
              )}
              {theme === "dark" ? "Light theme" : "Dark theme"}
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
    </div>
  );
}

/** Apps: machines without a harness, reached at their URLs. */
function AppsSection({
  query: q,
  open,
  onToggle,
}: {
  query: string;
  open: boolean;
  onToggle: () => void;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { appId?: string };
  const apps = useQuery(appsQuery);
  const create = useMutation({
    mutationFn: (name: string) => unwrap(api.POST("/apps", { body: { name } })),
    onSuccess: async (app) => {
      await qc.invalidateQueries({ queryKey: appsQuery.queryKey });
      await navigate({ to: "/apps/$appId", params: { appId: app.id } });
    },
  });
  const all = apps.data ?? [];
  const list = all.filter((a) => !q || a.name.toLowerCase().includes(q));
  // Folded, the section still lists the apps that are running.
  const expanded = open || !!q;
  const shown = expanded ? list : list.filter((a) => a.state === "running");
  if (apps.isSuccess && all.length === 0 && !expanded) return null;
  return (
    <>
      <button
        type="button"
        aria-expanded={expanded}
        onClick={onToggle}
        className="flex h-8 w-full items-center gap-2 rounded-md px-2 text-xs font-medium text-muted-foreground hover:text-foreground"
      >
        <span className="flex-1 text-left">Apps</span>
        <span className="tabular-nums">{all.length}</span>
        <ChevronRight
          aria-hidden
          className={cn(
            "size-3.5 transition-transform",
            expanded && "rotate-90",
          )}
        />
      </button>
      <ErrorText error={apps.error ?? create.error} />
      <ul
        aria-label="Apps"
        className="grid max-h-[30vh] grid-cols-1 gap-px overflow-y-auto pb-1"
      >
        {expanded && apps.isPending && <SkeletonRows n={3} />}
        {expanded && apps.isSuccess && list.length === 0 && (
          <li className="px-2 py-1.5 text-muted-foreground">
            {q
              ? "No matching apps."
              : "No apps yet. Agents create them with deploy_app."}
          </li>
        )}
        {shown.map((a) => (
          <li key={a.id}>
            <Link
              to="/apps/$appId"
              params={{ appId: a.id }}
              aria-current={params.appId === a.id ? "page" : undefined}
              title={a.url || a.name}
              className={cn(
                "grid grid-cols-1 rounded-lg px-2 py-1.5 hover:bg-accent/50",
                params.appId === a.id && "bg-accent/70",
              )}
            >
              <span className="flex min-w-0 items-center gap-2">
                <AgentStateDot state={a.state} />
                <span className="min-w-0 flex-1 truncate">{a.name}</span>
              </span>
              {a.url && (
                <span className="truncate pl-4 text-xs text-muted-foreground">
                  {a.url.replace(/^https?:\/\//, "").replace(/\/$/, "")}
                </span>
              )}
            </Link>
          </li>
        ))}
        {expanded && (
          <li>
            <button
              type="button"
              disabled={create.isPending}
              onClick={() => {
                const name = globalThis.prompt?.("Name of the new app")?.trim();
                if (name) create.mutate(name);
              }}
              className="flex h-8 w-full items-center gap-2 rounded-lg px-2 text-muted-foreground hover:bg-accent/50 hover:text-foreground disabled:opacity-50"
            >
              <Plus className="size-3.5" aria-hidden />
              New app
            </button>
          </li>
        )}
      </ul>
    </>
  );
}
