import { describe, expect, it } from 'vitest';
import {
  bytes,
  filterMachines,
  mib,
  noFilter,
  percent,
  sortMachines,
  type OverviewMachine,
} from './overview';

const machine = (over: Partial<OverviewMachine>): OverviewMachine => ({
  id: 'm',
  name: 'm',
  kind: 'agent',
  isTemplate: false,
  owner: { id: 'u', name: 'Alice', email: 'alice@example.com', avatarUrl: '' },
  nodeId: 'local',
  nodeName: 'main',
  state: 'running',
  stateError: '',
  stateChangedAt: '2026-01-01T00:00:00Z',
  cpus: 2,
  memoryMb: 4096,
  createdAt: '2026-01-01T00:00:00Z',
  ...over,
});

describe('formatting', () => {
  it('formats sizes', () => {
    expect(bytes(512 * 1024 ** 2)).toBe('512 MiB');
    expect(bytes(3.5 * 1024 ** 3)).toBe('3.5 GiB');
    expect(bytes(64 * 1024 ** 3)).toBe('64 GiB');
    expect(mib(4096)).toBe('4.0 GiB');
  });
  it('has no percentage without a whole', () => {
    expect(percent(1, undefined)).toBeUndefined();
    expect(percent(1, 0)).toBeUndefined();
    expect(percent(1, 4)).toBe(25);
  });
});

describe('sortMachines', () => {
  const a = machine({
    id: 'a',
    name: 'alpha',
    usage: { sampledAt: 'x', cpuPercent: 10 },
  });
  const b = machine({
    id: 'b',
    name: 'Bravo',
    usage: { sampledAt: 'x', cpuPercent: 80 },
  });
  const c = machine({ id: 'c', name: 'charlie' });
  it('sorts by name case-insensitively', () => {
    expect(sortMachines([c, b, a], 'name', 'asc').map((m) => m.id)).toEqual([
      'a',
      'b',
      'c',
    ]);
    expect(sortMachines([a, b, c], 'name', 'desc').map((m) => m.id)).toEqual([
      'c',
      'b',
      'a',
    ]);
  });
  it('puts machines without a value last in both directions', () => {
    expect(sortMachines([c, a, b], 'cpu', 'asc').map((m) => m.id)).toEqual([
      'a',
      'b',
      'c',
    ]);
    expect(sortMachines([c, a, b], 'cpu', 'desc').map((m) => m.id)).toEqual([
      'b',
      'a',
      'c',
    ]);
  });
  it('does not change its input', () => {
    const input = [c, b, a];
    sortMachines(input, 'name', 'asc');
    expect(input.map((m) => m.id)).toEqual(['c', 'b', 'a']);
  });
});

describe('filterMachines', () => {
  const list = [
    machine({ id: '1', name: 'builder' }),
    machine({
      id: '2',
      name: 'site',
      kind: 'app',
      state: 'sleeping',
      nodeId: 'n2',
      owner: { id: 'u2', name: 'Bob', email: 'bob@corp.test', avatarUrl: '' },
    }),
  ];
  const ids = (f: Partial<typeof noFilter>) =>
    filterMachines(list, { ...noFilter, ...f }).map((m) => m.id);
  it('matches name, owner name and owner e-mail', () => {
    expect(ids({ text: 'BUILD' })).toEqual(['1']);
    expect(ids({ text: 'bob' })).toEqual(['2']);
    expect(ids({ text: 'corp.test' })).toEqual(['2']);
  });
  it('combines the filters', () => {
    expect(ids({ kind: 'app' })).toEqual(['2']);
    expect(ids({ state: 'running' })).toEqual(['1']);
    expect(ids({ node: 'n2', state: 'running' })).toEqual([]);
    expect(ids({})).toEqual(['1', '2']);
  });
});
