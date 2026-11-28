import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import {
  CircleAlert,
  GitBranch,
  GitMerge,
  ListTodo,
  MessagesSquare,
  Settings2,
  Upload,
  X,
  type LucideIcon,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  projectQuery,
  projectStatusQuery,
  projectTasksQuery,
  type Project,
  type ProjectInput,
  type ProjectSourceResult,
  type ProjectTask,
  type ProjectTaskStatus,
  type ProjectThread,
} from '@/api/queries';
import { AgentStateDot } from '@/components/AgentStateBadge';
import { ProjectForm } from '@/components/ProjectForm';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import {
  SettingsHeader,
  SettingsLayout,
  SettingsNavItem,
} from '@/components/layout/SettingsLayout';
import { Crumb, CrumbSep, TopBar } from '@/components/layout/TopBar';
import { useChatIndex } from '@/hooks/useChatIndex';
import { useDocumentTitle } from '@/hooks/useDocumentTitle';
import { accessOf } from '@/pages/AgentPage';
import { cn } from '@/lib/utils';

export type ProjectSection = 'threads' | 'tasks' | 'settings';

const SECTIONS: {
  id: ProjectSection;
  label: string;
  hint: string;
  icon: LucideIcon;
}[] = [
  {
    id: 'threads',
    label: 'Threads',
    hint: 'Each thread works on a branch of its own. Merge its work into the main checkouts, push it, or discard it.',
    icon: MessagesSquare,
  },
  {
    id: 'tasks',
    label: 'Tasks',
    hint: 'Work to hand to agents. Assigning a task starts a thread that works on it; agents set it to review when they are done.',
    icon: ListTodo,
  },
  {
    id: 'settings',
    label: 'Settings',
    hint: 'Repositories, folders and instructions for agents.',
    icon: Settings2,
  },
];

export const isProjectSection = (v: unknown): v is ProjectSection =>
  SECTIONS.some((s) => s.id === v);

export function ProjectPage({
  projectId,
  tab,
}: {
  projectId: string;
  tab?: ProjectSection;
}) {
  const project = useQuery(projectQuery(projectId));
  useDocumentTitle(project.data?.name ?? 'Project');
  if (project.isLoading)
    return (
      <>
        <TopBar />
        <Loading />
      </>
    );
  if (!project.data)
    return (
      <>
        <TopBar />
        <div className="p-6">
          <ErrorText error={project.error} />
        </div>
      </>
    );
  const p = project.data;
  const current = SECTIONS.find((s) => s.id === tab) ?? SECTIONS[0];
  return (
    <SettingsLayout
      navLabel="Project"
      wide={current.id !== 'settings'}
      crumbs={
        <>
          <Crumb>
            <Link to="/projects">Projects</Link>
          </Crumb>
          <CrumbSep />
          <Crumb current>{p.name}</Crumb>
        </>
      }
      nav={SECTIONS.map((s) => (
        <SettingsNavItem key={s.id} active={s.id === current.id}>
          <Link
            to="/projects/$projectId"
            params={{ projectId }}
            search={{ tab: s.id }}
          >
            <s.icon className="size-4 shrink-0" aria-hidden />
            {s.label}
          </Link>
        </SettingsNavItem>
      ))}
    >
      <SettingsHeader title={current.label} description={current.hint} />
      {current.id === 'threads' && <Threads project={p} />}
      {current.id === 'tasks' && <Tasks project={p} />}
      {current.id === 'settings' && <ProjectSettings project={p} />}
    </SettingsLayout>
  );
}

/** Agents the user may start threads with. */
function useUsableAgents() {
  const index = useChatIndex();
  return index.agents
    .filter((x) => accessOf(x.agent).canUse)
    .map((x) => x.agent);
}

