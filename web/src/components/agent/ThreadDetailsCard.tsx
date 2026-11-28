import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import {
  Bug,
  ExternalLink,
  Eye,
  EyeOff,
  FolderGit2,
  GitBranch,
  GitPullRequestCreate,
  Hammer,
  Info,
  Loader2,
  Play,
  Radio,
  Settings2,
  SquareCheck,
  TestTube,
  type LucideIcon,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  exposuresQuery,
  gitDiffQuery,
  projectScriptsQuery,
  servicesQuery,
  sessionPullRequestsQuery,
  type Agent,
  type ProjectScript,
  type ProjectScriptRun,
  type Session,
  type SessionPullRequest,
} from '@/api/queries';
import {
  ChecksIndicator,
  PullRequestStateBadge,
} from '@/components/PullRequestBadges';
import { AgentStateDot, stateLabel } from '@/components/AgentStateBadge';
import { ErrorText } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { DiffStat } from '@/components/workspace/DiffPane';
import { useAgentActions } from '@/hooks/useAgentActions';
import { cn } from '@/lib/utils';

const SCRIPT_ICON: Record<ProjectScript['icon'], LucideIcon> = {
  play: Play,
  test: TestTube,
  lint: SquareCheck,
  configure: Settings2,
  build: Hammer,
  debug: Bug,
};

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="grid gap-1.5 border-t px-3 py-2.5 first:border-t-0">
      <h3 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        {title}
      </h3>
      {children}
    </section>
  );
}

/**
 * The thread's environment at a glance, as a popover from the top bar: the
 * machine and its state, the project and branch with what changed, the
 * project's scripts and what the agent serves.
 */
export function ThreadDetailsCard({
  agent,
  session,
  canUse,
  onOpenDiff,
  defaultOpen = false,
}: {
  agent: Agent;
  session?: Session;
  canUse: boolean;
  /** Opens the diff pane (the stats badge is a link to it). */
  onOpenDiff?: () => void;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const down = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const key = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false);
    document.addEventListener('mousedown', down);
    document.addEventListener('keydown', key);
    return () => {
      document.removeEventListener('mousedown', down);
      document.removeEventListener('keydown', key);
    };
  }, [open]);
  return (
    <div ref={ref} className="relative">
      <Button
        variant="ghost"
        size="icon-sm"
        className={open ? 'bg-accent text-foreground' : 'text-muted-foreground'}
        aria-label="Thread details"
        aria-expanded={open}
        title="Thread details"
        onClick={() => setOpen((o) => !o)}
      >
        <Info className="size-4" />
      </Button>
      {open && (
        <div
          role="dialog"
          aria-label="Thread details"
          className="absolute top-full right-0 z-40 mt-2 w-[min(22rem,calc(100vw-1.5rem))] overflow-hidden rounded-xl border bg-card text-sm shadow-xl shadow-black/20"
        >
          <ThreadDetails
            agent={agent}
            session={session}
            canUse={canUse}
            onOpenDiff={
              onOpenDiff &&
              (() => {
                setOpen(false);
                onOpenDiff();
              })
            }
          />
        </div>
      )}
    </div>
  );
}

