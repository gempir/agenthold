import {
  Fragment,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Check,
  ChevronDown,
  ChevronRight,
  ChevronsDownUp,
  ChevronsUpDown,
  Columns2,
  FileCode2,
  GitCommitHorizontal,
  MessageSquare,
  Rows2,
  Search,
  Send,
  X,
} from 'lucide-react';
import { api, ApiError, unwrap } from '@/api/client';
import {
  agentGitReposQuery,
  gitCommitsQuery,
  gitDiffQuery,
  sessionReviewCommentsQuery,
  type GitDiff,
  type GitDiffFile,
  type SessionReviewComment,
} from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { readPref, writePref } from '@/components/layout/shell';
import {
  AttachedNote,
  CommentBox,
  nextSelection,
  selectionBounds,
  type LineSelection,
} from '@/components/workspace/LineComment';
import {
  attachToComposer,
  newContextId,
  type ReviewComment,
} from '@/lib/composerContext';
import { joinRel } from '@/lib/files';
import { parsePatch, splitRows, type PatchRow } from '@/lib/patch';
import { cn } from '@/lib/utils';

export type DiffSource = 'uncommitted' | 'branch' | 'commits';
export type DiffLayout = 'unified' | 'split';

const SOURCES: { id: DiffSource; label: string }[] = [
  { id: 'uncommitted', label: 'Uncommitted' },
  { id: 'branch', label: 'Branch' },
  { id: 'commits', label: 'Commits' },
];

const STATUS: Record<GitDiffFile['status'], { label: string; cls: string }> = {
  A: { label: 'Added', cls: 'text-emerald-600 dark:text-emerald-400' },
  M: { label: 'Modified', cls: 'text-amber-600 dark:text-amber-400' },
  D: { label: 'Deleted', cls: 'text-red-600 dark:text-red-400' },
  R: { label: 'Renamed', cls: 'text-sky-600 dark:text-sky-400' },
  '?': { label: 'Untracked', cls: 'text-muted-foreground' },
};

/** "+12 −3" */
export function DiffStat({
  additions,
  deletions,
  className,
}: {
  additions: number;
  deletions: number;
  className?: string;
}) {
  return (
    <span className={cn('shrink-0 font-mono tabular-nums', className)}>
      <span className="text-emerald-600 dark:text-emerald-400">
        +{additions}
      </span>{' '}
      <span className="text-red-600 dark:text-red-400">−{deletions}</span>
    </span>
  );
}

/** The repository a directory is in, among the agent's repositories. */
export function repoFor<T extends { path: string }>(
  repos: T[],
  dir: string | undefined,
): T | undefined {
  if (dir === undefined) return undefined;
  return repos
    .filter((r) => dir === r.path || dir.startsWith(`${r.path}/`))
    .sort((a, b) => b.path.length - a.path.length)[0];
}

type Match = { file: string; row: number };

/**
 * The changes of a repository in the agent: uncommitted work, the branch
 * against its base, or one commit. Lines can be commented on for the
 * composer, files opened in the viewer, and uncommitted work committed.
 */
