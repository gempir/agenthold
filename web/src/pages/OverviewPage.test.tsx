import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it } from 'vitest';
import { mockApi, renderApp } from '@/test-utils';

const alice = {
  id: 'u1',
  name: 'Alice',
  email: 'a@example.com',
  avatarUrl: '',
};
const bob = { id: 'u2', name: 'Bob', email: 'bob@example.com', avatarUrl: '' };
const admin = { admin: true, user: alice };

const machine = (over: Record<string, unknown>) => ({
  id: 'm',
  name: 'm',
  kind: 'agent',
  isTemplate: false,
  owner: alice,
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

const overview = {
  generatedAt: '2026-01-01T00:00:00Z',
  nodes: [
    {
      id: 'local',
      name: 'main',
      online: true,
      local: true,
      version: 'v1.2.3',
      cpus: 8,
      memoryTotalBytes: 16 * 1024 ** 3,
      diskAllocatedBytes: 3 * 1024 ** 3,
      freeDiskBytes: 100 * 1024 ** 3,
      allocated: { machines: 3, cpus: 5, memoryMb: 10240 },
      running: { machines: 2, cpus: 3, memoryMb: 6144 },
    },
    {
      id: 'n2',
      name: 'edge',
      online: false,
      local: false,
      cpus: 4,
      lastSeen: '2025-12-31T00:00:00Z',
      allocated: { machines: 1, cpus: 2, memoryMb: 2048 },
      running: { machines: 0, cpus: 0, memoryMb: 0 },
    },
  ],
  machines: [
    machine({
      id: 'a1',
      name: 'builder',
      harness: 'claude-code',
      model: 'opus',
      usage: {
        sampledAt: '2026-01-01T00:00:00Z',
        cpuPercent: 42,
        memUsedBytes: 1024 ** 3,
        memTotalBytes: 4 * 1024 ** 3,
        homeUsedBytes: 2 * 1024 ** 3,
        homeTotalBytes: 10 * 1024 ** 3,
      },
    }),
    machine({
      id: 'p1',
      name: 'site',
      kind: 'app',
      owner: bob,
      memoryMb: 1024,
      cpus: 1,
      state: 'running',
      usageReason: 'timeout',
    }),
    machine({
      id: 'z1',
      name: 'napper',
      owner: bob,
      state: 'sleeping',
      memoryMb: 2048,
    }),
    machine({
      id: 'e1',
      name: 'remote-one',
      nodeId: 'n2',
      nodeName: 'edge',
      state: 'unreachable',
      usageReason: 'node_offline',
    }),
  ],
  totals: {
    machines: 4,
    byState: { running: 2, sleeping: 1, unreachable: 1 },
    allocated: { machines: 4, cpus: 7, memoryMb: 12288 },
    running: { machines: 2, cpus: 3, memoryMb: 6144 },
  },
};

const names = () =>
  within(screen.getByRole('region', { name: 'Machines' }))
    .getAllByRole('row')
    .slice(1)
    .map((r) => within(r).getAllByRole('cell')[0].textContent ?? '');

it('shows totals, nodes and machines for an admin', async () => {
  mockApi({
    'GET /me': admin,
    'GET /admin/overview': overview,
    'GET /agents': [],
    'GET /apps': [],
  });
  renderApp('/overview');
  const totals = await screen.findByRole('region', { name: 'Totals' });
  expect(within(totals).getByText(/2 running/)).toBeVisible();
  expect(within(totals).getByText(/1 failing/)).toBeVisible();
  expect(within(totals).getByText('Nodes online')).toBeVisible();

  const nodes = screen.getByRole('region', { name: 'Nodes' });
  const [main, edge] = within(nodes).getAllByRole('row').slice(1);
  expect(within(main).getByText('built-in')).toBeVisible();
  expect(within(main).getByText('v1.2.3')).toBeVisible();
  expect(
    within(main).getByRole('progressbar', { name: 'main vCPU allocation' }),
  ).toHaveAttribute('aria-valuenow', '38');
  expect(within(main).getByText('3.0 GiB used')).toBeVisible();
  expect(within(main).getByText('100 GiB free')).toBeVisible();
  expect(within(edge).getByRole('img', { name: 'Offline' })).toBeVisible();

  const rows = within(screen.getByRole('region', { name: 'Machines' }))
    .getAllByRole('row')
    .slice(1);
  expect(rows).toHaveLength(4);
  const builder = rows.find((r) => within(r).queryByText('builder'))!;
  expect(
    within(builder).getByRole('link', { name: 'builder' }),
  ).toHaveAttribute('href', '/a/a1');
  expect(within(builder).getByText('claude-code · opus')).toBeVisible();
  expect(within(builder).getByText('42%')).toBeVisible();
  expect(
    within(builder).getByText('2.0 GiB / 10 GiB', { exact: false }),
  ).toBeVisible();
  const site = rows.find((r) => within(r).queryByText('site'))!;
  expect(within(site).getByRole('link', { name: 'site' })).toHaveAttribute(
    'href',
    '/apps/p1',
  );
  expect(within(site).getByText('app')).toBeVisible();
  expect(within(site).getByText('no answer')).toBeVisible();
  const remote = rows.find((r) => within(r).queryByText('remote-one'))!;
  expect(within(remote).getByText('Node offline')).toBeVisible();
  expect(within(remote).getByText('node offline')).toBeVisible();
});

it('sorts and filters machines', async () => {
  mockApi({
    'GET /me': admin,
    'GET /admin/overview': overview,
    'GET /agents': [],
    'GET /apps': [],
  });
  renderApp('/overview');
  await screen.findByRole('region', { name: 'Machines' });
  expect(names().map((n) => n.split(/claude/)[0])).toEqual([
    'builder',
    'napper',
    'remote-one',
    'site',
  ]);

  await userEvent.click(screen.getByRole('button', { name: /^Machine/ }));
  expect(names()[0]).toMatch(/^site/);
  await userEvent.click(screen.getByRole('button', { name: /^CPU/ }));
  expect(names()[0]).toMatch(/^builder/);

  await userEvent.type(screen.getByLabelText('Filter by name or owner'), 'bob');
  expect(names()).toHaveLength(2);
  await userEvent.selectOptions(screen.getByLabelText('Kind'), 'app');
  expect(names()).toEqual(['site']);
  await userEvent.selectOptions(screen.getByLabelText('State'), 'sleeping');
  expect(screen.getByText('No machines match the filter.')).toBeVisible();
});

it('filters by node', async () => {
  mockApi({
    'GET /me': admin,
    'GET /admin/overview': overview,
    'GET /agents': [],
    'GET /apps': [],
  });
  renderApp('/overview');
  await screen.findByRole('region', { name: 'Machines' });
  await userEvent.selectOptions(screen.getByLabelText('Node'), 'n2');
  expect(names()).toEqual(['remote-one']);
});

it('has empty and error states', async () => {
  mockApi({
    'GET /me': admin,
    'GET /agents': [],
    'GET /apps': [],
    'GET /admin/overview': {
      ...overview,
      nodes: [],
      machines: [],
      totals: {
        machines: 0,
        byState: {},
        allocated: { machines: 0, cpus: 0, memoryMb: 0 },
        running: { machines: 0, cpus: 0, memoryMb: 0 },
      },
    },
  });
  renderApp('/overview');
  expect(await screen.findByText('No machines yet.')).toBeVisible();
  expect(screen.getByText(/No nodes yet/)).toBeVisible();
});

it('shows the error when the overview cannot be loaded', async () => {
  mockApi({
    'GET /me': admin,
    'GET /agents': [],
    'GET /apps': [],
    'GET /admin/overview': () => ({
      status: 500,
      body: { error: 'database is down' },
    }),
  });
  renderApp('/overview');
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'database is down',
  );
});

it('tells non-admins they cannot see it, without asking for data', async () => {
  const api = mockApi({
    'GET /me': { ...admin, admin: false },
    'GET /agents': [],
    'GET /apps': [],
  });
  renderApp('/overview');
  expect(
    await screen.findByText('Only admins can see the overview.'),
  ).toBeVisible();
  expect(api.calls.some((c) => c.key === 'GET /admin/overview')).toBe(false);
  expect(screen.queryByRole('link', { name: 'Overview' })).toBeNull();
});

it('lists the overview in the settings for admins', async () => {
  mockApi({
    'GET /me': admin,
    'GET /admin/overview': overview,
    'GET /agents': [],
    'GET /apps': [],
  });
  renderApp('/overview');
  expect(await screen.findByRole('link', { name: 'Overview' })).toHaveAttribute(
    'href',
    '/settings/overview',
  );
});
