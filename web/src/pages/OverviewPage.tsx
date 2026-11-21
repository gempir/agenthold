import { useMemo, useState, type ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowDown, ArrowUp, ChevronsUpDown } from 'lucide-react';
import { meQuery, overviewQuery } from '@/api/queries';
import type { components } from '@/api/schema';
import { AgentStateBadge, AgentStateDot } from '@/components/AgentStateBadge';
import { ErrorText, Loading } from '@/components/Spinner';
import { UsersAdmin } from '@/components/UsersAdmin';
import { Crumb, TopBar } from '@/components/layout/TopBar';
import { Badge } from '@/components/ui/badge';
import { Card } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import {
  bytes,
  filterMachines,
  mib,
  noFilter,
  percent,
  sortMachines,
  usageReasonText,
  vcpus,
  type MachineFilter,
  type OverviewMachine,
  type OverviewNode,
  type SortDir,
  type SortKey,
} from '@/lib/overview';
import { relTime, useNow } from '@/lib/time';
import { cn } from '@/lib/utils';

/** Everything running on this agenthold: nodes and machines. Admins only. */
export function OverviewPage() {
  const me = useQuery(meQuery);
  return (
    <>
      <TopBar>
        <Crumb current>Overview</Crumb>
      </TopBar>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="grid gap-6 px-4 py-4 md:px-8 md:py-6">
          {me.isPending ? (
            <Loading />
          ) : me.data?.admin ? (
            <>
              <OverviewContent />
              <UsersAdmin />
            </>
          ) : (
            <p className="text-sm text-muted-foreground">
              Only admins can see the overview.
            </p>
          )}
        </div>
      </div>
    </>
  );
}

function OverviewContent() {
  const overview = useQuery(overviewQuery);
  const now = useNow(15_000);
  const data = overview.data;
  if (overview.isPending) return <Loading />;
  if (!data)
    return (
      <>
        <ErrorText error={overview.error} />
      </>
    );
  return (
    <>
      {overview.error && <ErrorText error={overview.error} />}
      <Tiles overview={data} />
      <NodesTable nodes={data.nodes} now={now} />
      <MachinesTable
        machines={data.machines}
        nodes={data.nodes}
        now={now}
        stale={!!overview.error}
      />
    </>
  );
}

type Overview = components['schemas']['AdminOverview'];

/* ------------------------------------------------------------------ tiles */

function Tile({
  label,
  children,
  detail,
}: {
  label: string;
  children: ReactNode;
  detail?: ReactNode;
}) {
  return (
    <Card className="grid gap-1 p-4">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="text-2xl font-semibold tracking-tight tabular-nums">
        {children}
      </div>
      {detail && <div className="text-xs text-muted-foreground">{detail}</div>}
    </Card>
  );
}

function Tiles({ overview }: { overview: Overview }) {
  const { nodes, totals } = overview;
  const online = nodes.filter((n) => n.online);
  const cpuCapacity = online.reduce((s, n) => s + (n.cpus ?? 0), 0);
  const memCapacity = online.reduce((s, n) => s + (n.memoryTotalBytes ?? 0), 0);
  const by = totals.byState;
  const running = by.running ?? 0;
  const sleeping = by.sleeping ?? 0;
  const failing = (by.error ?? 0) + (by.unreachable ?? 0);
  const memRunning = totals.running.memoryMb * 1024 ** 2;
  return (
    <section
      aria-label="Totals"
      className="grid grid-cols-2 gap-3 lg:grid-cols-4"
    >
      <Tile
        label="Nodes online"
        detail={
          nodes.length - online.length > 0 ? 'some are offline' : undefined
        }
      >
        {online.length}
        <span className="text-base font-normal text-muted-foreground">
          {' '}
          / {nodes.length}
        </span>
      </Tile>
      <Tile
        label="Machines"
        detail={
          <>
            <span>{running} running</span> · <span>{sleeping} asleep</span> ·{' '}
            <span className={cn(failing > 0 && 'text-destructive')}>
              {failing} failing
            </span>
          </>
        }
      >
        {totals.machines}
      </Tile>
      <Tile
        label="vCPUs running"
        detail={`${vcpus(totals.allocated.cpus)} configured in total`}
      >
        {vcpus(totals.running.cpus)}
        {cpuCapacity > 0 && (
          <span className="text-base font-normal text-muted-foreground">
            {' '}
            / {cpuCapacity}
          </span>
        )}
        <Bar
          value={percent(totals.running.cpus, cpuCapacity)}
          label="vCPU allocation"
        />
      </Tile>
      <Tile
        label="Memory running"
        detail={`${mib(totals.allocated.memoryMb)} configured in total`}
      >
        {mib(totals.running.memoryMb)}
        {memCapacity > 0 && (
          <span className="text-base font-normal text-muted-foreground">
            {' '}
            / {bytes(memCapacity)}
          </span>
        )}
        <Bar
          value={percent(memRunning, memCapacity)}
          label="Memory allocation"
        />
      </Tile>
    </section>
  );
}