export function DiffPane({
  agentId,
  canUse,
  repoPath,
  composerKey,
  sessionId,
  onOpenFile,
  onStart,
  starting,
}: {
  agentId: string;
  canUse: boolean;
  /** The directory (relative to home) to show the repository of. */
  repoPath?: string;
  /** Draft key of the composer review comments go to. */
  composerKey?: string;
  /** Session ID for persisted collaborative diff review comments. */
  sessionId?: string;
  onOpenFile?: (path: string, line?: number) => void;
  onStart?: () => void;
  starting?: boolean;
}) {
  const qc = useQueryClient();
  const activeSessionId =
    sessionId ??
    (composerKey && !composerKey.startsWith('new:') ? composerKey : undefined);
  useEffect(() => {
    globalThis.dispatchEvent?.(
      new CustomEvent('agenthold:presence-activity', {
        detail: { activity: 'viewing_diff' },
      }),
    );
    return () => {
      globalThis.dispatchEvent?.(
        new CustomEvent('agenthold:presence-activity', {
          detail: { activity: 'idle' },
        }),
      );
    };
  }, []);
  const reviewCommentsQ = useQuery({
    ...sessionReviewCommentsQuery(activeSessionId ?? ''),
    enabled: Boolean(activeSessionId),
  });
  const openReviewComments = useMemo(
    () =>
      Array.isArray(reviewCommentsQ.data)
        ? reviewCommentsQ.data.filter((c) => !c.resolvedAt)
        : [],
    [reviewCommentsQ.data],
  );
  const createReviewComment = useMutation({
    mutationFn: (input: {
      filePath: string;
      startLine: number;
      endLine: number;
      side: 'new' | 'old';
      body: string;
    }) =>
      unwrap(
        api.POST('/sessions/{sessionId}/review-comments', {
          params: { path: { sessionId: activeSessionId! } },
          body: input,
        }),
      ),
    onSuccess: () => {
      if (activeSessionId) {
        void qc.invalidateQueries({
          queryKey: sessionReviewCommentsQuery(activeSessionId).queryKey,
        });
      }
    },
  });
  const resolveReviewComment = useMutation({
    mutationFn: (commentId: string) =>
      unwrap(
        api.POST('/sessions/{sessionId}/review-comments/{commentId}/resolve', {
          params: { path: { sessionId: activeSessionId!, commentId } },
        }),
      ),
    onSuccess: () => {
      if (activeSessionId) {
        void qc.invalidateQueries({
          queryKey: sessionReviewCommentsQuery(activeSessionId).queryKey,
        });
      }
    },
  });
  const [sentAllNote, setSentAllNote] = useState(false);
  const sendAllToComposer = () => {
    if (!composerKey || openReviewComments.length === 0) return;
    for (const c of openReviewComments) {
      attachToComposer(composerKey, {
        kind: 'review',
        id: `rc-${c.id}`,
        path: c.filePath,
        startLine: c.startLine,
        endLine: c.endLine,
        side: c.side,
        source: `review by ${c.userName || 'teammate'}`,
        excerpt: '',
        body: c.body,
      });
    }
    setSentAllNote(true);
    setTimeout(() => setSentAllNote(false), 2500);
  };
  const [source, setSource] = useState<DiffSource>('uncommitted');
  const [repoChoice, setRepoChoice] = useState<string>();
  const [sha, setSha] = useState<string>();
  const [layout, setLayoutState] = useState<DiffLayout>(() =>
    readPref('diff.layout', 'unified') === 'split' ? 'split' : 'unified',
  );
  const setLayout = (l: DiffLayout) => {
    setLayoutState(l);
    writePref('diff.layout', l);
  };
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());
  const [findOpen, setFindOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [matchIndex, setMatchIndex] = useState(0);
  const findInput = useRef<HTMLInputElement>(null);
  const root = useRef<HTMLDivElement>(null);

  const repos = useQuery(agentGitReposQuery(agentId));
  const repoList = repos.data?.repos ?? [];
  // The repository root the thread's directory is in, once they are known.
  const defaultRepo = repoFor(repoList, repoPath)?.path;
  const path = repoChoice ?? defaultRepo ?? repoPath ?? repoList[0]?.path ?? '';
  const selectedRepo = repoChoice ?? defaultRepo ?? repoList[0]?.path;
  const ready = !repos.isPending;

  const commits = useQuery({
    ...gitCommitsQuery(agentId, path),
    enabled: ready && source === 'commits',
  });
  const ref =
    source === 'commits' ? (sha ?? commits.data?.commits[0]?.sha) : undefined;
  const mode = source === 'commits' ? 'commit' : source;
  const diff = useQuery({
    ...gitDiffQuery(agentId, path, mode, ref),
    enabled: ready && (source !== 'commits' || !!ref),
  });

  const parsed = useMemo(
    () =>
      (diff.data?.files ?? []).map((f) => ({
        file: f,
        rows: parsePatch(f.patch),
      })),
    [diff.data],
  );

  const q = query.trim().toLowerCase();
  const matches = useMemo<Match[]>(() => {
    if (!q) return [];
    const out: Match[] = [];
    for (const { file, rows } of parsed) {
      if (file.path.toLowerCase().includes(q))
        out.push({ file: file.path, row: -1 });
      rows.forEach((r, i) => {
        if (r.kind !== 'hunk' && r.text.toLowerCase().includes(q))
          out.push({ file: file.path, row: i });
      });
    }
    return out;
  }, [parsed, q]);
  const current = matches.length
    ? matches[((matchIndex % matches.length) + matches.length) % matches.length]
    : undefined;
  const matchFiles = useMemo(
    () => new Set(matches.map((m) => m.file)),
    [matches],
  );

  useEffect(() => {
    if (!current) return;
    root.current
      ?.querySelector('[data-find-current]')
      ?.scrollIntoView?.({ block: 'center' });
  }, [current]);

  const isOpen = (p: string) => !collapsed.has(p) || matchFiles.has(p);
  const allPaths = parsed.map((p) => p.file.path);

  const openFind = () => {
    setFindOpen(true);
    requestAnimationFrame(() => findInput.current?.select());
  };

  const sourceLabel =
    source === 'uncommitted'
      ? 'uncommitted diff'
      : source === 'branch'
        ? `branch diff${diff.data?.base ? ` vs ${diff.data.base}` : ''}`
        : `commit ${ref?.slice(0, 7) ?? ''}`;

  const notRunning =
    diff.error instanceof ApiError && diff.error.status === 409;

  return (
    <div
      ref={root}
      tabIndex={-1}
      data-testid="diff-pane"
      onKeyDown={(e) => {
        if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'f') {
          e.preventDefault();
          openFind();
        }
      }}
      className="flex min-h-0 flex-1 flex-col outline-none"
    >
      <div className="grid shrink-0 gap-2 border-b px-3 py-2">
        <div className="flex flex-wrap items-center gap-1.5">
          {repoList.length > 1 && (
            <Select
              aria-label="Repository"
              value={selectedRepo}
              onChange={(e) => {
                setRepoChoice(e.target.value);
                setSha(undefined);
              }}
              className="h-7 max-w-40 text-xs"
            >
              {repoList.map((r) => (
                <option key={r.path} value={r.path}>
                  {r.project || r.path}
                  {r.isWorktree ? ` (${r.branch})` : ''}
                </option>
              ))}
            </Select>
          )}
          <div
            role="radiogroup"
            aria-label="Diff source"
            className="flex rounded-lg border p-0.5"
          >
            {SOURCES.map((s) => (
              <button
                key={s.id}
                type="button"
                role="radio"
                aria-checked={source === s.id}
                onClick={() => setSource(s.id)}
                className={cn(
                  'rounded-md px-2 py-0.5 text-xs',
                  source === s.id
                    ? 'bg-accent font-medium text-foreground'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                {s.label}
              </button>
            ))}
          </div>
          <div className="ml-auto flex items-center gap-0.5">
            <ToolButton label="Find in diff" onClick={openFind}>
              <Search className="size-3.5" />
            </ToolButton>
            <ToolButton
              label={layout === 'split' ? 'Unified view' : 'Split view'}
              pressed={layout === 'split'}
              onClick={() =>
                setLayout(layout === 'split' ? 'unified' : 'split')
              }
            >
              {layout === 'split' ? (
                <Rows2 className="size-3.5" />
              ) : (
                <Columns2 className="size-3.5" />
              )}
            </ToolButton>
            <ToolButton
              label="Expand all"
              onClick={() => setCollapsed(new Set())}
            >
              <ChevronsUpDown className="size-3.5" />
            </ToolButton>
            <ToolButton
              label="Collapse all"
              onClick={() => setCollapsed(new Set(allPaths))}
            >
              <ChevronsDownUp className="size-3.5" />
            </ToolButton>
          </div>
        </div>
        {source === 'commits' && commits.data && (
          <Select
            aria-label="Commit"
            value={ref ?? ''}
            onChange={(e) => setSha(e.target.value)}
            className="h-7 w-full text-xs"
          >
            {commits.data.commits.length === 0 && (
              <option value="">No commits</option>
            )}
            {commits.data.commits.map((c) => (
              <option key={c.sha} value={c.sha}>
                {c.shortSha} {c.subject}
              </option>
            ))}
          </Select>
        )}
        {findOpen && (
          <div className="flex items-center gap-1.5">
            <Input
              ref={findInput}
              autoFocus
              aria-label="Find in diff"
              placeholder="Find in diff"
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setMatchIndex(0);
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  setMatchIndex((i) => i + (e.shiftKey ? -1 : 1));
                } else if (e.key === 'Escape') {
                  e.preventDefault();
                  setFindOpen(false);
                  setQuery('');
                  root.current?.focus();
                }
              }}
              className="h-7 text-xs"
            />
            <span
              aria-live="polite"
              className="shrink-0 text-xs text-muted-foreground tabular-nums"
            >
              {q
                ? matches.length
                  ? `${(((matchIndex % matches.length) + matches.length) % matches.length) + 1}/${matches.length}`
                  : 'No matches'
                : ''}
            </span>
            <ToolButton
              label="Close find"
              onClick={() => {
                setFindOpen(false);
                setQuery('');
              }}
            >
              <X className="size-3.5" />
            </ToolButton>
          </div>
        )}
        {diff.data && <Summary diff={diff.data} />}
      </div>

      {canUse &&
        source === 'uncommitted' &&
        diff.data &&
        diff.data.files.length > 0 && (
          <CommitBar agentId={agentId} path={diff.data.repo.path || path} />
        )}

      {openReviewComments.length > 0 && (
        <SharedReviewCommentsBar
          comments={openReviewComments}
          canSend={Boolean(composerKey)}
          sent={sentAllNote}
          resolving={resolveReviewComment.isPending}
          onResolve={(id) => resolveReviewComment.mutate(id)}
          onSendAll={sendAllToComposer}
        />
      )}

      <div className="min-h-0 flex-1 overflow-auto">
        {(!ready ||
          diff.isLoading ||
          (source === 'commits' && commits.isLoading)) && <Loading />}
        {notRunning ? (
          <div className="grid justify-items-start gap-3 p-4">
            <p role="alert" className="text-sm">
              {(diff.error as Error).message}
            </p>
            {canUse && onStart && (
              <Button size="sm" disabled={starting} onClick={onStart}>
                Start
              </Button>
            )}
          </div>
        ) : (
          <div className="p-3">
            <ErrorText error={diff.error ?? commits.error} />
          </div>
        )}
        {diff.data && diff.data.files.length === 0 && (
          <p className="p-6 text-center text-sm text-muted-foreground">
            {source === 'uncommitted'
              ? 'No uncommitted changes.'
              : source === 'branch'
                ? 'No changes on this branch.'
                : 'This commit changes no files.'}
          </p>
        )}
        {diff.data && (
          <ul aria-label="Changed files" className="grid gap-2 p-2">
            {parsed.map(({ file, rows }) => (
              <DiffFile
                key={file.path}
                file={file}
                rows={rows}
                repoPath={diff.data.repo.path}
                open={isOpen(file.path)}
                onToggle={() =>
                  setCollapsed((s) => {
                    const n = new Set(s);
                    if (isOpen(file.path)) n.add(file.path);
                    else n.delete(file.path);
                    return n;
                  })
                }
                layout={layout}
                query={q}
                currentRow={
                  current?.file === file.path ? current.row : undefined
                }
                composerKey={composerKey}
                source={sourceLabel}
                onPersistComment={
                  activeSessionId
                    ? (c) => createReviewComment.mutate(c)
                    : undefined
                }
                onOpenFile={onOpenFile}
              />
            ))}
          </ul>
        )}
        {diff.data?.truncated && (
          <p className="px-3 pb-3 text-xs text-muted-foreground">
            More files changed than are shown.
          </p>
        )}
      </div>
    </div>
  );
}

