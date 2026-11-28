import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import {
  Check,
  ChevronDown,
  Copy,
  FolderGit2,
  FolderKanban,
  GitBranch,
  RefreshCw,
  Settings,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  projectsQuery,
  type Agent,
  type Project,
  type Session,
} from '@/api/queries';
import { Menu, MenuItem, MenuSeparator } from '@/components/ui/menu';
import { cn } from '@/lib/utils';

/** Where a draft thread starts: a configured project, home, or an explicit
 * directory passed from the agent's Git tab. */
export interface GitDraft {
  /** Selected project id; empty when not bound to a project. */
  projectId: string;
  /** Work directly in the project's main checkouts instead of a new branch. */
  direct: boolean;
  /** Directory relative to home (from ?cwd=); empty for home. */
  cwd: string;
}

export const EMPTY_GIT_DRAFT: GitDraft = {
  projectId: '',
  direct: false,
  cwd: '',
};

/**
 * The draft's workspace choice. Starts from `initialCwd` (the `?cwd=` of the
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

/** The startChat fields of a draft workspace choice. */
export function gitStartArgs(d: GitDraft) {
  if (d.projectId) {
    return {
      projectId: d.projectId,
      ...(d.direct && { direct: true }),
    };
  }
  return {
    ...(d.cwd && { cwd: d.cwd }),
  };
}

const basename = (p: string) => p.split('/').filter(Boolean).pop() ?? p;

/**
 * Where a draft conversation starts, in the composer's meta bar: a clean picker
 * over the user's configured projects (or an explicit `?cwd=` from GitTab).
 * A started thread shows its project/branch in SessionGitLine.
 */
export function GitMeta({
  draft,
  onDraft,
  canUse = true,
}: {
  agent?: Agent;
  draft?: GitDraft;
  onDraft?: (d: GitDraft) => void;
  canUse?: boolean;
}) {
  if (!draft || !onDraft || !canUse) return null;
  return <DraftProjectPicker draft={draft} onDraft={onDraft} />;
}

/**
 * The project and branch a started thread is on, as a quiet line under its
 * composer: the branch can be copied, and re-detected with refresh. Nothing
 * when the thread is not known to be in a project or repository.
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
  const proj = session.project;
  const git = session.git;
  if (!proj && (!git || !(git.project || git.branch))) return null;

  const branch = proj ? (proj.branch ?? '') : (git?.branch ?? '');
  const copy = async () => {
    if (!branch) return;
    try {
      await navigator.clipboard.writeText(branch);
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
      {proj ? (
        <Link
          to="/projects/$projectId"
          params={{ projectId: proj.id }}
          className="flex min-w-0 shrink items-center gap-1 hover:text-foreground"
        >
          <FolderKanban className="size-3 shrink-0" aria-hidden />
          <span className="truncate">{proj.name}</span>
        </Link>
      ) : (
        git?.project && (
          <span
            title={git.repoPath ? `~/${git.repoPath}` : git.project}
            className="flex min-w-0 shrink items-center gap-1"
          >
            <FolderGit2 className="size-3 shrink-0" aria-hidden />
            <span className="truncate">{git.project}</span>
          </span>
        )
      )}
      {proj?.direct && !branch && (
        <span className="ml-1.5 truncate font-mono text-[11px]">
          main checkouts
        </span>
      )}
      {branch && (
        <>
          <GitBranch className="ml-1.5 size-3 shrink-0" aria-hidden />
          <span
            title={branch}
            className="min-w-0 truncate font-mono text-[11px]"
          >
            {branch}
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
      {canUse && !proj && (
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

function sourceSummary(p: Project) {
  if (p.sources.length === 0) return '';
  if (p.sources.length === 1) return p.sources[0].name;
  return `${p.sources.length} sources`;
}

function DraftProjectPicker({
  draft,
  onDraft,
}: {
  draft: GitDraft;
  onDraft: (d: GitDraft) => void;
}) {
  const navigate = useNavigate();
  const projects = useQuery(projectsQuery);
  const list = projects.data ?? [];
  const selected = list.find((p) => p.id === draft.projectId);
  // Nothing to choose from and no preselected directory: stay out of the way.
  if (list.length === 0 && !draft.cwd) return null;

  const pickProject = (p?: Project) =>
    onDraft({ projectId: p?.id ?? '', direct: false, cwd: '' });

  return (
    <Menu
      side="top"
      trigger={({ open, toggle }) => (
        <button
          type="button"
          aria-label="Project"
          aria-haspopup="menu"
          aria-expanded={open}
          onClick={toggle}
          className="flex min-w-0 items-center gap-1.5 rounded-md px-1.5 py-1 text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          {draft.cwd && !selected ? (
            <>
              <FolderGit2 className="size-3.5 shrink-0" aria-hidden />
              <span className="max-w-36 truncate text-foreground">
                {basename(draft.cwd)}
              </span>
            </>
          ) : selected ? (
            <>
              <FolderKanban className="size-3.5 shrink-0" aria-hidden />
              <span className="max-w-36 truncate text-foreground">
                {selected.name}
              </span>
              {draft.direct && (
                <span className="hidden font-mono text-xs sm:inline">main</span>
              )}
            </>
          ) : (
            <>
              <FolderKanban className="size-3.5 shrink-0" aria-hidden />
              <span>No project</span>
            </>
          )}
          <ChevronDown className="size-3.5" aria-hidden />
        </button>
      )}
      className="w-64 max-w-[90vw]"
    >
      {(close) => (
        <>
          <MenuItem
            active={!draft.projectId && !draft.cwd}
            onClick={() => {
              pickProject();
              close();
            }}
          >
            <span className="min-w-0 flex-1 truncate">No project</span>
            <span className="text-xs text-muted-foreground">Home</span>
          </MenuItem>
          {list.length > 0 && <MenuSeparator />}
          {list.map((p) => (
            <MenuItem
              key={p.id}
              active={p.id === draft.projectId}
              onClick={() => {
                pickProject(p);
                close();
              }}
            >
              <FolderKanban
                className="size-3.5 shrink-0 text-muted-foreground"
                aria-hidden
              />
              <span className="min-w-0 flex-1 truncate">{p.name}</span>
              {p.sources.length > 0 && (
                <span className="max-w-24 truncate text-xs text-muted-foreground">
                  {sourceSummary(p)}
                </span>
              )}
            </MenuItem>
          ))}
          {draft.cwd && !selected && (
            <>
              <MenuSeparator />
              <MenuItem active onClick={close}>
                <FolderGit2
                  className="size-3.5 shrink-0 text-muted-foreground"
                  aria-hidden
                />
                <span className="min-w-0 flex-1 truncate font-mono text-xs">
                  ~/{draft.cwd}
                </span>
              </MenuItem>
            </>
          )}
          {selected && (
            <>
              <MenuSeparator />
              <label className="flex cursor-pointer items-center gap-2 rounded-lg px-2 py-1.5 text-xs text-muted-foreground hover:bg-accent/50 hover:text-foreground">
                <input
                  type="checkbox"
                  checked={draft.direct}
                  onChange={(e) =>
                    onDraft({ ...draft, direct: e.target.checked })
                  }
                />
                <span>Work directly in main checkouts</span>
              </label>
            </>
          )}
          <MenuSeparator />
          <MenuItem
            onClick={() => {
              close();
              void navigate({ to: '/settings/projects' });
            }}
          >
            <Settings
              className="size-3.5 shrink-0 text-muted-foreground"
              aria-hidden
            />
            <span className="min-w-0 flex-1 truncate">Manage projects</span>
          </MenuItem>
        </>
      )}
    </Menu>
  );
}
