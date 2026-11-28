import { lazy, Suspense, useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import {
  ChevronDown,
  ChevronRight,
  ExternalLink,
  GitBranch,
  GitPullRequest,
  Loader2,
  MessageSquare,
  Search,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  agentsQuery,
  pullRequestQuery,
  pullRequestsQuery,
  type PullRequestComment,
  type PullRequestDetail,
  type PullRequestFile,
  type PullRequestFilter,
  type PullRequestListState,
  type PullRequestSummary,
} from '@/api/queries';
import {
  ChecksIndicator,
  PullRequestStateBadge,
} from '@/components/PullRequestBadges';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { Crumb, TopBar } from '@/components/layout/TopBar';
import { DiffStat } from '@/components/workspace/DiffPane';
import { useDocumentTitle } from '@/hooks/useDocumentTitle';
import { parsePatch } from '@/lib/patch';
import { relTime, useNow } from '@/lib/time';
import { cn } from '@/lib/utils';

const Markdown = lazy(() => import('@/components/chat/Markdown'));

const STATES: { id: PullRequestListState; label: string }[] = [
  { id: 'open', label: 'Open' },
  { id: 'merged', label: 'Merged' },
  { id: 'closed', label: 'Closed' },
];

const FILTERS: { id: PullRequestFilter; label: string }[] = [
  { id: 'involved', label: 'Involving me' },
  { id: 'authored', label: 'Created by me' },
  { id: 'review_requested', label: 'Review requested' },
];

/** "host/owner/repo#number": a pull request's key in the page's search. */
export function prKey(p: { host: string; repo: string; number: number }) {
  return `${p.host}/${p.repo}#${p.number}`;
}

function parseKey(
  key: string | undefined,
): { host: string; repo: string; number: number } | null {
  const m = key?.match(/^([^/]+)\/([^/]+\/[^/#]+)#(\d+)$/);
  return m ? { host: m[1]!, repo: m[2]!, number: Number(m[3]) } : null;
}

/**
 * The pull request hub: the pull requests of the user's git accounts, one
 * read in full beside the list (description, checks, comments, files), to
 * comment on, merge or start a thread on.
 */
export function PullRequestsPage({
  selected,
  onSelect,
}: {
  /** The open pull request (prKey). */
  selected?: string;
  onSelect?: (key: string | undefined) => void;
}) {
  useDocumentTitle('Pull requests');
  const [state, setState] = useState<PullRequestListState>('open');
  const [filter, setFilter] = useState<PullRequestFilter>('involved');
  const [search, setSearch] = useState('');
  const [localSel, setLocalSel] = useState<string | undefined>(selected);
  const sel = onSelect ? selected : localSel;
  const select = onSelect ?? setLocalSel;
  const list = useQuery(pullRequestsQuery(state, filter));
  const now = useNow(60_000);

  const shown = useMemo(() => {
    const q = search.trim().toLowerCase();
    const all = list.data?.pullRequests ?? [];
    if (!q) return all;
    return all.filter((p) =>
      `${p.repo} #${p.number} ${p.title} ${p.author} ${p.headRef ?? ''}`
        .toLowerCase()
        .includes(q),
    );
  }, [list.data, search]);
  const current = parseKey(sel);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <TopBar>
        <Crumb current>
          <GitPullRequest className="size-4" aria-hidden />
          Pull requests
        </Crumb>
      </TopBar>
      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <aside
          className={cn(
            'flex min-h-0 flex-col border-b md:w-[22rem] md:shrink-0 md:border-r md:border-b-0',
            current && 'hidden md:flex',
          )}
        >
          <div className="grid gap-2 p-3">
            <div
              role="tablist"
              aria-label="State"
              className="flex gap-1 rounded-lg bg-muted p-0.5"
            >
              {STATES.map((s) => (
                <button
                  key={s.id}
                  type="button"
                  role="tab"
                  aria-selected={state === s.id}
                  onClick={() => setState(s.id)}
                  className={cn(
                    'flex-1 rounded-md px-2 py-1 text-xs font-medium transition-colors',
                    state === s.id
                      ? 'bg-background text-foreground shadow-sm'
                      : 'text-muted-foreground hover:text-foreground',
                  )}
                >
                  {s.label}
                </button>
              ))}
            </div>
            <div className="flex gap-2">
              <div className="relative flex-1">
                <Search
                  className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted-foreground"
                  aria-hidden
                />
                <Input
                  aria-label="Search pull requests"
                  placeholder="Search"
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  className="h-8 pl-7 text-xs"
                />
              </div>
              <Select
                aria-label="Filter"
                value={filter}
                onChange={(e) => setFilter(e.target.value as PullRequestFilter)}
                className="h-8 text-xs"
              >
                {FILTERS.map((f) => (
                  <option key={f.id} value={f.id}>
                    {f.label}
                  </option>
                ))}
              </Select>
            </div>
          </div>
          <div className="min-h-0 flex-1 overflow-auto">
            {list.isPending && <Loading label="Loading pull requests" />}
            <div className="px-3">
              <ErrorText error={list.error} />
            </div>
            {list.data?.errors.map((e) => (
              <p
                key={e.host}
                role="alert"
                className="px-3 py-1 text-xs text-destructive"
              >
                {e.host}: {e.error}
              </p>
            ))}
            {list.data && list.data.hosts.length === 0 && (
              <div className="grid gap-1 p-4 text-sm text-muted-foreground">
                <p>Connect a git account to see your pull requests.</p>
                <Link
                  to="/settings/git"
                  className="text-foreground underline underline-offset-2"
                >
                  Settings → Git
                </Link>
              </div>
            )}
            {list.data && list.data.hosts.length > 0 && shown.length === 0 && (
              <p className="p-4 text-sm text-muted-foreground">
                No {state === 'open' ? 'open' : state} pull requests
                {search && ' match'}.
              </p>
            )}
            <ul aria-label="Pull requests" className="grid gap-0.5 px-1.5 pb-3">
              {shown.map((p) => (
                <PullRequestRow
                  key={prKey(p)}
                  pr={p}
                  now={now}
                  active={sel === prKey(p)}
                  onClick={() => select(prKey(p))}
                />
              ))}
            </ul>
          </div>
        </aside>
        <main className="flex min-h-0 min-w-0 flex-1 flex-col">
          {current ? (
            <PullRequestView
              key={sel}
              host={current.host}
              repo={current.repo}
              number={current.number}
              onBack={() => select(undefined)}
            />
          ) : (
            <div className="hidden flex-1 items-center justify-center text-sm text-muted-foreground md:flex">
              Pick a pull request to read it.
            </div>
          )}
        </main>
      </div>
    </div>
  );
}

function PullRequestRow({
  pr: p,
  now,
  active,
  onClick,
}: {
  pr: PullRequestSummary;
  now: number;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <li>
      <button
        type="button"
        onClick={onClick}
        aria-current={active ? 'true' : undefined}
        className={cn(
          'grid w-full gap-0.5 rounded-lg px-2 py-1.5 text-left transition-colors',
          active ? 'bg-accent' : 'hover:bg-accent/60',
        )}
      >
        <span className="flex min-w-0 items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-sm font-medium">
            {p.title}
          </span>
          <PullRequestStateBadge state={p.state} />
        </span>
        <span className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
          <span className="truncate">
            {p.repo} #{p.number}
          </span>
          <span aria-hidden>·</span>
          <span className="truncate">{p.author}</span>
          {p.comments > 0 && (
            <span className="flex items-center gap-0.5">
              <MessageSquare className="size-3" aria-hidden />
              {p.comments}
            </span>
          )}
          <span className="ml-auto shrink-0">{relTime(p.updatedAt, now)}</span>
        </span>
      </button>
    </li>
  );
}

type Tab = 'description' | 'checks' | 'comments' | 'files';

export function PullRequestView({
  host,
  repo,
  number,
  onBack,
}: {
  host: string;
  repo: string;
  number: number;
  onBack?: () => void;
}) {
  const q = useQuery(pullRequestQuery(host, repo, number));
  const [tab, setTab] = useState<Tab>('description');
  if (q.isPending) return <Loading label="Loading the pull request" />;
  if (q.error)
    return (
      <div className="p-6">
        <ErrorText error={q.error} />
      </div>
    );
  const d = q.data;
  const p = d.pullRequest;
  const tabs: { id: Tab; label: string; count?: number }[] = [
    { id: 'description', label: 'Description' },
    { id: 'checks', label: 'Checks', count: d.checks.length },
    { id: 'comments', label: 'Comments', count: d.comments.length },
    { id: 'files', label: 'Files', count: d.files.length },
  ];
  return (
    <div className="flex min-h-0 flex-1 flex-col" data-testid="pull-request">
      <header className="grid gap-2 border-b px-4 py-3">
        {onBack && (
          <button
            type="button"
            onClick={onBack}
            className="w-fit text-xs text-muted-foreground hover:text-foreground md:hidden"
          >
            ← All pull requests
          </button>
        )}
        <div className="flex min-w-0 items-start gap-2">
          <h1 className="min-w-0 flex-1 text-base font-semibold">
            {p.title}{' '}
            <span className="font-normal text-muted-foreground">
              #{p.number}
            </span>
          </h1>
          <a
            href={p.url}
            target="_blank"
            rel="noreferrer"
            aria-label="Open on the host"
            title="Open on the host"
            className="shrink-0 rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <ExternalLink className="size-4" aria-hidden />
          </a>
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <PullRequestStateBadge state={p.state} />
          <ChecksIndicator state={d.checksState} />
          <span>{p.repo}</span>
          <span aria-hidden>·</span>
          <span>{p.author}</span>
          {p.headRef && (
            <span className="flex min-w-0 items-center gap-1 font-mono">
              <GitBranch className="size-3 shrink-0" aria-hidden />
              <span className="truncate">{p.headRef}</span>→
              <span className="truncate">{p.baseRef}</span>
            </span>
          )}
          <DiffStat additions={d.additions} deletions={d.deletions} />
          {d.conflicted && (
            <span className="text-red-600 dark:text-red-400">
              Merge conflicts
            </span>
          )}
        </div>
        <Actions detail={d} />
      </header>
      <div
        role="tablist"
        aria-label="Pull request"
        className="flex gap-1 border-b px-3"
      >
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => setTab(t.id)}
            className={cn(
              '-mb-px border-b-2 px-2 py-2 text-xs font-medium',
              tab === t.id
                ? 'border-foreground text-foreground'
                : 'border-transparent text-muted-foreground hover:text-foreground',
            )}
          >
            {t.label}
            {t.count !== undefined && t.count > 0 && (
              <span className="ml-1 text-muted-foreground">{t.count}</span>
            )}
          </button>
        ))}
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-4">
        {tab === 'description' &&
          (d.body.trim() ? (
            <div className="prose-sm max-w-none text-sm">
              <Suspense
                fallback={<p className="whitespace-pre-wrap">{d.body}</p>}
              >
                <Markdown text={d.body} />
              </Suspense>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">No description.</p>
          ))}
        {tab === 'checks' && <Checks detail={d} />}
        {tab === 'comments' && (
          <Comments
            host={host}
            repo={repo}
            number={number}
            comments={d.comments}
          />
        )}
        {tab === 'files' && <Files files={d.files} />}
      </div>
    </div>
  );
}

