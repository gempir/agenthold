import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { Copy } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  agentsQuery,
  harnessesQuery,
  isOwner,
  meQuery,
  nodesQuery,
  templatesQuery,
  sandboxLimitsQuery,
  type Agent,
} from '@/api/queries';
import { cn } from '@/lib/utils';
import { quotaFull, quotaText } from '@/lib/roles';
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
import { nodeChoice, nodeLabel } from '@/lib/nodes';

export function AgentList({ agents }: { agents: Agent[] }) {
  if (agents.length === 0)
    return <p className="text-sm text-muted-foreground">No agents yet.</p>;
  return (
    <ul
      aria-label="Agents"
      className="flex flex-col divide-y overflow-hidden rounded-xl border"
    >
      {agents.map((a) => (
        <li key={a.id}>
          <Link
            to="/a/$agentId"
            params={{ agentId: a.id }}
            className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm hover:bg-accent/60"
          >
            <span className="font-medium">{a.name}</span>
            <AgentStateBadge state={a.state} sleepsAt={a.sleepsAt} />
            {a.needsRebuild && (
              <span className="text-xs text-amber-600 dark:text-amber-400">
                reset pending
              </span>
            )}
            <span className="ml-auto text-xs text-muted-foreground">
              {a.role === 'owner' ? 'yours' : `shared by ${a.owner.name}`}
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

/** Templates are never chatted with: each row opens its settings, where it is cloned. */
export function TemplateList({ templates }: { templates: Agent[] }) {
  return (
    <ul
      aria-label="Templates"
      className="flex flex-col divide-y overflow-hidden rounded-xl border"
    >
      {templates.map((a) => (
        <li key={a.id}>
          <Link
            to="/a/$agentId/settings"
            params={{ agentId: a.id }}
            search={{ tab: 'general' }}
            className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm hover:bg-accent/60"
          >
            <Copy className="size-3.5 text-muted-foreground" aria-hidden />
            <span className="font-medium">{a.name}</span>
            <AgentStateBadge state={a.state} sleepsAt={a.sleepsAt} />
            <span className="ml-auto text-xs text-muted-foreground">
              {a.memoryMb} MB · {a.cpus} CPU
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

export function AgentsSection() {
  const agents = useQuery(agentsQuery);
  const [open, setOpen] = useState(false);
  const templates = useQuery(templatesQuery).data ?? [];
  return (
    <>
      <section className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <h2 className="text-base font-semibold">Agents</h2>
          <Button size="sm" onClick={() => setOpen(true)}>
            New agent
          </Button>
        </div>
        {agents.isPending && <Loading />}
        <ErrorText error={agents.error} />
        {agents.data && <AgentList agents={agents.data} />}
        {open && <NewAgentDialog onClose={() => setOpen(false)} />}
      </section>
      {templates.length > 0 && (
        <section className="mt-6 flex flex-col gap-3">
          <div>
            <h2 className="text-base font-semibold">Templates</h2>
            <p className="text-sm text-muted-foreground">
              Stopped machines to start new agents from.
            </p>
          </div>
          <TemplateList templates={templates} />
        </section>
      )}
    </>
  );
}

export function NewAgentDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const limits = useQuery(sandboxLimitsQuery);
  const l = limits.data;
  const harnesses = useQuery(harnessesQuery);
  // '' until the user picks one: the server's default harness.
  const [picked, setHarness] = useState('');
  const harness = picked || harnesses.data?.find((h) => h.default)?.id || '';
  const [model, setModel] = useState('');
  const [name, setName] = useState('');
  // undefined until the user edits the field: the server's default.
  const [cpusEdit, setCpus] = useState<string>();
  const [memEdit, setMem] = useState<string>();
  const cpus = cpusEdit ?? (l ? String(l.defaultCpus) : '');
  const mem = memEdit ?? (l ? String(l.defaultMemoryMb) : '');
  const cpusN = Number(cpus);
  const memN = Number(mem);
  const resourcesValid =
    !!l &&
    cpus !== '' &&
    mem !== '' &&
    cpusN >= 1 &&
    cpusN <= l.maxCpus &&
    memN >= l.minMemoryMb &&
    memN <= l.maxMemoryMb;
  const [idle, setIdle] = useState('15');
  // '' is a blank agent; otherwise the id of the template to clone. Only
  // templates you own can be cloned.
  const templates = (useQuery(templatesQuery).data ?? []).filter(isOwner);
  const [templateId, setTemplateId] = useState('');
  const template = templates.find((t) => t.id === templateId);
  // Admins may pick the node. '' is automatic placement.
  const meData = useQuery(meQuery).data;
  const admin = !!meData?.admin;
  const quota = meData?.agents;
  const full = quotaFull(quota);
  const nodes = useQuery({ ...nodesQuery, enabled: admin }).data ?? [];
  const [nodePick, setNodePick] = useState('');
  const chosen = nodes.find((n) => n.id === nodePick);
  const nodeProblem = chosen ? nodeChoice(chosen, memN) : undefined;
  // With one node there is nothing to choose.
  const pickNode = !template && nodes.length > 1;

  const create = useMutation({
    mutationFn: () =>
      template
        ? unwrap(
            api.POST('/agents/{agentId}/clone', {
              params: { path: { agentId: template.id } },
              body: { name: name.trim() },
            }),
          )
        : unwrap(
            api.POST('/agents', {
              body: {
                name: name.trim(),
                cpus: cpusN,
                memoryMb: memN,
                idleMinutes: Number(idle),
                ...(harness && { harness }),
                ...(model.trim() && { model: model.trim() }),
                ...(pickNode && chosen && { nodeId: chosen.id }),
              },
            }),
          ),
    onSuccess: async (a) => {
      await qc.invalidateQueries({ queryKey: agentsQuery.queryKey });
      await qc.invalidateQueries({ queryKey: meQuery.queryKey, exact: true });
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
            Creates an agent with its own sandbox, blank or cloned from a
            template. You are its owner.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          {templates.length > 0 && (
            <label className="grid gap-1 text-sm">
              Start from
              <Select
                value={templateId}
                onChange={(e) => setTemplateId(e.target.value)}
              >
                <option value="">Blank agent</option>
                {templates.map((t) => (
                  <option key={t.id} value={t.id}>
                    Template: {t.name}
                  </option>
                ))}
              </Select>
            </label>
          )}
          <label className="grid gap-1 text-sm">
            Name
            <Input
              required
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          {template ? (
            <p className="text-sm text-muted-foreground">
              A copy of {template.name}: its files, {template.cpus} CPUs,{' '}
              {template.memoryMb} MB of memory and its harness settings. It runs
              on the same node
              {template.nodeName ? ` (${template.nodeName})` : ''}.
            </p>
          ) : (
            <>
              <div className="grid grid-cols-3 gap-3">
                <label className="grid gap-1 text-sm">
                  CPUs
                  <Input
                    type="number"
                    min={1}
                    max={l?.maxCpus}
                    value={cpus}
                    disabled={!l}
                    onChange={(e) => setCpus(e.target.value)}
                  />
                </label>
                <label className="grid gap-1 text-sm">
                  Memory (MB)
                  <Input
                    type="number"
                    min={l?.minMemoryMb}
                    max={l?.maxMemoryMb}
                    value={mem}
                    disabled={!l}
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
              {pickNode && (
                <label className="grid gap-1 text-sm">
                  Node
                  <Select
                    value={nodePick}
                    onChange={(e) => setNodePick(e.target.value)}
                  >
                    <option value="">
                      Automatic (the node with the most free memory)
                    </option>
                    {nodes.map((n) => (
                      <option
                        key={n.id}
                        value={n.id}
                        disabled={nodeChoice(n, memN).disabled}
                      >
                        {nodeLabel(n, memN)}
                      </option>
                    ))}
                  </Select>
                  {nodeProblem?.disabled && (
                    <span role="alert" className="text-destructive">
                      {nodeProblem.reason}
                    </span>
                  )}
                </label>
              )}
            </>
          )}
          {quotaText(quota) && (
            <p
              className={cn(
                'text-sm',
                full ? 'text-destructive' : 'text-muted-foreground',
              )}
            >
              {quotaText(quota)}
              {full &&
                '. Delete an agent or ask an admin for more; templates and clones count, too.'}
            </p>
          )}
          <ErrorText error={create.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={
                !name.trim() ||
                full ||
                (!template && !resourcesValid) ||
                (pickNode && !!nodeProblem?.disabled) ||
                create.isPending
              }
            >
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
