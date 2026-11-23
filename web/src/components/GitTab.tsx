import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import {
  ChevronRight,
  Download,
  FolderGit2,
  GitBranch,
  GitFork,
  Loader2,
  SquarePen,
  Trash2,
  Upload,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  agentGitReposQuery,
  agentGitStatusQuery,
  type GitRepo,
} from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';

const gitKey = (agentId: string) => ['agents', agentId, 'git'];

/** The branch of a repo as shown in badges: the name, or "detached". */
export const repoBranch = (r: Pick<GitRepo, 'branch' | 'detached'>) =>
  r.detached ? `detached @ ${r.branch}` : r.branch;

/**
 * Git inside the agent: the repositories and worktrees found in its home,
 * with the actions to start a thread in one, pull, push, switch branches,
 * add or remove a worktree and clone another repository.
 */
export function GitTab({
  agentId,
  canUse,
  onStart,
  starting,
}: {
  agentId: string;
  canUse: boolean;
  onStart: () => void;
  starting: boolean;
}) {
  const repos = useQuery(agentGitReposQuery(agentId));

  if (repos.isPending) return <Loading />;
  if (repos.error) return <ErrorText error={repos.error} />;

  if (!repos.data.connected) {
    return (
      <div className="grid justify-items-start gap-3 rounded-xl border p-4">
        <p className="text-sm">
          The agent is asleep. Start it to see its repositories and worktrees.
        </p>
        {canUse && (
          <Button disabled={starting} onClick={onStart}>
            Start
          </Button>
        )}
      </div>
    );
  }

  const list = repos.data.repos;
  return (
    <div className="grid gap-4">
      {list.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No git repository in the home directory yet.
          {canUse && ' Clone one below.'}
        </p>
      ) : (
        <ul aria-label="Repositories" className="grid gap-3">
          {list.map((r) => (
            <li key={r.path}>
              <RepoCard agentId={agentId} repo={r} canUse={canUse} />
            </li>
          ))}
        </ul>
      )}
      {canUse && <CloneForm agentId={agentId} />}
    </div>
  );
}