export function ThreadDetails({
  agent: a,
  session,
  canUse,
  onOpenDiff,
}: {
  agent: Agent;
  session?: Session;
  canUse: boolean;
  onOpenDiff?: () => void;
}) {
  const { action, busy } = useAgentActions(a);
  const live = a.state === 'running';
  const dir = session?.git?.repoPath ?? session?.cwd ?? '';
  const git = session?.git;
  const diff = useQuery({
    ...gitDiffQuery(a.id, dir, 'uncommitted'),
    enabled: live && !!git,
  });
  const scripts = useQuery({
    ...projectScriptsQuery(a.id, session?.cwd ?? dir),
    enabled: live && !!(session?.cwd || dir),
  });
  const exposures = useQuery({ ...exposuresQuery(a.id), enabled: live });
  const services = useQuery({ ...servicesQuery(a.id), enabled: live });
  const runningServices = (services.data?.services ?? []).filter(
    (s) => s.status?.state === 'running',
  );
  const urls = exposures.data?.exposures ?? [];

  return (
    <div data-testid="thread-details">
      <Section title="Environment">
        <div className="flex items-center gap-2">
          <AgentStateDot state={a.state} />
          <span className="min-w-0 flex-1 truncate font-medium">{a.name}</span>
          <span className="shrink-0 text-xs text-muted-foreground">
            {stateLabel(a.state)}
          </span>
          {canUse && (a.state === 'sleeping' || a.state === 'error') && (
            <Button
              size="sm"
              variant="outline"
              className="h-6 px-2 text-xs"
              disabled={busy}
              onClick={() => action.mutate('start')}
            >
              Wake
            </Button>
          )}
          {canUse && a.state === 'running' && (
            <Button
              size="sm"
              variant="outline"
              className="h-6 px-2 text-xs"
              disabled={busy}
              onClick={() => action.mutate('stop')}
            >
              Sleep
            </Button>
          )}
        </div>
        <ErrorText error={action.error} />
      </Section>

      {(git || session?.project) && (
        <Section title="Project">
          <div className="flex min-w-0 items-center gap-2 text-xs">
            <FolderGit2
              className="size-3.5 shrink-0 text-muted-foreground"
              aria-hidden
            />
            <span className="truncate">
              {session?.project?.name || git?.project}
            </span>
            {(git?.branch || session?.project?.branch) && (
              <span className="flex min-w-0 items-center gap-1 font-mono text-muted-foreground">
                <GitBranch className="size-3 shrink-0" aria-hidden />
                <span className="truncate">
                  {git?.branch || session?.project?.branch}
                </span>
              </span>
            )}
            {diff.data && (
              <button
                type="button"
                aria-label="Open diff"
                title="Uncommitted changes: open the diff"
                disabled={!onOpenDiff}
                onClick={onOpenDiff}
                className="ml-auto shrink-0 rounded-md border px-1.5 py-0.5 hover:bg-accent"
              >
                {diff.data.files.length === 0 ? (
                  <span className="text-muted-foreground">clean</span>
                ) : (
                  <DiffStat
                    additions={diff.data.additions}
                    deletions={diff.data.deletions}
                  />
                )}
              </button>
            )}
          </div>
        </Section>
      )}

      {session && (
        <LinkedPullRequests
          agentId={a.id}
          session={session}
          canUse={canUse}
          live={live}
          dir={dir}
        />
      )}

      {live && scripts.data && scripts.data.scripts.length > 0 && (
        <Section title="Scripts">
          <Scripts
            agentId={a.id}
            path={scripts.data.path}
            scripts={scripts.data.scripts}
            canUse={canUse}
          />
        </Section>
      )}
      {scripts.data?.error && (
        <Section title="Scripts">
          <p className="text-xs text-destructive">{scripts.data.error}</p>
        </Section>
      )}

      {live && (urls.length > 0 || runningServices.length > 0) && (
        <Section title="Running">
          <ul className="grid gap-1 text-xs">
            {urls.map((x) => (
              <li key={x.id}>
                <a
                  href={x.url}
                  target="_blank"
                  rel="noreferrer"
                  className="flex items-center gap-2 rounded-md px-1 py-0.5 hover:bg-accent"
                >
                  <ExternalLink
                    className="size-3.5 shrink-0 text-muted-foreground"
                    aria-hidden
                  />
                  <span className="truncate">{x.name}</span>
                  <span className="ml-auto shrink-0 font-mono text-muted-foreground">
                    :{x.port}
                  </span>
                </a>
              </li>
            ))}
            {runningServices.map((s) => (
              <li
                key={s.name}
                className="flex items-center gap-2 px-1 py-0.5 text-muted-foreground"
              >
                <Radio
                  className="size-3.5 shrink-0 text-emerald-500"
                  aria-hidden
                />
                <span className="truncate text-foreground">{s.name}</span>
                <span className="ml-auto min-w-0 truncate font-mono">
                  {s.command}
                </span>
              </li>
            ))}
          </ul>
        </Section>
      )}

      {!live && (
        <p className="border-t px-3 py-2.5 text-xs text-muted-foreground">
          Wake the agent to see its scripts, changes and servers.
        </p>
      )}
    </div>
  );
}