function ToolButton({
  label,
  pressed,
  onClick,
  children,
}: {
  label: string;
  pressed?: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <Button
      variant="ghost"
      size="icon-sm"
      className={cn(
        'size-7 text-muted-foreground',
        pressed && 'bg-accent text-foreground',
      )}
      aria-label={label}
      title={label}
      {...(pressed !== undefined && { 'aria-pressed': pressed })}
      onClick={onClick}
    >
      {children}
    </Button>
  );
}

function Summary({ diff }: { diff: GitDiff }) {
  const n = diff.files.length;
  return (
    <p className="flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
      <span>
        {n} {n === 1 ? 'file' : 'files'} changed
      </span>
      <DiffStat additions={diff.additions} deletions={diff.deletions} />
      {diff.repo.branch && (
        <span className="truncate font-mono">
          {diff.repo.branch}
          {diff.mode === 'branch' && diff.base ? ` ← ${diff.base}` : ''}
        </span>
      )}
    </p>
  );
}

function SharedReviewCommentsBar({
  comments,
  canSend,
  sent,
  resolving,
  onResolve,
  onSendAll,
}: {
  comments: SessionReviewComment[];
  canSend: boolean;
  sent: boolean;
  resolving: boolean;
  onResolve: (id: string) => void;
  onSendAll: () => void;
}) {
  return (
    <section
      data-testid="shared-review-comments"
      aria-label="Shared review comments"
      className="grid shrink-0 gap-2 border-b bg-muted/20 px-3 py-2 text-xs"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex items-center gap-1.5 font-medium text-foreground">
          <MessageSquare className="size-3.5" aria-hidden />
          Open review comments ({comments.length})
        </span>
        {canSend && (
          <Button
            type="button"
            size="sm"
            variant="secondary"
            className="h-6 px-2 text-xs"
            onClick={onSendAll}
          >
            <Send className="size-3" aria-hidden />
            {sent ? 'Added to composer' : 'Send all to composer'}
          </Button>
        )}
      </div>
      <ul className="grid max-h-36 gap-1.5 overflow-y-auto">
        {comments.map((c) => (
          <li
            key={c.id}
            className="flex items-start justify-between gap-2 rounded-md border bg-background px-2 py-1.5"
          >
            <div className="min-w-0 flex-1">
              <p className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
                <span className="font-medium text-foreground">
                  {c.userName || 'Teammate'}
                </span>
                <span className="truncate font-mono">
                  {c.filePath}:
                  {c.startLine === c.endLine
                    ? c.startLine
                    : `${c.startLine}-${c.endLine}`}
                </span>
              </p>
              <p className="mt-0.5 whitespace-pre-wrap text-foreground">
                {c.body}
              </p>
            </div>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="h-6 shrink-0 px-2 text-xs text-muted-foreground"
              disabled={resolving}
              onClick={() => onResolve(c.id)}
            >
              <Check className="size-3" aria-hidden />
              Resolve
            </Button>
          </li>
        ))}
      </ul>
    </section>
  );
}

