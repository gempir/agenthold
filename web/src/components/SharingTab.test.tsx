import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router';
import { expect, it } from 'bun:test';
import { mockApi } from '@/test-utils';
import { SharingTab } from './SharingTab';

const alice = { id: 'u1', name: 'Alice', email: 'a@x', avatarUrl: '' };
const bob = { id: 'u2', name: 'Bob', email: 'b@x', avatarUrl: '' };

function renderTab(isOwner: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <SharingTab agentId="ag1" isOwner={isOwner} />,
    }),
    history: createMemoryHistory(),
  });
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

it('lists access and shares by email', async () => {
  const { calls } = mockApi({
    'GET /me': { user: alice },
    'GET /agents/ag1/shares': { owner: alice, shares: [] },
    'POST /agents/ag1/shares': { user: bob, createdAt: '2026-01-01T00:00:00Z' },
  });
  renderTab(true);
  const people = await screen.findByRole('list', {
    name: 'People with access',
  });
  expect(people).toHaveTextContent('Alice');
  expect(people).toHaveTextContent('owner');

  const user = userEvent.setup();
  await user.type(screen.getByRole('textbox', { name: 'Email' }), 'b@x');
  await user.click(screen.getByRole('button', { name: 'Share' }));
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /agents/ag1/shares')?.body,
    ).toEqual({ email: 'b@x' }),
  );
});

it('lets a shared user leave but not share or remove others', async () => {
  const carol = { id: 'u3', name: 'Carol', email: 'c@x', avatarUrl: '' };
  mockApi({
    'GET /me': { user: bob },
    'GET /agents/ag1/shares': {
      owner: alice,
      shares: [
        { user: bob, createdAt: '2026-01-01T00:00:00Z' },
        { user: carol, createdAt: '2026-01-01T00:00:00Z' },
      ],
    },
  });
  renderTab(false);
  await screen.findByRole('list', { name: 'People with access' });
  expect(
    await screen.findByRole('button', { name: 'Leave' }),
  ).toBeInTheDocument();
  expect(
    screen.queryByRole('button', { name: 'Remove Carol' }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole('button', { name: 'Share' }),
  ).not.toBeInTheDocument();
});
