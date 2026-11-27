import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Check,
  ChevronDown,
  Copy,
  FolderGit2,
  GitBranch,
  RefreshCw,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  agentGitReposQuery,
  type Agent,
  type GitRepo,
  type Session,
} from '@/api/queries';
import { Input } from '@/components/ui/input';
import { Menu, MenuItem, MenuSeparator } from '@/components/ui/menu';
import { cn } from '@/lib/utils';

/** Where a draft chat starts: a repository (or home), optionally on a branch
 * or in a fresh worktree. */
export interface GitDraft {
  /** Directory relative to home; empty for home. */
  cwd: string;
  branch: string;
  worktree: boolean;
}

export const EMPTY_GIT_DRAFT: GitDraft = {
  cwd: '',
  branch: '',
  worktree: false,
};

/**
 * The draft's git choice. It starts from `initialCwd` (the `?cwd=` of the
 * page) and follows it when that changes, until the user picks something else.
 */
export function useGitDraft(
  initialCwd?: string,
): [GitDraft, (d: GitDraft) => void] {
  const [picked, setPicked] = useState<{ from?: string; draft: GitDraft }>();
  const draft =
    picked && picked.from === initialCwd
      ? picked.draft
      : { ...EMPTY_GIT_DRAFT, cwd: initialCwd ?? '' };
  return [draft, (d) => setPicked({ from: initialCwd, draft: d })];
}

/** The createSession fields of a draft. */
export function gitStartArgs(d: GitDraft) {
  const branch = d.branch.trim();
  return {
    ...(d.cwd && { cwd: d.cwd }),
    ...(d.cwd &&
      branch && {
        gitBranch: branch,
        createBranch: true,
        createWorktree: d.worktree,
      }),
  };
}

const basename = (p: string) => p.split('/').filter(Boolean).pop() ?? p;

/**
 * Where a draft conversation starts, in the composer's meta bar: a picker
 * over the repositories found in the agent's home, and the choice to start
 * in a new worktree. A started thread shows its branch in SessionGitLine.
 */
export function GitMeta({
  agent,
  draft,
  onDraft,
  canUse = true,
}: {
  agent: Agent;
  draft?: GitDraft;
  onDraft?: (d: GitDraft) => void;
  canUse?: boolean;
}) {
  if (!draft || !onDraft || !canUse) return null;
  return <DraftGitPicker agent={agent} draft={draft} onDraft={onDraft} />;
}

/**
 * The project and branch a started thread is on, as a quiet line under its
 * composer: the branch can be copied, and re-detected with refresh. Nothing
 * when the thread is not known to be in a repository.
 */
export function SessionGitLine({
  agent,
  session,
  canUse,
}: {
  agent: Agent;
  session: Session;
  canUse: boolean;
}) {
  const qc = useQueryClient();
  const [copied, setCopied] = useState(false);
  const refresh = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/sessions/{sessionId}/git', {
          params: { path: { sessionId: session.id } },
          body: { refresh: true },
        }),
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ['agents', agent.id, 'sessions'] }),
  });
  const git = session.git;
  if (!git || !(git.project || git.branch)) return null;
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(git.branch);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // No clipboard (an insecure context): nothing to show.
    }
  };
  return (
    <div
      data-testid="session-git"
      className="flex min-w-0 items-center gap-1 px-3 text-xs text-muted-foreground/80"
    >
      {git.project && (
        <span
          title={git.repoPath ? `~/${git.repoPath}` : git.project}
          className="flex min-w-0 shrink items-center gap-1"
        >
          <FolderGit2 className="size-3 shrink-0" aria-hidden />
          <span className="truncate">{git.project}</span>
        </span>
      )}
      {git.branch && (
        <>
          <GitBranch className="ml-1.5 size-3 shrink-0" aria-hidden />
          <span
            title={git.branch}
            className="min-w-0 truncate font-mono text-[11px]"
          >
            {git.branch}
          </span>
          <button
            type="button"
            aria-label={copied ? 'Branch name copied' : 'Copy branch name'}
            title={copied ? 'Copied' : 'Copy branch name'}
            onClick={() => void copy()}
            className="flex size-5 shrink-0 items-center justify-center rounded hover:bg-accent hover:text-foreground"
          >
            {copied ? (
              <Check className="size-3 text-emerald-600 dark:text-emerald-400" />
            ) : (
              <Copy className="size-3" />
            )}
          </button>
        </>
      )}
      {canUse && (
        <button
          type="button"
          aria-label="Refresh git branch"
          title="Refresh git branch"
          disabled={refresh.isPending}
          onClick={() => refresh.mutate()}
          className="flex size-5 shrink-0 items-center justify-center rounded hover:bg-accent hover:text-foreground disabled:opacity-50"
        >
          <RefreshCw
            className={cn('size-3', refresh.isPending && 'animate-spin')}
            aria-hidden
          />
        </button>
      )}
    </div>
  );
}