function AgentPicker({
  value,
  onChange,
  label = 'Agent',
}: {
  value: string;
  onChange: (id: string) => void;
  label?: string;
}) {
  const agents = useUsableAgents();
  return (
    <Select
      aria-label={label}
      value={value}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="">{label}…</option>
      {agents.map((a) => (
        <option key={a.id} value={a.id}>
          {a.name}
          {a.state !== 'running' ? ` (${a.state})` : ''}
        </option>
      ))}
    </Select>
  );
}

function StartThread({ project }: { project: Project }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [agentId, setAgentId] = useState('');
  const [title, setTitle] = useState('');
  const [direct, setDirect] = useState(false);
  const start = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/projects/{projectId}/threads', {
          params: { path: { projectId: project.id } },
          body: {
            agentId,
            direct,
            ...(title.trim() && { title: title.trim() }),
          },
        }),
      ),
    onSuccess: async (s) => {
      await qc.invalidateQueries({ queryKey: ['agents'] });
      await qc.invalidateQueries({ queryKey: ['projects', project.id] });
      void navigate({
        to: '/a/$agentId',
        params: { agentId: s.agentId },
        search: { s: s.id },
      });
    },
  });
  return (
    <form
      className="mb-6 flex flex-wrap items-center gap-2 rounded-xl border p-3"
      onSubmit={(e) => {
        e.preventDefault();
        start.mutate();
      }}
    >
      <AgentPicker value={agentId} onChange={setAgentId} />
      <Input
        aria-label="Thread title"
        placeholder="Title (optional)"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        className="w-48 flex-1"
      />
      <label className="flex items-center gap-1.5 text-sm text-muted-foreground">
        <input
          type="checkbox"
          checked={direct}
          onChange={(e) => setDirect(e.target.checked)}
        />
        In the main checkouts
      </label>
      <Button type="submit" disabled={!agentId || start.isPending}>
        Start thread
      </Button>
      <ErrorText error={start.error} />
    </form>
  );
}

function Results({ results }: { results?: ProjectSourceResult[] }) {
  if (!results?.length) return null;
  return (
    <ul className="mt-2 grid gap-0.5 text-xs">
      {results.map((r) => (
        <li
          key={r.source}
          className={r.error ? 'text-destructive' : 'text-muted-foreground'}
        >
          {r.source}:{' '}
          {r.error ??
            (r.done
              ? `${r.commits} commit${r.commits === 1 ? '' : 's'} done`
              : 'nothing to do')}
        </li>
      ))}
    </ul>
  );
}

