import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import { meQuery, nodesQuery } from '@/api/queries';
import type { components } from '@/api/schema';
import { ErrorText, Loading } from '@/components/Spinner';
import { SettingsHeader } from '@/components/layout/SettingsLayout';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

type NodeInfo = components['schemas']['NodeInfo'];
type JoinToken = components['schemas']['NodeJoinToken'];

function bytes(value: number | undefined) {
  if (value === undefined) return 'Not reported';
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(0)} MiB`;
  return `${(value / 1024 ** 3).toFixed(1)} GiB`;
}

/** Machines that run agents. Only admins manage them. */
export function NodesPage() {
  const me = useQuery(meQuery);
  if (me.isPending) return <Loading />;
  if (!me.data?.admin)
    return (
      <>
        <SettingsHeader title="Nodes" />
        <p className="text-sm text-muted-foreground">
          Only admins manage nodes.
        </p>
      </>
    );
  return <NodesAdmin />;
}

function NodesAdmin() {
  const qc = useQueryClient();
  const nodes = useQuery(nodesQuery);
  const [name, setName] = useState('');
  const [created, setCreated] = useState<JoinToken>();
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/nodes/join-tokens', {
          body: name.trim() ? { name: name.trim() } : {},
        }),
      ),
    onSuccess: (t) => {
      setCreated(t);
      setName('');
    },
  });
  const remove = useMutation({
    mutationFn: ({ id, force }: { id: string; force: boolean }) =>
      unwrap(
        api.DELETE('/nodes/{nodeId}', {
          params: { path: { nodeId: id }, query: force ? { force } : {} },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: nodesQuery.queryKey }),
  });

  return (
    <>
      <SettingsHeader
        title="Nodes"
        description="Machines that run agent sandboxes. New agents go to the connected node with the most free memory and stay there."
      />
      <section className="mb-6 grid gap-3" aria-label="Add a node">
        <h2 className="text-sm font-medium">Add an edge node</h2>
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <label className="grid gap-1 text-sm">
            Name (optional)
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="its hostname"
            />
          </label>
          <Button type="submit" disabled={create.isPending}>
            Create join token
          </Button>
        </form>
        {created && (
          <div
            className="rounded-lg border border-amber-500/50 bg-amber-500/10 p-3 text-sm"
            role="status"
          >
            Run this as root on a Linux host with KVM, with the same agenthold
            version. The token works once, until{' '}
            {new Date(created.expiresAt).toLocaleString()}, and is not shown
            again:
            <code className="mt-2 block break-all rounded bg-background p-2 font-mono text-xs">
              {created.command}
            </code>
            <Button
              className="mt-2"
              size="sm"
              variant="outline"
              onClick={() =>
                void navigator.clipboard?.writeText(created.command)
              }
            >
              Copy
            </Button>
          </div>
        )}
      </section>
      <ErrorText error={create.error ?? remove.error ?? nodes.error} />
      {nodes.isPending && <Loading />}
      {nodes.data?.length === 0 && (
        <p className="text-sm text-muted-foreground">
          No nodes yet. Agents can only run once a node is connected.
        </p>
      )}
      {nodes.data && nodes.data.length > 0 && (
        <ul aria-label="Nodes" className="grid gap-3">
          {nodes.data.map((n) => (
            <NodeItem
              key={n.id}
              node={n}
              busy={remove.isPending}
              onRemove={(force) => remove.mutate({ id: n.id, force })}
            />
          ))}
        </ul>
      )}
    </>
  );
}

function NodeItem({
  node,
  busy,
  onRemove,
}: {
  node: NodeInfo;
  busy: boolean;
  onRemove: (force: boolean) => void;
}) {
  const reported =
    node.cpus !== undefined ||
    node.availableMemoryBytes !== undefined ||
    node.freeDiskBytes !== undefined;
  const forget = node.agents > 0 && !node.online;
  return (
    <li className="rounded-xl border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <strong>{node.name}</strong>
        {node.arch && (
          <span className="rounded border px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
            {node.arch}
          </span>
        )}
        <span className="text-xs text-muted-foreground">
          {node.local ? 'built-in' : node.id}
        </span>
        <span
          className={
            node.online
              ? 'text-sm text-emerald-700 dark:text-emerald-400'
              : 'text-sm text-destructive'
          }
        >
          {node.online ? 'Online' : 'Offline'}
        </span>
        {!node.local && (
          <Button
            className="ml-auto"
            size="sm"
            variant="ghost"
            disabled={busy || (node.agents > 0 && node.online)}
            title={
              node.agents > 0 && node.online
                ? 'Delete or move its agents first'
                : undefined
            }
            onClick={() => {
              const question = forget
                ? `${node.name} is offline and hosts ${node.agents} agent(s). Forget the node and delete those agents? Their disks stay on that machine.`
                : `Remove ${node.name}? It can only come back with a new join token.`;
              if (confirm(question)) onRemove(forget);
            }}
          >
            {forget ? 'Forget node and its agents' : 'Remove'}
          </Button>
        )}
      </div>
      {!node.online && reported && (
        <p className="mt-2 text-xs text-muted-foreground">
          Showing the last reported capacity.
        </p>
      )}
      <dl className="mt-3 grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1 text-sm">
        <dt className="text-muted-foreground">Agents</dt>
        <dd>{node.agents}</dd>
        <dt className="text-muted-foreground">CPUs</dt>
        <dd>{node.cpus ?? 'Not reported'}</dd>
        <dt className="text-muted-foreground">Available memory</dt>
        <dd>{bytes(node.availableMemoryBytes)}</dd>
        <dt className="text-muted-foreground">Free disk</dt>
        <dd>{bytes(node.freeDiskBytes)}</dd>
        <dt className="text-muted-foreground">Last seen</dt>
        <dd>
          {node.lastSeen
            ? new Date(node.lastSeen).toLocaleString()
            : 'Not reported'}
        </dd>
      </dl>
    </li>
  );
}
