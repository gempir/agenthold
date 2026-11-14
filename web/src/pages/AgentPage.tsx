import { lazy, Suspense, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Moon } from 'lucide-react';
import { Link, useNavigate } from '@tanstack/react-router';
import { api, ApiError, unwrap } from '@/api/client';
import { agentQuery, type Agent } from '@/api/queries';
import { AgentStateBadge } from '@/components/AgentStateBadge';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { FilesTab } from '@/components/FilesTab';
import { NetworkTab } from '@/components/NetworkTab';
import { ChatTab } from '@/components/ChatTab';
import { HarnessPanel } from '@/components/HarnessPanel';

const TerminalTab = lazy(() =>
  import('@/components/TerminalTab').then((m) => ({ default: m.TerminalTab })),
);

type Tab = 'chat' | 'overview' | 'terminal' | 'files' | 'network';

export function AgentPage({
  agentId,
  sessionId,
  onSelectSession,
}: {
  agentId: string;
  sessionId?: string;
  onSelectSession?: (id: string) => void;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const q = useQuery(agentQuery(agentId));
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [tab, setTab] = useState<Tab>('chat');
  const path = { params: { path: { agentId } } };

  const setAgent = (a: Agent) => qc.setQueryData(['agents', agentId], a);
  const refreshList = (a: Agent) =>
    qc.invalidateQueries({ queryKey: ['workspaces', a.workspaceId, 'agents'] });

  const action = useMutation({
    mutationFn: (kind: 'start' | 'stop' | 'rebuild') =>
      unwrap(
        kind === 'start'
          ? api.POST('/agents/{agentId}/start', path)
          : kind === 'stop'
            ? api.POST('/agents/{agentId}/stop', path)
            : api.POST('/agents/{agentId}/rebuild', path),
      ),
    onSuccess: async (a) => {
      setAgent(a);
      await refreshList(a);
    },
  });

  const remove = useMutation({
    mutationFn: () => unwrap(api.DELETE('/agents/{agentId}', path)),
    onSuccess: async () => {
      const a = q.data;
      setConfirmOpen(false);
      qc.removeQueries({ queryKey: ['agents', agentId] });
      if (!a) return navigate({ to: '/' });
      await refreshList(a);
      await navigate({
        to: '/w/$workspaceId',
        params: { workspaceId: a.workspaceId },
      });
    },
  });

  if (q.isPending) return <Loading />;
  if (q.error) {
    if (
      q.error instanceof ApiError &&
      q.error.status === 404 &&
      remove.isSuccess
    )
      return null;
    return <ErrorText error={q.error} />;
  }
  const a = q.data;
  const operator = a.role === 'operator' || a.role === 'admin';
  const admin = a.role === 'admin';
  const busy = action.isPending || remove.isPending;

  return (
    <div className="flex flex-col gap-6">
      <Link
        to="/w/$workspaceId"
        params={{ workspaceId: a.workspaceId }}
        className="text-sm text-muted-foreground hover:text-foreground"
      >
        &larr; Back to workspace
      </Link>
      <header className="flex flex-wrap items-center gap-3">
        <h1 className="text-2xl font-semibold">{a.name}</h1>
        <AgentStateBadge state={a.state} sleepsAt={a.sleepsAt} />
      </header>

      {a.state === 'error' && a.stateError && (
        <p
          role="alert"
          className="rounded-md border border-destructive bg-destructive/10 p-3 text-sm text-destructive"
        >
          {a.stateError}
        </p>
      )}

      {a.state === 'sleeping' && (
        <div
          role="status"
          className="flex flex-wrap items-center gap-3 rounded-md border bg-muted p-3 text-sm"
        >
          <Moon className="size-4 shrink-0" aria-hidden />
          <p className="min-w-0 flex-1">
            This agent is asleep. Its files and conversations are kept. Sending
            a message or opening the terminal wakes it.
          </p>
          {operator && (
            <Button
              size="sm"
              disabled={busy}
              onClick={() => action.mutate('start')}
            >
              Wake now
            </Button>
          )}
        </div>
      )}

      <div role="tablist" className="flex gap-1 border-b">
        {(operator
          ? (['chat', 'overview', 'terminal', 'files', 'network'] as const)
          : (['chat', 'overview', 'files', 'network'] as const)
        ).map((t) => (
          <button
            key={t}
            role="tab"
            type="button"
            aria-selected={tab === t}
            onClick={() => setTab(t)}
            className={`-mb-px border-b-2 px-3 py-2 text-sm capitalize ${
              tab === t
                ? 'border-primary font-medium'
                : 'border-transparent text-muted-foreground hover:text-foreground'
            }`}
          >
            {t}
          </button>
        ))}
      </div>

      {tab === 'chat' && (
        <ChatTab
          agentId={agentId}
          operator={operator}
          sessionId={sessionId}
          agentState={a.state}
          onSelect={(id) => onSelectSession?.(id)}
        />
      )}
      {tab === 'overview' && (
        <>
          <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1 text-sm">
            <dt className="text-muted-foreground">Image</dt>
            <dd>{a.image}</dd>
            <dt className="text-muted-foreground">CPUs</dt>
            <dd>{a.cpus}</dd>
            <dt className="text-muted-foreground">Memory</dt>
            <dd>{a.memoryMb} MB</dd>
            <dt className="text-muted-foreground">Idle minutes</dt>
            <dd>{a.idleMinutes}</dd>
            <dt className="text-muted-foreground">Owner</dt>
            <dd>{a.owner.name}</dd>
            <dt className="text-muted-foreground">Created</dt>
            <dd>{new Date(a.createdAt).toLocaleString()}</dd>
          </dl>

          {a.needsRebuild && (
            <p className="text-sm text-amber-600">
              Rebuild to apply image/resource changes
            </p>
          )}

          {operator && (
            <div className="flex flex-wrap gap-2">
              {(a.state === 'sleeping' || a.state === 'error') && (
                <Button disabled={busy} onClick={() => action.mutate('start')}>
                  Start
                </Button>
              )}
              {(a.state === 'running' || a.state === 'error') && (
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() => action.mutate('stop')}
                >
                  Stop
                </Button>
              )}
              {(a.state === 'running' ||
                a.state === 'sleeping' ||
                a.state === 'error') && (
                <Button
                  variant={a.needsRebuild ? 'default' : 'outline'}
                  className={
                    a.needsRebuild ? 'ring-2 ring-amber-500' : undefined
                  }
                  disabled={busy}
                  onClick={() => action.mutate('rebuild')}
                >
                  Rebuild
                </Button>
              )}
              {admin && (
                <Button
                  variant="destructive"
                  disabled={busy}
                  onClick={() => setConfirmOpen(true)}
                >
                  Delete
                </Button>
              )}
            </div>
          )}
          <ErrorText error={action.error} />
          <ErrorText error={remove.error} />

          <HarnessPanel agent={a} admin={admin} onSaved={setAgent} />

          {admin && (
            <AgentSettings key={a.updatedAt} agent={a} onSaved={setAgent} />
          )}
        </>
      )}
      {tab === 'terminal' && operator && (
        <>
          {a.state === 'sleeping' && (
            <p className="mb-2 text-sm text-muted-foreground">
              Opening the terminal wakes the agent
            </p>
          )}
          <Suspense fallback={<Loading />}>
            <TerminalTab agentId={agentId} />
          </Suspense>
        </>
      )}
      {tab === 'files' && (
        <FilesTab
          agentId={agentId}
          operator={operator}
          starting={busy}
          onStart={() => action.mutate('start')}
        />
      )}

      {tab === 'network' && (
        <NetworkTab
          agentId={agentId}
          workspaceId={a.workspaceId}
          admin={admin}
        />
      )}

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {a.name}?</DialogTitle>
            <DialogDescription>
              The sandbox and its data are destroyed. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <ErrorText error={remove.error} />
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              Delete agent
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function AgentSettings({
  agent,
  onSaved,
}: {
  agent: Agent;
  onSaved: (a: Agent) => void;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState(agent.name);
  const [image, setImage] = useState(agent.image);
  const [cpus, setCpus] = useState(String(agent.cpus));
  const [mem, setMem] = useState(String(agent.memoryMb));
  const [idle, setIdle] = useState(String(agent.idleMinutes));

  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/agents/{agentId}', {
          params: { path: { agentId: agent.id } },
          body: {
            name: name.trim(),
            image: image.trim(),
            cpus: Number(cpus),
            memoryMb: Number(mem),
            idleMinutes: Number(idle),
          },
        }),
      ),
    onSuccess: async (a) => {
      onSaved(a);
      await qc.invalidateQueries({
        queryKey: ['workspaces', a.workspaceId, 'agents'],
      });
    },
  });

  return (
    <form
      className="grid gap-3 rounded-lg border p-4"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <h2 className="text-sm font-semibold">Settings</h2>
      <label className="grid gap-1 text-sm">
        Name
        <Input
          required
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </label>
      <label className="grid gap-1 text-sm">
        Image
        <Input
          required
          value={image}
          onChange={(e) => setImage(e.target.value)}
        />
      </label>
      <div className="grid grid-cols-3 gap-3">
        <label className="grid gap-1 text-sm">
          CPUs
          <Input
            type="number"
            min={1}
            value={cpus}
            onChange={(e) => setCpus(e.target.value)}
          />
        </label>
        <label className="grid gap-1 text-sm">
          Memory (MB)
          <Input
            type="number"
            min={128}
            value={mem}
            onChange={(e) => setMem(e.target.value)}
          />
        </label>
        <label className="grid gap-1 text-sm">
          Idle minutes
          <Input
            type="number"
            min={1}
            value={idle}
            onChange={(e) => setIdle(e.target.value)}
          />
        </label>
      </div>
      <ErrorText error={save.error} />
      <div>
        <Button type="submit" disabled={!name.trim() || save.isPending}>
          Save
        </Button>
      </div>
    </form>
  );
}