function ThreadRow({
  thread,
  overlapping,
  project,
}: {
  thread: ProjectThread;
  overlapping: Set<string>;
  project: Project;
}) {
  const qc = useQueryClient();
  const s = thread.session;
  const [results, setResults] = useState<ProjectSourceResult[]>();
  const refresh = () =>
    qc.invalidateQueries({ queryKey: ['projects', project.id] });
  const act = useMutation({
    mutationFn: async (what: 'merge' | 'push' | 'close' | 'discard') => {
      const path = { params: { path: { sessionId: s.id } } };
      if (what === 'merge')
        return unwrap(api.POST('/sessions/{sessionId}/project/merge', path));
      if (what === 'push')
        return unwrap(api.POST('/sessions/{sessionId}/project/push', path));
      await unwrap(
        api.POST('/sessions/{sessionId}/project/close', {
          ...path,
          body: { force: what === 'discard' },
        }),
      );
      return undefined;
    },
    onSuccess: async (r) => {
      setResults(r);
      await refresh();
      await qc.invalidateQueries({ queryKey: ['agents'] });
    },
  });
  const direct = s.project?.direct;
  const commits = thread.sources.reduce((n, c) => n + c.commits, 0);
  const dirty = thread.sources.some((c) => c.dirty);
  const canPush = thread.sources.some((c) => c.remote && c.commits > 0);
  return (
    <li className="rounded-xl border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Link
          to="/a/$agentId"
          params={{ agentId: thread.agentId }}
          search={{ s: s.id }}
          className="min-w-0 flex-1 truncate font-medium hover:underline"
        >
          {s.title}
        </Link>
        <span className="flex items-center gap-1 text-xs text-muted-foreground">
          {s.running && <AgentStateDot state="running" />}
          {thread.agentName}
        </span>
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span className="flex items-center gap-1">
          <GitBranch className="size-3.5" aria-hidden />
          {direct ? 'main checkouts' : s.project?.branch}
        </span>
        {!thread.connected && <span>machine off: changes unknown</span>}
        {thread.connected && !direct && (
          <span>
            {commits} commit{commits === 1 ? '' : 's'}
            {dirty && ', uncommitted changes'}
          </span>
        )}
        {s.settledAt && <span>settled</span>}
      </div>
      {thread.sources.some((c) => c.files.length > 0 || c.error) && (
        <details className="mt-2 text-xs">
          <summary className="cursor-pointer text-muted-foreground">
            Changed files
          </summary>
          <ul className="mt-1 grid gap-0.5 font-mono">
            {thread.sources.flatMap((c) => [
              ...(c.error
                ? [
                    <li key={c.source + ':err'} className="text-destructive">
                      {c.source}: {c.error}
                    </li>,
                  ]
                : []),
              ...c.files.map((f) => {
                const k = `${c.source}/${f.path}`;
                return (
                  <li
                    key={k}
                    className={cn(
                      overlapping.has(k) &&
                        'text-amber-600 dark:text-amber-400',
                    )}
                    title={
                      overlapping.has(k)
                        ? 'Another thread changed this file too'
                        : undefined
                    }
                  >
                    {f.status} {k}
                  </li>
                );
              }),
            ])}
          </ul>
        </details>
      )}
      {!direct && thread.connected && !s.settledAt && (
        <div className="mt-3 flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={act.isPending || s.running || commits === 0 || dirty}
            title={dirty ? 'Commit the uncommitted changes first' : undefined}
            onClick={() => act.mutate('merge')}
          >
            <GitMerge className="size-4" />
            Merge
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={act.isPending || s.running || !canPush}
            onClick={() => act.mutate('push')}
          >
            <Upload className="size-4" />
            Push branch
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={act.isPending || s.running}
            onClick={() => {
              if (commits === 0 && !dirty) act.mutate('close');
              else if (
                confirm(
                  'Discard this thread’s branch and uncommitted changes? This cannot be undone.',
                )
              )
                act.mutate('discard');
            }}
          >
            <X className="size-4" />
            {commits === 0 && !dirty ? 'Close workspace' : 'Discard'}
          </Button>
        </div>
      )}
      <Results results={results} />
      <ErrorText error={act.error ?? thread.error} />
    </li>
  );
}

function Threads({ project }: { project: Project }) {
  const [settled, setSettled] = useState(false);
  const status = useQuery(projectStatusQuery(project.id, settled));
  const overlapping = new Set(
    (status.data?.overlaps ?? []).map((o) => `${o.source}/${o.path}`),
  );
  const titleOf = (id: string) =>
    status.data?.threads.find((t) => t.session.id === id)?.session.title ?? id;
  return (
    <>
      <StartThread project={project} />
      {status.isLoading && <Loading />}
      <ErrorText error={status.error} />
      {!!status.data?.overlaps.length && (
        <div
          role="alert"
          className="mb-4 rounded-xl border border-amber-500/40 bg-amber-500/10 p-3 text-sm"
        >
          <p className="flex items-center gap-2 font-medium">
            <CircleAlert className="size-4" aria-hidden />
            Threads change the same files
          </p>
          <ul className="mt-1 grid gap-0.5 text-xs">
            {status.data.overlaps.map((o) => (
              <li key={o.source + o.path}>
                <span className="font-mono">
                  {o.source}/{o.path}
                </span>
                : {o.sessionIds.map(titleOf).join(', ')}
              </li>
            ))}
          </ul>
        </div>
      )}
      {status.data?.threads.length === 0 && (
        <p className="text-sm text-muted-foreground">
          No {settled ? '' : 'active '}threads of this project yet.
        </p>
      )}
      <ul aria-label="Project threads" className="grid gap-2">
        {status.data?.threads.map((t) => (
          <ThreadRow
            key={t.session.id}
            thread={t}
            overlapping={overlapping}
            project={project}
          />
        ))}
      </ul>
      <label className="mt-4 flex items-center gap-1.5 text-sm text-muted-foreground">
        <input
          type="checkbox"
          checked={settled}
          onChange={(e) => setSettled(e.target.checked)}
        />
        Show settled threads
      </label>
    </>
  );
}

