import type { components } from '@/api/schema';

export type OverviewNode = components['schemas']['OverviewNode'];
export type OverviewMachine = components['schemas']['OverviewMachine'];
export type UsageReason = NonNullable<OverviewMachine['usageReason']>;

const MIB = 1024 ** 2;
const GIB = 1024 ** 3;

/** "512 MiB", "3.5 GiB", "1.2 TiB". */
export function bytes(n: number): string {
  if (n < GIB) return `${Math.round(n / MIB)} MiB`;
  if (n < 1024 * GIB) return `${(n / GIB).toFixed(n < 10 * GIB ? 1 : 0)} GiB`;
  return `${(n / (1024 * GIB)).toFixed(1)} TiB`;
}

/** Memory configured in MiB, as "4 GiB" or "512 MiB". */
export const mib = (n: number) => bytes(n * MIB);

/** vCPUs: "2", "1.5". */
export const vcpus = (n: number) => String(Math.round(n * 100) / 100);

export const usageReasonText: Record<UsageReason, string> = {
  node_offline: 'node offline',
  not_connected: 'not connected',
  timeout: 'no answer',
  unsupported: 'not reported',
  error: 'error',
};

/** A ratio as a percentage, or undefined when the whole is unknown. */
export function percent(part: number, whole: number | undefined) {
  return whole && whole > 0 ? (part / whole) * 100 : undefined;
}

export type SortKey =
  | 'name'
  | 'kind'
  | 'owner'
  | 'node'
  | 'state'
  | 'size'
  | 'cpu'
  | 'memory'
  | 'active';
export type SortDir = 'asc' | 'desc';

/** The value a column sorts by; undefined sorts last in either direction. */
function sortValue(
  m: OverviewMachine,
  key: SortKey,
): string | number | undefined {
  switch (key) {
    case 'name':
      return m.name.toLowerCase();
    case 'kind':
      return m.kind;
    case 'owner':
      return (m.owner.name || m.owner.email).toLowerCase();
    case 'node':
      return (m.nodeName ?? '').toLowerCase();
    case 'state':
      return m.state;
    case 'size':
      return m.memoryMb * 1000 + m.cpus;
    case 'cpu':
      return m.usage?.cpuPercent;
    case 'memory':
      return m.usage?.memUsedBytes;
    case 'active': {
      const t = Date.parse(m.lastActivityAt ?? m.stateChangedAt);
      return Number.isFinite(t) ? t : undefined;
    }
  }
}

export function sortMachines(
  machines: OverviewMachine[],
  key: SortKey,
  dir: SortDir,
): OverviewMachine[] {
  const sign = dir === 'asc' ? 1 : -1;
  return [...machines].sort((a, b) => {
    const x = sortValue(a, key);
    const y = sortValue(b, key);
    if (x === undefined || y === undefined) {
      if (x === y) return a.name.localeCompare(b.name);
      return x === undefined ? 1 : -1;
    }
    const c =
      typeof x === 'number' ? x - (y as number) : x.localeCompare(String(y));
    return c !== 0 ? sign * c : a.name.localeCompare(b.name);
  });
}

export interface MachineFilter {
  text: string;
  node: string;
  state: string;
  kind: string;
}

export const noFilter: MachineFilter = {
  text: '',
  node: '',
  state: '',
  kind: '',
};

/** Text matches the machine name, owner name or owner e-mail. */
export function filterMachines(
  machines: OverviewMachine[],
  f: MachineFilter,
): OverviewMachine[] {
  const q = f.text.trim().toLowerCase();
  return machines.filter(
    (m) =>
      (!f.node || m.nodeId === f.node) &&
      (!f.state || m.state === f.state) &&
      (!f.kind || m.kind === f.kind) &&
      (!q ||
        m.name.toLowerCase().includes(q) ||
        m.owner.name.toLowerCase().includes(q) ||
        m.owner.email.toLowerCase().includes(q)),
  );
}