function CommitBar({ agentId, path }: { agentId: string; path: string }) {
  const qc = useQueryClient();
  const [message, setMessage] = useState('');
  const commit = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/agents/{agentId}/git/commit', {
          params: { path: { agentId } },
          body: { path, message: message.trim(), all: true },
        }),
      ),
    onSuccess: async () => {
      setMessage('');
      await qc.invalidateQueries({ queryKey: ['agents', agentId, 'git'] });
    },
  });
  return (
    <form
      aria-label="Commit changes"
      onSubmit={(e) => {
        e.preventDefault();
        if (message.trim()) commit.mutate();
      }}
      className="grid shrink-0 gap-1.5 border-b px-3 py-2"
    >
      <div className="flex gap-1.5">
        <Input
          aria-label="Commit message"
          placeholder="Commit message"
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          className="h-7 text-xs"
        />
        <Button
          type="submit"
          size="sm"
          className="h-7"
          disabled={!message.trim() || commit.isPending}
        >
          <GitCommitHorizontal className="size-3.5" aria-hidden />
          Commit
        </Button>
      </div>
      <ErrorText error={commit.error} />
      {commit.data && (
        <p role="status" className="text-xs text-muted-foreground">
          Committed{' '}
          <span className="font-mono">{commit.data.commit.shortSha}</span>{' '}
          {commit.data.commit.subject}
        </p>
      )}
    </form>
  );
}