const STATUS_LABEL: Record<ProjectTaskStatus, string> = {
  open: 'Open',
  in_progress: 'In progress',
  review: 'Review',
  done: 'Done',
  cancelled: 'Cancelled',
};
const STATUS_ORDER: ProjectTaskStatus[] = [
  'review',
  'in_progress',
  'open',
  'done',
  'cancelled',
];

function TaskRow({ task, project }: { task: ProjectTask; project: Project }) {
  const qc = useQueryClient();
  const [agentId, setAgentId] = useState('');
  const refresh = () =>
    qc.invalidateQueries({ queryKey: ['projects', project.id] });
  const update = useMutation({
    mutationFn: (status: ProjectTaskStatus) =>
      unwrap(
        api.PATCH('/tasks/{taskId}', {
          params: { path: { taskId: task.id } },
          body: { status },
        }),
      ),
    onSuccess: refresh,
  });
  const assign = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/tasks/{taskId}/assign', {
          params: { path: { taskId: task.id } },
          body: { agentId },
        }),
      ),
    onSuccess: async () => {
      setAgentId('');
      await refresh();
      await qc.invalidateQueries({ queryKey: ['agents'] });
    },
  });
  const remove = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE('/tasks/{taskId}', {
          params: { path: { taskId: task.id } },
        }),
      ),
    onSuccess: refresh,
  });
  const assignable = task.status === 'open' || task.status === 'in_progress';
  return (
    <li className="rounded-xl border p-3">
      <div className="flex flex-wrap items-start gap-2">
        <div className="min-w-0 flex-1">
          <p className="font-medium">{task.title}</p>
          {task.body && (
            <p className="mt-0.5 text-sm whitespace-pre-wrap text-muted-foreground">
              {task.body}
            </p>
          )}
        </div>
        <Select
          aria-label="Status"
          value={task.status}
          disabled={update.isPending}
          onChange={(e) => update.mutate(e.target.value as ProjectTaskStatus)}
          className="h-8"
        >
          {STATUS_ORDER.map((s) => (
            <option key={s} value={s}>
              {STATUS_LABEL[s]}
            </option>
          ))}
        </Select>
      </div>
      <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span>by {task.createdBy}</span>
        {task.agentName && task.agentId && (
          <span>
            {task.sessionId ? (
              <Link
                to="/a/$agentId"
                params={{ agentId: task.agentId }}
                search={{ s: task.sessionId }}
                className="underline"
              >
                {task.agentName}’s thread
              </Link>
            ) : (
              task.agentName
            )}
          </span>
        )}
        {task.note && <span className="italic">“{task.note}”</span>}
      </div>
      {assignable && (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <AgentPicker
            value={agentId}
            onChange={setAgentId}
            label={task.agentId ? 'Reassign to' : 'Assign to'}
          />
          <Button
            size="sm"
            variant="outline"
            disabled={!agentId || assign.isPending}
            onClick={() => assign.mutate()}
          >
            Assign
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={remove.isPending}
            onClick={() => {
              if (confirm('Delete this task?')) remove.mutate();
            }}
          >
            Delete
          </Button>
        </div>
      )}
      <ErrorText error={update.error ?? assign.error ?? remove.error} />
    </li>
  );
}

