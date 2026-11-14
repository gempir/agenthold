import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, it } from 'vitest';
import { mockApi } from '@/test-utils';
import { SharingTab } from './SharingTab';

const alice = { id: 'u1', name: 'Alice', email: 'a@x', avatarUrl: '' };
const bob = { id: 'u2', name: 'Bob', email: 'b@x', avatarUrl: '' };

function renderTab(admin: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <SharingTab agentId="ag1" workspaceId="ws1" admin={admin} />
    </QueryClientProvider>,
  );
}

it('lists effective access and shares with a member', async () => {
  const { calls } = mockApi({
    'GET /agents/ag1/grants': {
      grants: [],
      effective: [{ user: alice, role: 'admin', via: 'owner' }],
    },
    'GET /workspaces/ws1/members': [
      { user: alice, role: 'owner', joinedAt: '2026-01-01T00:00:00Z' },
      { user: bob, role: 'member', joinedAt: '2026-01-01T00:00:00Z' },
    ],
    'POST /agents/ag1/grants': {
      id: 'g1',
      subjectType: 'user',
      user: bob,
      role: 'operator',
      createdAt: '2026-01-01T00:00:00Z',
    },
  });
  renderTab(true);
  const people = await screen.findByRole('list', {
    name: 'People with access',
  });
  expect(people).toHaveTextContent('Alice');
  expect(people).toHaveTextContent('Owner');

  const user = userEvent.setup();
  await waitFor(() =>
    expect(screen.getByRole('option', { name: /Bob/ })).toBeInTheDocument(),
  );
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'With' }),
    'u2',
  );
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Role' }),
    'operator',
  );
  await user.click(screen.getByRole('button', { name: 'Share' }));
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /agents/ag1/grants')?.body,
    ).toEqual({
      subjectType: 'user',
      userId: 'u2',
      role: 'operator',
    }),
  );
});

it('hides the share form for non-admins', async () => {
  mockApi({
    'GET /agents/ag1/grants': {
      grants: [],
      effective: [{ user: alice, role: 'admin', via: 'owner' }],
    },
  });
  renderTab(false);
  await screen.findByRole('list', { name: 'People with access' });
  expect(
    screen.queryByRole('button', { name: 'Share' }),
  ).not.toBeInTheDocument();
});