function rangeComment(
  rows: PatchRow[],
  [from, to]: readonly [number, number],
): Pick<ReviewComment, 'startLine' | 'endLine' | 'side' | 'excerpt'> {
  const sel = rows.slice(from, to + 1).filter((r) => r.kind !== 'hunk');
  const side = sel.every((r) => r.kind === 'del') ? 'old' : 'new';
  const nums = sel
    .map((r) => (side === 'old' ? r.oldNo : r.newNo))
    .filter((n): n is number => n !== undefined);
  const mark = { add: '+', del: '-', context: ' ' } as const;
  return {
    side,
    startLine: nums[0] ?? 0,
    endLine: nums[nums.length - 1] ?? 0,
    excerpt: sel.map((r) => mark[r.kind] + r.text).join('\n'),
  };
}

const ROW_BG = {
  add: 'bg-emerald-500/10',
  del: 'bg-red-500/10',
  context: '',
} as const;

function DiffFile({
  file,
  rows,
  repoPath,
  open,
  onToggle,
  layout,
  query,
  currentRow,
  composerKey,
  source,
  onPersistComment,
  onOpenFile,
}: {
  file: GitDiffFile;
  rows: PatchRow[];
  repoPath: string;
  open: boolean;
  onToggle: () => void;
  layout: DiffLayout;
  query: string;
  currentRow?: number;
  composerKey?: string;
  source: string;
  onPersistComment?: (c: {
    filePath: string;
    startLine: number;
    endLine: number;
    side: 'new' | 'old';
    body: string;
  }) => void;
  onOpenFile?: (path: string, line?: number) => void;
}) {
  const [sel, setSel] = useState<LineSelection | null>(null);
  const [commenting, setCommenting] = useState(false);
  const [attached, setAttached] = useState<boolean | null>(null);
  const st = STATUS[file.status];
  const fullPath = joinRel(repoPath, file.path);
  const bounds = sel ? selectionBounds(sel) : null;
  const canComment = !!composerKey || !!onPersistComment;

  const pick = (i: number, shift: boolean) => {
    if (!canComment) return;
    setSel((s) => nextSelection(s, i, shift));
    setCommenting(true);
    setAttached(null);
  };
  const selected = (i: number | undefined) =>
    i !== undefined && !!bounds && i >= bounds[0] && i <= bounds[1];
  const isMatch = (r: PatchRow | undefined) =>
    !!query && !!r && r.kind !== 'hunk' && r.text.toLowerCase().includes(query);

  const box =
    commenting && bounds && canComment ? (
      <CommentBox
        label={(() => {
          const c = rangeComment(rows, bounds);
          return c.startLine === c.endLine
            ? `Line ${c.startLine}`
            : `Lines ${c.startLine}-${c.endLine}`;
        })()}
        onCancel={() => {
          setCommenting(false);
          setSel(null);
        }}
        onSubmit={(body) => {
          const rc = rangeComment(rows, bounds);
          if (composerKey) {
            const rich = attachToComposer(composerKey, {
              kind: 'review',
              id: newContextId(),
              path: fullPath,
              source,
              body,
              ...rc,
            });
            setAttached(rich);
          }
          if (onPersistComment && rc.startLine > 0) {
            onPersistComment({
              filePath: fullPath,
              startLine: rc.startLine,
              endLine: Math.max(rc.startLine, rc.endLine),
              side: rc.side === 'old' ? 'old' : 'new',
              body,
            });
          }
          setCommenting(false);
          setSel(null);
        }}
      />
    ) : null;

  const firstChange = rows.find((r) => r.kind === 'add');
  const numCell = (
    n: number | undefined,
    i: number | undefined,
    side: 'old' | 'new',
  ) => (
    <td className="w-px border-r border-border/50 px-0 align-top select-none">
      {n !== undefined && i !== undefined ? (
        <button
          type="button"
          aria-label={`${side === 'old' ? 'Old' : 'New'} line ${n}`}
          disabled={!canComment}
          onClick={(e) => pick(i, e.shiftKey)}
          className="block w-full min-w-9 px-1.5 text-right text-muted-foreground/70 hover:text-foreground disabled:cursor-default disabled:hover:text-muted-foreground/70"
        >
          {n}
        </button>
      ) : null}
    </td>
  );

  return (
    <li
      data-testid="diff-file"
      className={cn(
        'overflow-hidden rounded-lg border',
        currentRow === -1 && 'ring-2 ring-amber-400',
      )}
      {...(currentRow === -1 && { 'data-find-current': '' })}
    >
      <div className="flex items-center gap-2 bg-muted/40 px-2 py-1.5 text-xs">
        <button
          type="button"
          aria-expanded={open}
          aria-label={`${open ? 'Collapse' : 'Expand'} ${file.path}`}
          onClick={onToggle}
          className="flex min-w-0 flex-1 items-center gap-1.5 text-left"
        >
          {open ? (
            <ChevronDown className="size-3.5 shrink-0" aria-hidden />
          ) : (
            <ChevronRight className="size-3.5 shrink-0" aria-hidden />
          )}
          <span
            title={st.label}
            aria-label={st.label}
            className={cn('w-3 shrink-0 font-mono font-semibold', st.cls)}
          >
            {file.status}
          </span>
          <span className="min-w-0 truncate font-mono" title={file.path}>
            {file.oldPath && file.oldPath !== file.path && (
              <span className="text-muted-foreground">{file.oldPath} → </span>
            )}
            {file.path}
          </span>
        </button>
        <DiffStat additions={file.additions} deletions={file.deletions} />
        {onOpenFile && file.status !== 'D' && (
          <Button
            variant="ghost"
            size="icon-sm"
            className="size-6 text-muted-foreground"
            aria-label={`Open ${file.path}`}
            title="Open file"
            onClick={() =>
              onOpenFile(
                fullPath,
                firstChange?.kind === 'add' ? firstChange.newNo : undefined,
              )
            }
          >
            <FileCode2 className="size-3.5" />
          </Button>
        )}
      </div>
      {attached !== null && (
        <div className="border-t px-3 py-1">
          <AttachedNote rich={attached} />
        </div>
      )}
      {open &&
        (file.binary ? (
          <p className="border-t px-3 py-2 text-xs text-muted-foreground">
            Binary file
          </p>
        ) : file.truncated || (!file.patch && rows.length === 0) ? (
          <p className="border-t px-3 py-2 text-xs text-muted-foreground">
            {file.truncated
              ? 'The diff is too large to show.'
              : 'No changes to show.'}
          </p>
        ) : (
          <div className="overflow-x-auto border-t">
            <table className="w-full border-collapse font-mono text-xs leading-5">
              <tbody>
                {layout === 'unified'
                  ? rows.map((r, i) => (
                      <Fragment key={i}>
                        {r.kind === 'hunk' ? (
                          <tr className="bg-sky-500/5 text-muted-foreground">
                            <td colSpan={3} className="px-2 py-0.5">
                              {r.text}
                            </td>
                          </tr>
                        ) : (
                          <tr
                            data-row={r.kind}
                            aria-selected={selected(i) || undefined}
                            {...(currentRow === i && {
                              'data-find-current': '',
                            })}
                            className={cn(
                              ROW_BG[r.kind],
                              isMatch(r) && 'bg-amber-400/20',
                              currentRow === i &&
                                'outline-2 -outline-offset-2 outline-amber-400',
                              selected(i) && 'bg-sky-500/20',
                            )}
                          >
                            {numCell(
                              r.oldNo,
                              r.kind === 'add' ? undefined : i,
                              'old',
                            )}
                            {numCell(
                              r.newNo,
                              r.kind === 'del' ? undefined : i,
                              'new',
                            )}
                            <td className="px-2 whitespace-pre">
                              <span className="select-none text-muted-foreground/60">
                                {r.kind === 'add'
                                  ? '+'
                                  : r.kind === 'del'
                                    ? '-'
                                    : ' '}
                              </span>
                              {r.text}
                            </td>
                          </tr>
                        )}
                        {bounds && bounds[1] === i && box && (
                          <tr>
                            <td colSpan={3} className="px-2">
                              {box}
                            </td>
                          </tr>
                        )}
                      </Fragment>
                    ))
                  : splitRows(rows).map((p, k) => {
                      if (p.kind === 'hunk')
                        return (
                          <tr
                            key={k}
                            className="bg-sky-500/5 text-muted-foreground"
                          >
                            <td colSpan={4} className="px-2 py-0.5">
                              {p.text}
                            </td>
                          </tr>
                        );
                      const hasBox =
                        bounds &&
                        box &&
                        (bounds[1] === p.leftIndex ||
                          bounds[1] === p.rightIndex);
                      const lcls = p.left
                        ? p.left.kind === 'del'
                          ? ROW_BG.del
                          : ''
                        : 'bg-muted/30';
                      const rcls = p.right
                        ? p.right.kind === 'add'
                          ? ROW_BG.add
                          : ''
                        : 'bg-muted/30';
                      const cur =
                        currentRow !== undefined &&
                        (currentRow === p.leftIndex ||
                          currentRow === p.rightIndex);
                      return (
                        <Fragment key={k}>
                          <tr
                            {...(cur && { 'data-find-current': '' })}
                            className={cn(
                              cur &&
                                'outline-2 -outline-offset-2 outline-amber-400',
                            )}
                          >
                            {numCell(p.left?.oldNo, p.leftIndex, 'old')}
                            <td
                              className={cn(
                                'w-1/2 border-r px-2 whitespace-pre',
                                lcls,
                                isMatch(p.left) && 'bg-amber-400/20',
                                selected(p.leftIndex) && 'bg-sky-500/20',
                              )}
                            >
                              {p.left?.text}
                            </td>
                            {numCell(
                              p.right?.newNo,
                              p.right?.kind === 'context'
                                ? undefined
                                : p.rightIndex,
                              'new',
                            )}
                            <td
                              className={cn(
                                'w-1/2 px-2 whitespace-pre',
                                rcls,
                                isMatch(p.right) && 'bg-amber-400/20',
                                selected(p.rightIndex) && 'bg-sky-500/20',
                              )}
                            >
                              {p.right?.text}
                            </td>
                          </tr>
                          {hasBox && (
                            <tr>
                              <td colSpan={4} className="px-2">
                                {box}
                              </td>
                            </tr>
                          )}
                        </Fragment>
                      );
                    })}
              </tbody>
            </table>
          </div>
        ))}
    </li>
  );
}