function Tasks({ project }: { project: Project }) {
  const qc = useQueryClient();
  const tasks = useQuery(projectTasksQuery(project.id));
  const [title, setTitle] = useState('');
  const [body, setBody] = useState('');
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/projects/{projectId}/tasks', {
          params: { path: { projectId: project.id } },
          body: {
            title: title.trim(),
            ...(body.trim() && { body: body.trim() }),
          },
        }),
      ),
    onSuccess: async () => {
      setTitle('');
      setBody('');
      await qc.invalidateQueries({ queryKey: ['projects', project.id] });
    },
  });
  const byStatus = STATUS_ORDER.map((s) => ({
    status: s,
    tasks: (tasks.data ?? []).filter((t) => t.status === s),
  })).filter((g) => g.tasks.length > 0);
  return (
    <>
      <form
        className="mb-6 flex flex-col gap-2 rounded-xl border p-3"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <Input
          aria-label="Task title"
          placeholder="New task"
          value={title}
          maxLength={200}
          onChange={(e) => setTitle(e.target.value)}
        />
        <textarea
          aria-label="Task description"
          placeholder="What to do, in enough detail for an agent (optional)"
          value={body}
          rows={3}
          onChange={(e) => setBody(e.target.value)}
          className="rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-xs placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        />
        <div>
          <Button
            type="submit"
            size="sm"
            disabled={!title.trim() || create.isPending}
          >
            Add task
          </Button>
        </div>
        <ErrorText error={create.error} />
      </form>
      {tasks.isLoading && <Loading />}
      <ErrorText error={tasks.error} />
      {tasks.data?.length === 0 && (
        <p className="text-sm text-muted-foreground">No tasks yet.</p>
      )}
      {byStatus.map((g) => (
        <section key={g.status} className="mb-5">
          <h2 className="mb-2 text-xs font-medium text-muted-foreground uppercase">
            {STATUS_LABEL[g.status]} · {g.tasks.length}
          </h2>
          <ul
            aria-label={`${STATUS_LABEL[g.status]} tasks`}
            className="grid gap-2"
          >
            {g.tasks.map((t) => (
              <TaskRow key={t.id} task={t} project={project} />
            ))}
          </ul>
        </section>
      ))}
    </>
  );
}

function ProjectSettings({ project }: { project: Project }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const save = useMutation({
    mutationFn: (body: ProjectInput) =>
      unwrap(
        api.PATCH('/projects/{projectId}', {
          params: { path: { projectId: project.id } },
          body,
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['projects'] }),
  });
  const del = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE('/projects/{projectId}', {
          params: { path: { projectId: project.id } },
        }),
      ),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['projects'] });
      void navigate({ to: '/projects' });
    },
  });
  return (
    <div className="flex flex-col gap-8">
      <p className="text-sm text-muted-foreground">
        On agents&apos; disks the project lives in{' '}
        <code>projects/{project.slug}</code>; thread workspaces in{' '}
        <code>projects/{project.slug}/.threads</code>.
      </p>
      <ProjectForm
        key={project.updatedAt}
        project={project}
        submitLabel={save.isSuccess && !save.isPending ? 'Saved' : 'Save'}
        pending={save.isPending}
        error={save.error}
        onSubmit={(b) => save.mutate(b)}
      />
      <section className="rounded-xl border border-destructive/30 p-4">
        <h2 className="text-sm font-medium">Delete project</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Deletes the project and its tasks. Threads and the checkouts on
          agents&apos; disks stay.
        </p>
        <Button
          variant="destructive"
          size="sm"
          className="mt-3"
          disabled={del.isPending}
          onClick={() => {
            if (confirm(`Delete project ${project.name}?`)) del.mutate();
          }}
        >
          Delete project
        </Button>
        <ErrorText error={del.error} />
      </section>
    </div>
  );
}