function Checks({ detail: d }: { detail: PullRequestDetail }) {
  if (d.checks.length === 0)
    return (
      <p className="text-sm text-muted-foreground">No checks on the head.</p>
    );
  return (
    <ul aria-label="Checks" className="grid gap-1 text-sm">
      {d.checks.map((c, i) => {
        const failed = [
          'failure',
          'timed_out',
          'cancelled',
          'action_required',
          'error',
        ].includes(c.conclusion ?? '');
        const state =
          c.status !== 'completed' ? 'pending' : failed ? 'failure' : 'success';
        return (
          <li
            key={`${c.name}-${i}`}
            className="flex items-center gap-2 rounded-md px-1 py-0.5"
          >
            <ChecksIndicator state={state} />
            <span className="min-w-0 flex-1 truncate">{c.name}</span>
            <span className="shrink-0 text-xs text-muted-foreground">
              {c.conclusion || c.status.replace('_', ' ')}
            </span>
            {c.url && (
              <a
                href={c.url}
                target="_blank"
                rel="noreferrer"
                aria-label={`Open ${c.name}`}
                className="shrink-0 text-muted-foreground hover:text-foreground"
              >
                <ExternalLink className="size-3.5" aria-hidden />
              </a>
            )}
          </li>
        );
      })}
    </ul>
  );
}