function Scripts({
  agentId,
  path,
  scripts,
  canUse,
}: {
  agentId: string;
  path: string;
  scripts: ProjectScript[];
  canUse: boolean;
}) {
  const qc = useQueryClient();
  const [last, setLast] = useState<ProjectScriptRun | null>(null);
  const run = useMutation({
    mutationFn: (name: string) =>
      unwrap(
        api.POST('/agents/{agentId}/scripts/run', {
          params: { path: { agentId } },
          body: { ...(path && { path }), name },
        }),
      ),
    onSuccess: async (r) => {
      setLast(r);
      if (r.script.previewUrl && r.script.autoOpenPreview)
        window.open(r.script.previewUrl, '_blank', 'noreferrer');
      await qc.invalidateQueries({ queryKey: ['agents', agentId, 'scripts'] });
      await qc.invalidateQueries({ queryKey: ['agents', agentId, 'services'] });
    },
  });
  return (
    <div className="grid gap-1.5">
      <ul className="grid gap-0.5">
        {scripts.map((s) => {
          const Icon = SCRIPT_ICON[s.icon] ?? Play;
          const running = s.status?.state === 'running';
          const pending = run.isPending && run.variables === s.id;
          return (
            <li key={s.id} className="flex items-center gap-2 text-xs">
              <Icon
                className="size-3.5 shrink-0 text-muted-foreground"
                aria-hidden
              />
              <span className="min-w-0 flex-1 truncate" title={s.command}>
                {s.name}
              </span>
              {running && (
                <span className="flex shrink-0 items-center gap-1 text-emerald-600 dark:text-emerald-400">
                  <Radio className="size-3" aria-hidden />
                  running
                </span>
              )}
              {s.previewUrl && (
                <a
                  href={s.previewUrl}
                  target="_blank"
                  rel="noreferrer"
                  aria-label={`Open ${s.name} preview`}
                  title={s.previewUrl}
                  className="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground"
                >
                  <ExternalLink className="size-3.5" aria-hidden />
                </a>
              )}
              {canUse && (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="size-6 text-muted-foreground"
                  aria-label={`Run ${s.name}`}
                  title={`Run: ${s.command}`}
                  disabled={run.isPending}
                  onClick={() => run.mutate(s.id)}
                >
                  {pending ? (
                    <Loader2 className="size-3.5 animate-spin" />
                  ) : (
                    <Play className="size-3.5" />
                  )}
                </Button>
              )}
            </li>
          );
        })}
      </ul>
      <ErrorText error={run.error} />
      {last && (
        <div
          role="status"
          aria-label="Script result"
          className="grid gap-1 rounded-lg border bg-background p-2 text-xs"
        >
          <span className="flex items-center gap-2">
            <span className="font-medium">{last.script.name}</span>
            <span
              className={cn(
                'text-muted-foreground',
                last.exitCode !== undefined &&
                  last.exitCode !== 0 &&
                  'text-destructive',
              )}
            >
              {last.mode === 'service'
                ? `started as ${last.service ?? 'a service'}`
                : last.timedOut
                  ? 'timed out'
                  : `exited ${last.exitCode ?? 0}`}
            </span>
            {last.script.previewUrl && (
              <a
                href={last.script.previewUrl}
                target="_blank"
                rel="noreferrer"
                className="ml-auto underline underline-offset-2"
              >
                Open preview
              </a>
            )}
          </span>
          {last.output && (
            <pre className="max-h-40 overflow-auto font-mono text-[11px] whitespace-pre-wrap text-muted-foreground">
              {last.output}
            </pre>
          )}
        </div>
      )}
    </div>
  );
}

const prLink = (p: SessionPullRequest) => `${p.host}/${p.repo}#${p.number}`;

/**
 * The pull requests linked to the thread: state, CI and the watch toggle,
 * and Create PR when its branch has commits its base has not.
 */