function DraftGitPicker({
  agent,
  draft,
  onDraft,
}: {
  agent: Agent;
  draft: GitDraft;
  onDraft: (d: GitDraft) => void;
}) {
  const repos = useQuery({
    ...agentGitReposQuery(agent.id),
    enabled: agent.state === 'running',
  });
  const list = repos.data?.connected ? repos.data.repos : [];
  const selected = list.find((r) => r.path === draft.cwd);
  // Nothing to choose from and nothing preselected: stay out of the way.
  if (list.length === 0 && !draft.cwd) return null;

  const project = selected?.project ?? (draft.cwd ? basename(draft.cwd) : '');
  const branch = draft.branch.trim() || selected?.branch;
  const pick = (r?: GitRepo) =>
    onDraft({ cwd: r?.path ?? '', branch: '', worktree: false });

  return (
    <Menu
      trigger={({ open, toggle }) => (
        <button
          type="button"
          aria-label="Project"
          aria-haspopup="menu"
          aria-expanded={open}
          onClick={toggle}
          className="flex min-w-0 items-center gap-1.5 rounded-md px-1.5 py-1 text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          <FolderGit2 className="size-3.5 shrink-0" aria-hidden />
          {project ? (
            <>
              <span className="max-w-32 truncate text-foreground">
                {project}
              </span>
              {branch && (
                <span className="hidden max-w-28 truncate font-mono text-xs sm:inline">
                  {draft.worktree && '+ '}
                  {branch}
                </span>
              )}
            </>
          ) : (
            <span>No project</span>
          )}
          <ChevronDown className="size-3.5" aria-hidden />
        </button>
      )}
      className="bottom-full top-auto mb-1 w-72 max-w-[90vw]"
    >
      {(close) => (
        <>
          <MenuItem
            active={!draft.cwd}
            onClick={() => {
              pick();
              close();
            }}
          >
            <span className="min-w-0 flex-1 truncate">
              Home directory (no project)
            </span>
          </MenuItem>
          {list.length > 0 && <MenuSeparator />}
          {list.map((r) => (
            <MenuItem
              key={r.path}
              active={r.path === draft.cwd}
              onClick={() => {
                pick(r);
                close();
              }}
            >
              <FolderGit2
                className="size-3.5 shrink-0 text-muted-foreground"
                aria-hidden
              />
              <span className="min-w-0 flex-1 truncate">
                {r.project}
                {r.isWorktree && (
                  <span className="text-xs text-muted-foreground">
                    {' '}
                    · worktree
                  </span>
                )}
              </span>
              <span className="max-w-24 truncate font-mono text-xs text-muted-foreground">
                {r.branch}
              </span>
            </MenuItem>
          ))}
          {draft.cwd && (
            <>
              <MenuSeparator />
              <div className="grid gap-2 px-2 py-1.5 text-sm">
                <label className="grid gap-1 text-xs text-muted-foreground">
                  Branch
                  <Input
                    value={draft.branch}
                    placeholder={selected?.branch ?? 'current branch'}
                    onChange={(e) =>
                      onDraft({ ...draft, branch: e.target.value })
                    }
                    autoComplete="off"
                    spellCheck={false}
                    className="h-8 font-mono text-xs"
                  />
                </label>
                <label className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={draft.worktree}
                    onChange={(e) =>
                      onDraft({ ...draft, worktree: e.target.checked })
                    }
                  />
                  Create worktree
                </label>
                {draft.worktree && !draft.branch.trim() && (
                  <p className="text-xs text-muted-foreground">
                    Name the branch of the new worktree.
                  </p>
                )}
              </div>
            </>
          )}
        </>
      )}
    </Menu>
  );
}