function Comments({
  host,
  repo,
  number,
  comments,
}: {
  host: string;
  repo: string;
  number: number;
  comments: PullRequestComment[];
}) {
  const qc = useQueryClient();
  const now = useNow(60_000);
  const [body, setBody] = useState('');
  const send = useMutation({
    mutationFn: (event: 'comment' | 'approve' | 'request_changes') => {
      const [owner = '', name = ''] = repo.split('/');
      return unwrap(
        api.POST('/pull-requests/{owner}/{repo}/{number}/comments', {
          params: { path: { owner, repo: name, number }, query: { host } },
          body: { body, event },
        }),
      );
    },
    onSuccess: async () => {
      setBody('');
      await qc.invalidateQueries({
        queryKey: ['pull-requests', host, repo, number],
      });
    },
  });
  return (
    <div className="grid gap-4">
      {comments.length === 0 && (
        <p className="text-sm text-muted-foreground">No comments yet.</p>
      )}
      <ul aria-label="Comments" className="grid gap-3">
        {comments.map((c) => (
          <li key={`${c.kind}-${c.id}`} className="rounded-lg border">
            <div className="flex items-center gap-2 border-b bg-muted/40 px-3 py-1.5 text-xs">
              <span className="font-medium">{c.author}</span>
              {c.kind === 'review' && c.state && (
                <span className="text-muted-foreground">
                  {c.state.toLowerCase().replace('_', ' ')}
                </span>
              )}
              {c.path && (
                <span className="truncate font-mono text-muted-foreground">
                  {c.path}
                  {c.line ? `:${c.line}` : ''}
                </span>
              )}
              <span className="ml-auto shrink-0 text-muted-foreground">
                {relTime(c.createdAt, now)}
              </span>
            </div>
            <p className="px-3 py-2 text-sm whitespace-pre-wrap">
              {c.body || (
                <span className="text-muted-foreground">(no text)</span>
              )}
            </p>
          </li>
        ))}
      </ul>
      <div className="grid gap-2">
        <textarea
          aria-label="New comment"
          placeholder="Leave a comment"
          value={body}
          onChange={(e) => setBody(e.target.value)}
          rows={3}
          className="rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-xs focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        />
        <div className="flex flex-wrap justify-end gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={send.isPending || !body.trim()}
            onClick={() => send.mutate('request_changes')}
          >
            Request changes
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={send.isPending}
            onClick={() => send.mutate('approve')}
          >
            Approve
          </Button>
          <Button
            size="sm"
            disabled={send.isPending || !body.trim()}
            onClick={() => send.mutate('comment')}
          >
            {send.isPending && <Loader2 className="size-3.5 animate-spin" />}
            Comment
          </Button>
        </div>
        <ErrorText error={send.error} />
      </div>
    </div>
  );
}

