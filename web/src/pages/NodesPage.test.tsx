import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { mockApi, renderApp } from '@/test-utils';

afterEach(() => vi.unstubAllGlobals());

const admin = {
  admin: true,
  user: { id: 'u1', name: 'Alice', email: 'a@example.com', avatarUrl: '' },
};

it('shows online capacity and an offline node without invented capacity', async () => {
  mockApi({
    'GET /me': admin,
    'GET /nodes': [
      {
        id: 'local',
        name: 'Main host',
        online: true,
        local: true,
        agents: 2,
        cpus: 8,
        availableMemoryBytes: 2147483648,
        freeDiskBytes: 10737418240,
        lastSeen: '2030-01-01T00:00:00Z',
      },
      {
        id: 'node_1',
        name: 'Remote host',
        online: false,
        local: false,
        agents: 0,
      },
    ],
  });
  renderApp('/settings/nodes');
  const list = await screen.findByRole('list', { name: 'Nodes' });
  const items = within(list).getAllByRole('listitem');
  expect(within(items[0]).getByText('Main host')).toBeVisible();
  expect(within(items[0]).getByText('Online')).toBeVisible();
  expect(within(items[0]).getByText('2.0 GiB')).toBeVisible();
  expect(within(items[0]).getByText('10.0 GiB')).toBeVisible();
  expect(within(items[0]).queryByRole('button')).toBeNull();
  expect(within(items[1]).getByText('Offline')).toBeVisible();
  expect(within(items[1]).getAllByText('Not reported')).toHaveLength(4);
  expect(
    within(items[1]).getByRole('button', { name: 'Remove' }),
  ).toBeEnabled();
});

it('creates a join token and shows the command once', async () => {
  const api = mockApi({
    'GET /me': admin,
    'GET /nodes': [],
    'POST /nodes/join-tokens': () => ({
      status: 201,
      body: {
        token: 'ahnode1_abc',
        expiresAt: '2030-01-01T00:00:00Z',
        command: 'agenthold node --join ahnode1_abc',
      },
    }),
  });
  renderApp('/settings/nodes');
  await userEvent.type(
    await screen.findByLabelText('Name (optional)'),
    'edge-1',
  );
  await userEvent.click(
    screen.getByRole('button', { name: 'Create join token' }),
  );
  expect(
    await screen.findByText('agenthold node --join ahnode1_abc'),
  ).toBeVisible();
  expect(
    api.calls.find((c) => c.key === 'POST /nodes/join-tokens')?.body,
  ).toEqual({
    name: 'edge-1',
  });
});

it('forgets an offline node with agents only after confirmation', async () => {
  vi.stubGlobal('confirm', () => true);
  const api = mockApi({
    'GET /me': admin,
    'GET /nodes': [
      { id: 'node_1', name: 'Gone', online: false, local: false, agents: 3 },
      { id: 'node_2', name: 'Busy', online: true, local: false, agents: 1 },
    ],
    'DELETE /nodes/node_1': () => ({ status: 204 }),
  });
  renderApp('/settings/nodes');
  const list = await screen.findByRole('list', { name: 'Nodes' });
  const [gone, busy] = within(list).getAllByRole('listitem');
  expect(within(busy).getByRole('button', { name: 'Remove' })).toBeDisabled();
  await userEvent.click(
    within(gone).getByRole('button', { name: 'Forget node and its agents' }),
  );
  await waitFor(() =>
    expect(api.calls.some((c) => c.key === 'DELETE /nodes/node_1')).toBe(true),
  );
});

it('tells non-admins that only admins manage nodes', async () => {
  mockApi({ 'GET /me': { ...admin, admin: false } });
  renderApp('/settings/nodes');
  expect(await screen.findByText('Only admins manage nodes.')).toBeVisible();
});