function RepoCard({
  agentId,
  repo,
  canUse,
}: {
  agentId: string;
  repo: GitRepo;
  canUse: boolean;
}) {
  const qc = useQueryClient();
  const [panel, setPanel] = useState<'branches' | 'worktree' | null>(null);
  const toggle = (p: 'branches' | 'worktree') =>
    setPanel((cur) => (cur === p ? null : p));
  const pull = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/agents/{agentId}/git/pull', {
          params: { path: { agentId } },
          body: { path: repo.path },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: gitKey(agentId) }),
  });
  const push = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/agents/{agentId}/git/push', {
          params: { path: { agentId } },
          body: { path: repo.path },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: gitKey(agentId) }),
  });
  const remove = useMutation({
    mutationFn: (force: boolean) =>
      unwrap(
        api.POST('/agents/{agentId}/git/worktrees/remove', {
          params: { path: { agentId } },
          body: { path: repo.path, ...(force && { force: true }) },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: gitKey(agentId) }),
  });
  const dir = repo.path === repo.project ? '' : repo.path;
  return (
    <article
      aria-label={repo.path}
      className="grid gap-3 rounded-xl border p-3 text-sm"
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <FolderGit2
          className="size-4 shrink-0 text-muted-foreground"
          aria-hidden
        />
        <span className="min-w-0 truncate font-medium">{repo.project}</span>
        <Badge variant="outline" className="gap-1 font-mono">
          <GitBranch className="size-3" aria-hidden />
          {repoBranch(repo)}
        </Badge>
        {repo.isWorktree && <Badge>worktree</Badge>}
        {repo.dirty && (
          <Badge className="bg-amber-500/15 text-amber-700 dark:text-amber-400">
            dirty
          </Badge>
        )}
        {repo.ahead > 0 && (
          <span
            className="text-xs text-muted-foreground tabular-nums"
            title={`${repo.ahead} ahead`}
          >
            ↑{repo.ahead}
          </span>
        )}
        {repo.behind > 0 && (
          <span
            className="text-xs text-muted-foreground tabular-nums"
            title={`${repo.behind} behind`}
          >
            ↓{repo.behind}
          </span>
        )}
      </div>
      {(dir || repo.remoteUrl) && (
        <div className="grid gap-0.5 text-xs text-muted-foreground">
          {dir && <span className="truncate font-mono">~/{dir}</span>}
          {repo.remoteUrl && (
            <span className="truncate" title={repo.remoteUrl}>
              {repo.remoteUrl}
            </span>
          )}
        </div>
      )}
      {canUse && (
        <div className="flex flex-wrap gap-2">
          <Button asChild size="sm">
            <Link
              to="/a/$agentId"
              params={{ agentId }}
              search={{ cwd: repo.path }}
            >
              <SquarePen className="size-3.5" aria-hidden />
              New thread
            </Link>
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={pull.isPending}
            onClick={() => pull.mutate()}
          >
            {pull.isPending ? (
              <Loader2 className="size-3.5 animate-spin" aria-hidden />
            ) : (
              <Download className="size-3.5" aria-hidden />
            )}
            Pull
          </Button>
          {!repo.detached && repo.remoteUrl && (
            <Button
              size="sm"
              variant="outline"
              disabled={push.isPending}
              onClick={() => push.mutate()}
            >
              {push.isPending ? (
                <Loader2 className="size-3.5 animate-spin" aria-hidden />
              ) : (
                <Upload className="size-3.5" aria-hidden />
              )}
              Push
            </Button>
          )}
          <Button
            size="sm"
            variant="outline"
            aria-expanded={panel === 'branches'}
            onClick={() => toggle('branches')}
          >
            <ChevronRight
              className={cn(
                'size-3.5 transition-transform',
                panel === 'branches' && 'rotate-90',
              )}
              aria-hidden
            />
            Branches
          </Button>
          <Button
            size="sm"
            variant="outline"
            aria-expanded={panel === 'worktree'}
            onClick={() => toggle('worktree')}
          >
            <GitFork className="size-3.5" aria-hidden />
            New worktree
          </Button>
          {repo.isWorktree && (
            <Button
              size="sm"
              variant="ghost"
              disabled={remove.isPending}
              aria-label={`Remove worktree ${repo.path}`}
              onClick={() => {
                const msg = repo.dirty
                  ? `~/${repo.path} has uncommitted changes. Remove the worktree and discard them? The branch ${repo.branch} stays.`
                  : `Remove the worktree ~/${repo.path}? The branch ${repo.branch} stays.`;
                if (confirm(msg)) remove.mutate(repo.dirty);
              }}
            >
              <Trash2 className="size-3.5" aria-hidden />
              Remove worktree
            </Button>
          )}
        </div>
      )}
      <ErrorText error={pull.error ?? push.error ?? remove.error} />
      {canUse && panel === 'branches' && (
        <BranchPanel agentId={agentId} repo={repo} />
      )}
      {canUse && panel === 'worktree' && (
        <WorktreeForm
          agentId={agentId}
          repo={repo}
          onDone={() => setPanel(null)}
        />
      )}
    </article>
  );
}