const ROW = {
  add: 'bg-emerald-500/10',
  del: 'bg-red-500/10',
  context: '',
} as const;
const MARK = { add: '+', del: '-', context: ' ' } as const;

function Files({ files }: { files: PullRequestFile[] }) {
  const [closed, setClosed] = useState<Set<string>>(() => new Set());
  if (files.length === 0)
    return <p className="text-sm text-muted-foreground">No changed files.</p>;
  return (
    <ul aria-label="Changed files" className="grid gap-2">
      {files.map((f) => {
        const open = !closed.has(f.path);
        const rows = parsePatch(f.patch);
        return (
          <li
            key={f.path}
            data-testid="pr-file"
            className="overflow-hidden rounded-lg border"
          >
            <button
              type="button"
              aria-expanded={open}
              onClick={() =>
                setClosed((s) => {
                  const n = new Set(s);
                  if (n.has(f.path)) n.delete(f.path);
                  else n.add(f.path);
                  return n;
                })
              }
              className="flex w-full items-center gap-2 bg-muted/40 px-2 py-1.5 text-left text-xs"
            >
              {open ? (
                <ChevronDown className="size-3.5 shrink-0" aria-hidden />
              ) : (
                <ChevronRight className="size-3.5 shrink-0" aria-hidden />
              )}
              <span className="w-3 shrink-0 font-mono font-semibold">
                {f.status}
              </span>
              <span className="min-w-0 flex-1 truncate font-mono">
                {f.previousPath && f.previousPath !== f.path && (
                  <span className="text-muted-foreground">
                    {f.previousPath} →{' '}
                  </span>
                )}
                {f.path}
              </span>
              <DiffStat additions={f.additions} deletions={f.deletions} />
            </button>
            {open &&
              (rows.length === 0 ? (
                <p className="px-3 py-2 text-xs text-muted-foreground">
                  {f.patch === undefined
                    ? 'No diff shown (binary or too large).'
                    : 'No changes in the text.'}
                </p>
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full border-collapse font-mono text-[12px] leading-5">
                    <tbody>
                      {rows.map((r, i) =>
                        r.kind === 'hunk' ? (
                          <tr key={i} className="bg-sky-500/5">
                            <td
                              colSpan={3}
                              className="px-2 text-muted-foreground"
                            >
                              {r.text}
                            </td>
                          </tr>
                        ) : (
                          <tr key={i} className={ROW[r.kind]}>
                            <td className="w-px px-1.5 text-right text-muted-foreground/70 select-none">
                              {r.oldNo ?? ''}
                            </td>
                            <td className="w-px px-1.5 text-right text-muted-foreground/70 select-none">
                              {r.newNo ?? ''}
                            </td>
                            <td className="px-2 whitespace-pre">
                              {MARK[r.kind]}
                              {r.text}
                            </td>
                          </tr>
                        ),
                      )}
                    </tbody>
                  </table>
                </div>
              ))}
          </li>
        );
      })}
    </ul>
  );
}

