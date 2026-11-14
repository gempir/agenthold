import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import {
  agentsQuery,
  harnessesQuery,
  sandboxLimitsQuery,
  type Agent,
} from '@/api/queries';
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
import { Select } from '@/components/ui/select';

export function AgentList({ agents }: { agents: Agent[] }) {
  if (agents.length === 0)
    return <p className="text-sm text-muted-foreground">No agents yet.</p>;
  return (
    <ul className="flex flex-col divide-y rounded-lg border">
      {agents.map((a) => (
        <li key={a.id}>
          <Link
            to="/a/$agentId"
            params={{ agentId: a.id }}
            className="flex flex-wrap items-center gap-3 p-3 hover:bg-accent"
          >
            <span className="font-medium">{a.name}</span>
            <AgentStateBadge state={a.state} sleepsAt={a.sleepsAt} />
            {a.needsRebuild && (
              <span className="text-xs text-amber-600">rebuild pending</span>
            )}
            <span className="ml-auto text-xs text-muted-foreground">
              {a.image} · {a.owner.name}
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

export function AgentsSection({ workspaceId }: { workspaceId: string }) {
  const agents = useQuery(agentsQuery(workspaceId));
  const [open, setOpen] = useState(false);
  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">Agents</h2>
        <Button size="sm" onClick={() => setOpen(true)}>
          New agent
        </Button>
      </div>
      {agents.isPending && <Loading />}
      <ErrorText error={agents.error} />
      {agents.data && <AgentList agents={agents.data} />}
      {open && (
        <NewAgentDialog
          workspaceId={workspaceId}
          onClose={() => setOpen(false)}
        />
      )}
    </section>
  );
}

function NewAgentDialog({
  workspaceId,
  onClose,
}: {
  workspaceId: string;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const limits = useQuery(sandboxLimitsQuery);
  const l = limits.data;
  const harnesses = useQuery(harnessesQuery);
  const [harness, setHarness] = useState('claude-code');
  const [model, setModel] = useState('');
  const [name, setName] = useState('');
  const [image, setImage] = useState('');
  const [cpus, setCpus] = useState<string>('');
  const [mem, setMem] = useState<string>('');
  const [idle, setIdle] = useState('15');

  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/workspaces/{workspaceId}/agents', {
          params: { path: { workspaceId } },
          body: {
            name: name.trim(),
            ...(image.trim() && { image: image.trim() }),
            cpus: Number(cpus || l?.defaultCpus),
            memoryMb: Number(mem || l?.defaultMemoryMb),
            idleMinutes: Number(idle),
            harness,
            ...(model.trim() && { model: model.trim() }),
          },
        }),
      ),
    onSuccess: async (a) => {
      await qc.invalidateQueries({
        queryKey: ['workspaces', workspaceId, 'agents'],
      });
      onClose();
      await navigate({ to: '/a/$agentId', params: { agentId: a.id } });
    },
  });

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New agent</DialogTitle>
          <DialogDescription>
            Creates a sandbox for this workspace.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
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
              value={image}
              placeholder={l?.defaultImage}
              onChange={(e) => setImage(e.target.value)}
            />
          </label>
          <div className="grid grid-cols-3 gap-3">
            <label className="grid gap-1 text-sm">
              CPUs
              <Input
                type="number"
                min={1}
                max={l?.maxCpus}
                value={cpus || (l?.defaultCpus ?? '')}
                onChange={(e) => setCpus(e.target.value)}
              />
            </label>
            <label className="grid gap-1 text-sm">
              Memory (MB)
              <Input
                type="number"
                min={128}
                max={l?.maxMemoryMb}
                value={mem || (l?.defaultMemoryMb ?? '')}
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
          <div className="grid grid-cols-2 gap-3">
            <label className="grid gap-1 text-sm">
              Harness
              <Select
                value={harness}
                onChange={(e) => setHarness(e.target.value)}
              >
                {!harnesses.data?.some((h) => h.id === 'claude-code') && (
                  <option value="claude-code">claude-code</option>
                )}
                {harnesses.data?.map((h) => (
                  <option key={h.id} value={h.id}>
                    {h.name}
                  </option>
                ))}
              </Select>
            </label>
            <label className="grid gap-1 text-sm">
              Model (optional)
              <Input
                value={model}
                placeholder="default"
                onChange={(e) => setModel(e.target.value)}
              />
            </label>
          </div>
          <ErrorText error={create.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!name.trim() || create.isPending}>
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