function BranchPanel({ agentId, repo }: { agentId: string; repo: GitRepo }) {
  const qc = useQueryClient();
  const status = useQuery(agentGitStatusQuery(agentId, repo.path));
  const [name, setName] = useState('');
  const checkout = useMutation({
    mutationFn: (v: { branch: string; create?: boolean }) =>
      unwrap(
        api.POST('/agents/{agentId}/git/checkout', {
          params: { path: { agentId } },
          body: {
            path: repo.path,
            branch: v.branch,
            ...(v.create && { create: true }),
          },
        }),
      ),
    onSuccess: async () => {
      setName('');
      await qc.invalidateQueries({ queryKey: gitKey(agentId) });
    },
  });
  const current = status.data?.repo?.branch ?? repo.branch;
  return (
    <div className="grid gap-3 border-t pt-3">
      {status.isPending && <Loading />}
      <ErrorText error={status.error} />
      {status.data && (
        <ul
          aria-label={`Branches of ${repo.project}`}
          className="grid max-h-48 gap-px overflow-y-auto"
        >
          {status.data.branches.length === 0 && (
            <li className="text-xs text-muted-foreground">No branches yet.</li>
          )}
          {status.data.branches.map((b) => (
            <li
              key={b}
              className="flex items-center gap-2 rounded-md px-2 py-1 hover:bg-accent/50"
            >
              <GitBranch
                className="size-3.5 shrink-0 text-muted-foreground"
                aria-hidden
              />
              <span className="min-w-0 flex-1 truncate font-mono text-xs">
                {b}
              </span>
              {b === current && !repo.detached ? (
                <span className="text-xs text-muted-foreground">current</span>
              ) : (
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={checkout.isPending}
                  aria-label={`Switch to ${b}`}
                  onClick={() => checkout.mutate({ branch: b })}
                >
                  Switch
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim())
            checkout.mutate({ branch: name.trim(), create: true });
        }}
      >
        <label className="grid min-w-40 flex-1 gap-1 text-xs">
          New branch
          <Input
            value={name}
            placeholder="feature/my-change"
            onChange={(e) => setName(e.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </label>
        <Button
          type="submit"
          size="sm"
          variant="outline"
          disabled={checkout.isPending || !name.trim()}
        >
          Create branch
        </Button>
      </form>
      <ErrorText error={checkout.error} />
    </div>
  );
}

function WorktreeForm({
  agentId,
  repo,
  onDone,
}: {
  agentId: string;
  repo: GitRepo;
  onDone: () => void;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [branch, setBranch] = useState('');
  const [create, setCreate] = useState(true);
  const [fromRef, setFromRef] = useState('');
  const [chat, setChat] = useState(false);
  const add = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/agents/{agentId}/git/worktrees', {
          params: { path: { agentId } },
          body: {
            path: repo.path,
            branch: branch.trim(),
            ...(create && { create: true }),
            ...(create && fromRef.trim() && { fromRef: fromRef.trim() }),
          },
        }),
      ),
    onSuccess: async (wt) => {
      await qc.invalidateQueries({ queryKey: gitKey(agentId) });
      if (chat) {
        await navigate({
          to: '/a/$agentId',
          params: { agentId },
          search: { cwd: wt.path },
        });
      } else {
        onDone();
      }
    },
  });
  return (
    <form
      aria-label="New worktree"
      className="grid gap-3 border-t pt-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (branch.trim()) add.mutate();
      }}
    >
      <p className="text-xs text-muted-foreground">
        A worktree is a second checkout of {repo.project} on its own branch, so
        threads can work in parallel.
      </p>
      <label className="grid gap-1 text-xs">
        Worktree branch
        <Input
          value={branch}
          placeholder="feature/my-change"
          onChange={(e) => setBranch(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          required
        />
      </label>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={create}
          onChange={(e) => setCreate(e.target.checked)}
        />
        Create the branch
      </label>
      {create && (
        <label className="grid gap-1 text-xs">
          Base ref (optional)
          <Input
            value={fromRef}
            placeholder={repo.branch}
            onChange={(e) => setFromRef(e.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </label>
      )}
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={chat}
          onChange={(e) => setChat(e.target.checked)}
        />
        Start a thread in the worktree
      </label>
      <ErrorText error={add.error} />
      <div>
        <Button
          type="submit"
          size="sm"
          disabled={add.isPending || !branch.trim()}
        >
          Create worktree
        </Button>
      </div>
    </form>
  );
}

function CloneForm({ agentId }: { agentId: string }) {
  const qc = useQueryClient();
  const [url, setUrl] = useState('');
  const [path, setPath] = useState('');
  const [branch, setBranch] = useState('');
  const clone = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/agents/{agentId}/git/clone', {
          params: { path: { agentId } },
          body: {
            url: url.trim(),
            ...(path.trim() && { path: path.trim() }),
            ...(branch.trim() && { branch: branch.trim() }),
          },
        }),
      ),
    onSuccess: async () => {
      setUrl('');
      setPath('');
      setBranch('');
      await qc.invalidateQueries({ queryKey: gitKey(agentId) });
    },
  });
  return (
    <form
      aria-label="Clone repository"
      className="grid gap-3 rounded-xl border p-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (url.trim()) clone.mutate();
      }}
    >
      <h3 className="text-sm font-semibold">Clone repository</h3>
      <label className="grid gap-1 text-sm">
        Repository URL
        <Input
          value={url}
          placeholder="https://github.com/owner/repo.git"
          onChange={(e) => setUrl(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          required
        />
      </label>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="grid gap-1 text-sm">
          Folder (optional)
          <Input
            value={path}
            placeholder="projects/repo"
            onChange={(e) => setPath(e.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </label>
        <label className="grid gap-1 text-sm">
          Branch (optional)
          <Input
            value={branch}
            placeholder="default branch"
            onChange={(e) => setBranch(e.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </label>
      </div>
      <p className="text-xs text-muted-foreground">
        Private repositories need a credential:{' '}
        <Link to="/settings/git" className="underline underline-offset-2">
          connect GitHub or add an HTTPS token
        </Link>
        .
      </p>
      <ErrorText error={clone.error} />
      <div>
        <Button type="submit" disabled={clone.isPending || !url.trim()}>
          {clone.isPending ? 'Cloning…' : 'Clone'}
        </Button>
      </div>
    </form>
  );
}