function LinkedPullRequests({
  agentId,
  session,
  canUse,
  live,
  dir,
}: {
  agentId: string;
  session: Session;
  canUse: boolean;
  live: boolean;
  dir: string;
}) {
  const qc = useQueryClient();
  const linked = useQuery(sessionPullRequestsQuery(session.id));
  const prs = linked.data?.pullRequests ?? [];
  const hasOpen = prs.some((p) => p.state === 'open' || p.state === 'draft');
  const git = session.git;
  const branch = useQuery({
    ...gitDiffQuery(agentId, dir, 'branch'),
    enabled: live && !!git && !hasOpen && linked.isSuccess,
  });
  const ahead = (branch.data?.files.length ?? 0) > 0;
  const sub =
    session.parentKind === 'subthread' || session.parentKind === 'side_chat';
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState(session.title);
  const refresh = () =>
    qc.invalidateQueries({
      queryKey: ['sessions', session.id, 'pull-requests'],
    });
  const watch = useMutation({
    mutationFn: (p: SessionPullRequest) =>
      unwrap(
        api.PATCH('/sessions/{sessionId}/pull-requests/{prId}', {
          params: { path: { sessionId: session.id, prId: p.id } },
          body: { watching: !p.watching },
        }),
      ),
    onSettled: refresh,
  });
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/sessions/{sessionId}/pull-requests', {
          params: { path: { sessionId: session.id } },
          body: { create: { title: title.trim() || session.title } },
        }),
      ),
    onSuccess: async () => {
      setCreating(false);
      await refresh();
    },
  });
  const canCreate = canUse && live && !!git && !hasOpen && ahead;
  if (prs.length === 0 && !canCreate) return null;
  return (
    <Section title="Pull requests">
      <ul aria-label="Linked pull requests" className="grid gap-1 text-xs">
        {prs.map((p) => {
          const done = p.state === 'merged' || p.state === 'closed';
          return (
            <li key={p.id} className="flex min-w-0 items-center gap-2">
              <PullRequestStateBadge state={p.state} />
              <ChecksIndicator state={p.checksState} />
              <Link
                to="/pulls"
                search={{ pr: prLink(p) }}
                className="min-w-0 flex-1 truncate hover:underline"
                title={`${p.repo}#${p.number}: ${p.title}`}
              >
                <span className="text-muted-foreground">#{p.number}</span>{' '}
                {p.title}
              </Link>
              <a
                href={p.url}
                target="_blank"
                rel="noreferrer"
                aria-label={`Open #${p.number} on the host`}
                className="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground"
              >
                <ExternalLink className="size-3.5" aria-hidden />
              </a>
              {!done && (
                <Button
                  variant="ghost"
                  size="sm"
                  aria-pressed={p.watching}
                  aria-label={
                    p.watching
                      ? `Stop watching #${p.number}`
                      : `Watch #${p.number}`
                  }
                  title={
                    sub
                      ? 'Sub-threads cannot watch: their parent thread does'
                      : p.watching
                        ? 'Watching: the agent is woken when CI, reviews or the state change'
                        : 'Watch: wake the agent when CI, reviews or the state change'
                  }
                  disabled={!canUse || watch.isPending || (sub && !p.watching)}
                  onClick={() => watch.mutate(p)}
                  className={cn(
                    'h-6 shrink-0 gap-1 px-1.5 text-xs',
                    p.watching
                      ? 'text-sky-600 dark:text-sky-400'
                      : 'text-muted-foreground',
                  )}
                >
                  {p.watching ? (
                    <Eye className="size-3.5" aria-hidden />
                  ) : (
                    <EyeOff className="size-3.5" aria-hidden />
                  )}
                  {p.watching ? 'Watching' : 'Watch'}
                </Button>
              )}
            </li>
          );
        })}
      </ul>
      {canCreate &&
        (creating ? (
          <form
            className="grid gap-1.5"
            onSubmit={(e) => {
              e.preventDefault();
              create.mutate();
            }}
          >
            <Input
              aria-label="Pull request title"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              className="h-7 text-xs"
              autoFocus
            />
            <div className="flex justify-end gap-1.5">
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-6 px-2 text-xs"
                onClick={() => setCreating(false)}
              >
                Cancel
              </Button>
              <Button
                type="submit"
                size="sm"
                className="h-6 px-2 text-xs"
                disabled={create.isPending}
              >
                {create.isPending && (
                  <Loader2 className="size-3 animate-spin" aria-hidden />
                )}
                Push and open
              </Button>
            </div>
          </form>
        ) : (
          <Button
            size="sm"
            variant="outline"
            className="h-6 w-fit gap-1 px-2 text-xs"
            onClick={() => setCreating(true)}
            title={`Push ${git?.branch ?? 'the branch'} and open a pull request`}
          >
            <GitPullRequestCreate className="size-3.5" aria-hidden />
            Create PR
          </Button>
        ))}
      <ErrorText error={watch.error ?? create.error} />
    </Section>
  );
}
