import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { CircleAlert, Copy, RefreshCw } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { agentsQuery, sandboxLimitsQuery, type Agent } from '@/api/queries';
import { AgentStateBadge } from '@/components/AgentStateBadge';
import { ErrorText } from '@/components/Spinner';
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
import { useAgentActions } from '@/hooks/useAgentActions';
import { cn } from '@/lib/utils';

/** State, lifecycle buttons and problems of one agent. */
export function AgentControls({
  agent: a,
  canUse,
  compact,
}: {
  agent: Agent;
  canUse: boolean;
  compact?: boolean;
}) {
  const { action, busy } = useAgentActions(a);
  const limits = useQuery(sandboxLimitsQuery).data;
  const tooSmall = !!limits && a.memoryMb < limits.minMemoryMb;
  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <AgentStateBadge state={a.state} sleepsAt={a.sleepsAt} />
      </div>
      {a.state === 'unreachable' && (
        <p role="status" className="text-sm text-destructive">
          {a.nodeName || 'The assigned node'} is offline. This agent will be
          available when the node reconnects.
        </p>
      )}
      {a.state === 'error' && a.stateError && (
        <p
          role="alert"
          className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/5 p-2.5 text-sm text-destructive"
        >
          <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          {a.stateError}
        </p>
      )}
      {tooSmall && (
        <p
          role="alert"
          className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/5 p-2.5 text-sm text-destructive"
        >
          <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          This agent has only {a.memoryMb} MB of memory, too little to run a
          harness. Raise it to at least {limits.minMemoryMb} MB in settings.
        </p>
      )}
      {a.needsRebuild && (
        <p className="flex items-center gap-2 text-sm text-amber-600 dark:text-amber-400">
          <RefreshCw className="size-3.5" aria-hidden />
          Reset the system to apply resource or environment changes
        </p>
      )}
      {a.isTemplate && (
        <p className="flex items-start gap-2 rounded-lg border bg-muted/40 p-2.5 text-sm text-muted-foreground">
          <Copy className="mt-0.5 size-4 shrink-0" aria-hidden />
          This machine is a template. It is never started or chatted with; clone
          it to get a new agent.
        </p>
      )}
      {canUse && !a.isTemplate && (
        <div className="flex flex-wrap gap-2">
          {(a.state === 'sleeping' ||
            a.state === 'error' ||
            a.state === 'unreachable') && (
            <Button
              size={compact ? 'sm' : 'default'}
              disabled={busy || a.state === 'unreachable'}
              onClick={() => action.mutate('start')}
            >
              Start
            </Button>
          )}
          {(a.state === 'running' ||
            a.state === 'error' ||
            a.state === 'unreachable') && (
            <Button
              size={compact ? 'sm' : 'default'}
              variant="outline"
              disabled={busy || a.state === 'unreachable'}
              onClick={() => action.mutate('stop')}
            >
              Stop
            </Button>
          )}
          {a.role === 'owner' &&
            (a.state === 'running' ||
              a.state === 'sleeping' ||
              a.state === 'error') && (
              <Button
                size={compact ? 'sm' : 'default'}
                variant={a.needsRebuild ? 'default' : 'outline'}
                className={a.needsRebuild ? 'ring-2 ring-amber-500' : undefined}
                disabled={busy}
                onClick={() => action.mutate('rebuild')}
              >
                Reset system
              </Button>
            )}
        </div>
      )}
      {a.role === 'owner' && (
        <p className="text-xs text-muted-foreground">
          Resetting the system discards system changes and keeps your home
          files.
        </p>
      )}
      <ErrorText error={action.error} />
    </div>
  );
}

export function AgentDetails({
  agent: a,
  className,
}: {
  agent: Agent;
  className?: string;
}) {
  return (
    <dl
      className={cn(
        'grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 text-sm',
        className,
      )}
    >
      {a.nodeId && (
        <>
          <dt className="text-muted-foreground">Node</dt>
          <dd>{a.nodeName || a.nodeId}</dd>
        </>
      )}
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
  );
}

export function AgentSettingsForm({
  agent,
  onSaved,
}: {
  agent: Agent;
  onSaved: (a: Agent) => void;
}) {
  const qc = useQueryClient();
  const limits = useQuery(sandboxLimitsQuery).data;
  const [name, setName] = useState(agent.name);
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
            cpus: Number(cpus),
            memoryMb: Number(mem),
            idleMinutes: Number(idle),
          },
        }),
      ),
    onSuccess: async (a) => {
      onSaved(a);
      await qc.invalidateQueries({ queryKey: agentsQuery.queryKey });
    },
  });

  return (
    <form
      className="grid gap-3 rounded-xl border p-4"
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
      <div className="grid gap-3 sm:grid-cols-3">
        <label className="grid gap-1 text-sm">
          CPUs
          <Input
            type="number"
            min={1}
            max={limits?.maxCpus}
            value={cpus}
            onChange={(e) => setCpus(e.target.value)}
          />
        </label>
        <label className="grid gap-1 text-sm">
          Memory (MB)
          <Input
            type="number"
            min={limits?.minMemoryMb}
            max={limits?.maxMemoryMb}
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
        <Button
          type="submit"
          disabled={!name.trim() || !limits || save.isPending}
        >
          Save
        </Button>
      </div>
    </form>
  );
}

export function DeleteAgent({ agent }: { agent: Agent }) {
  const { remove, busy } = useAgentActions(agent);
  const [open, setOpen] = useState(false);
  return (
    <section className="grid gap-2 rounded-xl border border-destructive/30 p-4">
      <h2 className="text-sm font-semibold">Delete agent</h2>
      <p className="text-sm text-muted-foreground">
        Destroys the sandbox, its files and all conversations.
      </p>
      <div>
        <Button
          variant="destructive"
          disabled={busy}
          onClick={() => setOpen(true)}
        >
          Delete
        </Button>
      </div>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {agent.name}?</DialogTitle>
            <DialogDescription>
              The sandbox and its data are destroyed. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <ErrorText error={remove.error} />
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
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
    </section>
  );
}