/** Merge (with a method) and Work on PR (start a thread on its branch). */
function Actions({ detail: d }: { detail: PullRequestDetail }) {
  const p = d.pullRequest;
  const qc = useQueryClient();
  const navigate = useNavigate();
  const agents = useQuery(agentsQuery);
  const [method, setMethod] = useState<'merge' | 'squash' | 'rebase'>('squash');
  const usable = (agents.data ?? []).filter((a) => !a.isTemplate);
  const [agentId, setAgentId] = useState('');
  const chosen = agentId || usable[0]?.id || '';
  const [owner = '', name = ''] = p.repo.split('/');
  const path = { owner, repo: name, number: p.number };
  const merge = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/pull-requests/{owner}/{repo}/{number}/merge', {
          params: { path, query: { host: p.host } },
          body: { method, headSha: p.headSha ?? '' },
        }),
      ),
    onSettled: () => qc.invalidateQueries({ queryKey: ['pull-requests'] }),
  });
  const work = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/pull-requests/{owner}/{repo}/{number}/work', {
          params: { path, query: { host: p.host } },
          body: { agentId: chosen },
        }),
      ),
    onSuccess: (r) =>
      navigate({
        to: '/a/$agentId',
        params: { agentId: r.session.agentId },
        search: { s: r.session.id },
      }),
  });
  const open = p.state === 'open' || p.state === 'draft';
  if (!open) return null;
  return (
    <div className="grid gap-1.5">
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex items-center">
          <Select
            aria-label="Merge method"
            value={method}
            onChange={(e) => setMethod(e.target.value as typeof method)}
            className="h-8 rounded-r-none border-r-0 text-xs"
          >
            <option value="merge">Merge commit</option>
            <option value="squash">Squash and merge</option>
            <option value="rebase">Rebase and merge</option>
          </Select>
          <Button
            size="sm"
            className="rounded-l-none"
            disabled={merge.isPending || p.state === 'draft' || d.conflicted}
            title={
              p.state === 'draft'
                ? 'Drafts cannot be merged'
                : d.conflicted
                  ? 'Resolve the conflicts first'
                  : undefined
            }
            onClick={() => merge.mutate()}
          >
            {merge.isPending && <Loader2 className="size-3.5 animate-spin" />}
            Merge
          </Button>
        </div>
        <div className="flex items-center">
          {usable.length > 1 && (
            <Select
              aria-label="Agent"
              value={chosen}
              onChange={(e) => setAgentId(e.target.value)}
              className="h-8 rounded-r-none border-r-0 text-xs"
            >
              {usable.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </Select>
          )}
          <Button
            size="sm"
            variant="outline"
            className={cn(usable.length > 1 && 'rounded-l-none')}
            disabled={work.isPending || !chosen}
            title={
              chosen
                ? 'Start a thread on this branch, in its own worktree'
                : 'Create an agent first'
            }
            onClick={() => work.mutate()}
          >
            {work.isPending && <Loader2 className="size-3.5 animate-spin" />}
            Work on PR
          </Button>
        </div>
      </div>
      {merge.data?.merged && (
        <p role="status" className="text-xs text-emerald-600">
          Merged.
        </p>
      )}
      <ErrorText error={merge.error ?? work.error} />
    </div>
  );
}