/* ------------------------------------------------------------------- bars */

/** A thin meter. Past 80 % it turns amber, past 100 % red. */
function Bar({
  value,
  label,
  className,
}: {
  value: number | undefined;
  label: string;
  className?: string;
}) {
  if (value === undefined) return null;
  return (
    <div
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(value)}
      className={cn(
        'mt-1 h-1.5 w-full min-w-16 overflow-hidden rounded-full bg-muted',
        className,
      )}
    >
      <div
        className={cn(
          'h-full rounded-full',
          value > 100
            ? 'bg-red-500'
            : value > 80
              ? 'bg-amber-500'
              : 'bg-emerald-500',
        )}
        style={{ width: `${Math.min(100, Math.max(value, 1))}%` }}
      />
    </div>
  );
}

/* ------------------------------------------------------------------ tables */

const th =
  'px-3 py-2 text-left text-xs font-medium whitespace-nowrap text-muted-foreground';
const td = 'px-3 py-2 align-top';

function NodesTable({ nodes, now }: { nodes: OverviewNode[]; now: number }) {
  return (
    <section aria-label="Nodes" className="grid gap-2">
      <h2 className="text-sm font-medium">Nodes</h2>
      <Card className="overflow-x-auto">
        {nodes.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">
            No nodes yet. Machines can only run once a node is connected.
          </p>
        ) : (
          <table className="w-full min-w-[48rem] border-collapse text-sm">
            <thead className="border-b">
              <tr>
                <th className={th}>Node</th>
                <th className={th}>Version</th>
                <th className={th}>Machines</th>
                <th className={th}>vCPU</th>
                <th className={th}>Memory</th>
                <th className={th}>Disk</th>
                <th className={th}>Last seen</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {nodes.map((n) => (
                <NodeRow key={n.id} node={n} now={now} />
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </section>
  );
}

function NodeRow({ node: n, now }: { node: OverviewNode; now: number }) {
  const memRunning = n.running.memoryMb * 1024 ** 2;
  return (
    <tr>
      <td className={td}>
        <div className="flex items-center gap-2">
          <span
            role="img"
            aria-label={n.online ? 'Online' : 'Offline'}
            className={cn(
              'size-2 shrink-0 rounded-full',
              n.online ? 'bg-emerald-400' : 'bg-red-400',
            )}
          />
          <span className="font-medium">{n.name}</span>
          {n.local && <Badge variant="outline">built-in</Badge>}
        </div>
        {!n.online && (
          <div className="mt-0.5 pl-4 text-xs text-destructive">Offline</div>
        )}
      </td>
      <td className={cn(td, 'text-muted-foreground')}>{n.version ?? '—'}</td>
      <td className={cn(td, 'tabular-nums')}>
        {n.running.machines} / {n.allocated.machines}
        <div className="text-xs text-muted-foreground">running / total</div>
      </td>
      <td className={td}>
        <span className="tabular-nums">
          {vcpus(n.running.cpus)}
          {n.cpus !== undefined && ` / ${n.cpus}`}
        </span>
        {n.allocated.cpus !== n.running.cpus && (
          <span className="text-xs text-muted-foreground">
            {' '}
            ({vcpus(n.allocated.cpus)} configured)
          </span>
        )}
        <Bar
          value={percent(n.running.cpus, n.cpus)}
          label={`${n.name} vCPU allocation`}
        />
      </td>
      <td className={td}>
        <span className="tabular-nums">
          {mib(n.running.memoryMb)}
          {n.memoryTotalBytes !== undefined &&
            ` / ${bytes(n.memoryTotalBytes)}`}
        </span>
        {n.allocated.memoryMb !== n.running.memoryMb && (
          <span className="text-xs text-muted-foreground">
            {' '}
            ({mib(n.allocated.memoryMb)} configured)
          </span>
        )}
        <Bar
          value={percent(memRunning, n.memoryTotalBytes)}
          label={`${n.name} memory allocation`}
        />
      </td>
      <td className={cn(td, 'whitespace-nowrap tabular-nums')}>
        {n.diskAllocatedBytes !== undefined
          ? `${bytes(n.diskAllocatedBytes)} used`
          : '—'}
        {n.freeDiskBytes !== undefined && (
          <div className="text-xs text-muted-foreground">
            {bytes(n.freeDiskBytes)} free
          </div>
        )}
      </td>
      <td
        className={cn(td, 'whitespace-nowrap text-muted-foreground')}
        title={n.lastSeen ? new Date(n.lastSeen).toLocaleString() : undefined}
      >
        {n.online
          ? 'now'
          : n.lastSeen
            ? `${relTime(n.lastSeen, now)} ago`
            : '—'}
      </td>
    </tr>
  );
}

const COLUMNS: { key: SortKey; label: string }[] = [
  { key: 'name', label: 'Machine' },
  { key: 'kind', label: 'Kind' },
  { key: 'owner', label: 'Owner' },
  { key: 'node', label: 'Node' },
  { key: 'state', label: 'State' },
  { key: 'size', label: 'Size' },
  { key: 'cpu', label: 'CPU' },
  { key: 'memory', label: 'Memory' },
  { key: 'active', label: 'Last active' },
];

function MachinesTable({
  machines,
  nodes,
  now,
  stale,
}: {
  machines: OverviewMachine[];
  nodes: OverviewNode[];
  now: number;
  stale: boolean;
}) {
  const [filter, setFilter] = useState<MachineFilter>(noFilter);
  const [sort, setSort] = useState<{ key: SortKey; dir: SortDir }>({
    key: 'name',
    dir: 'asc',
  });
  const rows = useMemo(
    () => sortMachines(filterMachines(machines, filter), sort.key, sort.dir),
    [machines, filter, sort],
  );
  const states = useMemo(
    () => [...new Set(machines.map((m) => m.state))].sort(),
    [machines],
  );
  const set = (patch: Partial<MachineFilter>) =>
    setFilter((f) => ({ ...f, ...patch }));
  const toggle = (key: SortKey) =>
    setSort((s) =>
      s.key === key
        ? { key, dir: s.dir === 'asc' ? 'desc' : 'asc' }
        : {
            key,
            dir:
              key === 'cpu' || key === 'memory' || key === 'active'
                ? 'desc'
                : 'asc',
          },
    );

  return (
    <section aria-label="Machines" className="grid gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="mr-2 text-sm font-medium">Machines</h2>
        <Input
          type="search"
          aria-label="Filter by name or owner"
          placeholder="Filter by name or owner"
          value={filter.text}
          onChange={(e) => set({ text: e.target.value })}
          className="h-9 w-56"
        />
        <Select
          aria-label="Node"
          value={filter.node}
          onChange={(e) => set({ node: e.target.value })}
        >
          <option value="">All nodes</option>
          {nodes.map((n) => (
            <option key={n.id} value={n.id}>
              {n.name}
            </option>
          ))}
        </Select>
        <Select
          aria-label="State"
          value={filter.state}
          onChange={(e) => set({ state: e.target.value })}
        >
          <option value="">All states</option>
          {states.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </Select>
        <Select
          aria-label="Kind"
          value={filter.kind}
          onChange={(e) => set({ kind: e.target.value })}
        >
          <option value="">Agents and apps</option>
          <option value="agent">Agents</option>
          <option value="app">Apps</option>
        </Select>
        <span
          className="ml-auto text-xs text-muted-foreground"
          aria-live="polite"
        >
          {rows.length === machines.length
            ? `${machines.length} machines`
            : `${rows.length} of ${machines.length} machines`}
          {stale && ' · showing the last result'}
        </span>
      </div>
      <Card className="overflow-x-auto">
        {machines.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">No machines yet.</p>
        ) : rows.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">
            No machines match the filter.
          </p>
        ) : (
          <table className="w-full min-w-[64rem] border-collapse text-sm">
            <thead className="border-b">
              <tr>
                {COLUMNS.map((c) => (
                  <th
                    key={c.key}
                    scope="col"
                    className={th}
                    aria-sort={
                      sort.key === c.key
                        ? sort.dir === 'asc'
                          ? 'ascending'
                          : 'descending'
                        : 'none'
                    }
                  >
                    <button
                      type="button"
                      onClick={() => toggle(c.key)}
                      className="inline-flex items-center gap-1 hover:text-foreground"
                    >
                      {c.label}
                      {sort.key !== c.key ? (
                        <ChevronsUpDown
                          className="size-3 opacity-50"
                          aria-hidden
                        />
                      ) : sort.dir === 'asc' ? (
                        <ArrowUp className="size-3" aria-hidden />
                      ) : (
                        <ArrowDown className="size-3" aria-hidden />
                      )}
                    </button>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y">
              {rows.map((m) => (
                <MachineRow key={m.id} machine={m} now={now} />
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </section>
  );
}

function MachineLink({ m }: { m: OverviewMachine }) {
  const cls = 'font-medium hover:underline';
  return m.kind === 'app' ? (
    <Link to="/apps/$appId" params={{ appId: m.id }} className={cls}>
      {m.name}
    </Link>
  ) : (
    <Link to="/a/$agentId" params={{ agentId: m.id }} className={cls}>
      {m.name}
    </Link>
  );
}

function MachineRow({
  machine: m,
  now,
}: {
  machine: OverviewMachine;
  now: number;
}) {
  const u = m.usage;
  const reason = m.usageReason ? usageReasonText[m.usageReason] : undefined;
  const memPct = u?.memTotalBytes
    ? percent(u.memUsedBytes ?? 0, u.memTotalBytes)
    : undefined;
  const active = m.lastActivityAt ?? m.stateChangedAt;
  return (
    <tr>
      <td className={td}>
        <div className="flex items-center gap-2">
          <AgentStateDot state={m.state} />
          <MachineLink m={m} />
          {m.isTemplate && <Badge variant="outline">template</Badge>}
        </div>
        {(m.harness || m.model) && (
          <div className="mt-0.5 pl-5 text-xs text-muted-foreground">
            {[m.harness, m.model].filter(Boolean).join(' · ')}
          </div>
        )}
      </td>
      <td className={td}>
        <Badge variant={m.kind === 'app' ? 'outline' : 'secondary'}>
          {m.kind}
        </Badge>
      </td>
      <td className={td}>
        <div>{m.owner.name || m.owner.email}</div>
        {m.owner.name && (
          <div className="text-xs text-muted-foreground">{m.owner.email}</div>
        )}
      </td>
      <td className={cn(td, 'whitespace-nowrap')}>{m.nodeName ?? '—'}</td>
      <td className={td}>
        <div className="flex flex-wrap items-center gap-1">
          <AgentStateBadge state={m.state} />
        </div>
        {m.state === 'error' && m.stateError && (
          <div className="mt-1 max-w-56 text-xs break-words text-destructive">
            {m.stateError}
          </div>
        )}
      </td>
      <td className={cn(td, 'whitespace-nowrap tabular-nums')}>
        {vcpus(m.cpus)} vCPU · {mib(m.memoryMb)}
        <div className="text-xs text-muted-foreground">
          {u?.homeTotalBytes !== undefined
            ? `disk ${bytes(u.homeUsedBytes ?? 0)} / ${bytes(u.homeTotalBytes)}`
            : 'disk —'}
        </div>
      </td>
      <td className={cn(td, 'whitespace-nowrap tabular-nums')}>
        {u?.cpuPercent !== undefined ? (
          <>
            {Math.round(u.cpuPercent)}%
            <Bar value={u.cpuPercent} label={`${m.name} CPU`} />
          </>
        ) : (
          <span className="text-muted-foreground" title={reason}>
            —
          </span>
        )}
      </td>
      <td className={cn(td, 'whitespace-nowrap tabular-nums')}>
        {u?.memTotalBytes !== undefined ? (
          <>
            {bytes(u.memUsedBytes ?? 0)} / {bytes(u.memTotalBytes)}
            <Bar value={memPct} label={`${m.name} memory`} />
          </>
        ) : (
          <span className="text-xs text-muted-foreground">{reason ?? '—'}</span>
        )}
      </td>
      <td
        className={cn(td, 'whitespace-nowrap text-muted-foreground')}
        title={new Date(active).toLocaleString()}
      >
        {relTime(active, now)}
        {!m.lastActivityAt && m.state === 'sleeping' && (
          <div className="text-xs">asleep since</div>
        )}
      </td>
    </tr>
  );
}
