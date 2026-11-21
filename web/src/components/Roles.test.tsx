import { screen, waitFor, within } from '@testing-library/react';
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
const me = (role: string, agents: Record<string, number> = { used: 0 }) => ({
  user: alice,
  role,
  admin: role === 'admin',
  agents,
});

it('shows a Disabled user only the no-access page', async () => {
  const { calls } = mockApi({
    'GET /me': me('disabled', { used: 0, max: 2 }),
    'POST /auth/logout': () => ({ status: 204 }),
    'GET /auth/providers': [],
  });
  renderApp('/');
  expect(
    await screen.findByRole('heading', { name: 'No access' }),
  ).toBeVisible();
  expect(screen.getByText(/ask an admin/i)).toBeVisible();
  expect(screen.queryByRole('link', { name: /Overview/ })).toBeNull();
  // Nothing but /me is asked for: everything else would be refused.
  expect(calls.map((c) => c.key)).toEqual(['GET /me']);
  await userEvent.click(screen.getByRole('button', { name: 'Sign out' }));
  await waitFor(() =>
    expect(calls.map((c) => c.key)).toContain('POST /auth/logout'),
  );
});

it('shows the role in the user menu and hides admin links from users', async () => {
  mockApi({
    'GET /me': me('user', { used: 1, max: 2 }),
    'GET /agents': [],
    'GET /apps': [],
  });
  renderApp('/');
  expect(await screen.findByText('User')).toBeVisible();
  expect(screen.queryByRole('link', { name: /Overview/ })).toBeNull();
});

it('says how many agents are left and blocks creating past the quota', async () => {
  mockApi({
    'GET /me': me('user', { used: 2, max: 2 }),
    'GET /agents': [],
    'GET /agents?template=true': [],
    'GET /apps': [],
    'GET /harnesses': [],
    'GET /sandbox/limits': {
      driver: 'fake',
      defaultCpus: 1,
      defaultMemoryMb: 1024,
      maxCpus: 2,
      maxMemoryMb: 2048,
      minMemoryMb: 512,
    },
  });
  renderApp('/');
  await userEvent.click(
    await screen.findByRole('button', { name: 'New agent' }),
  );
  const dialog = await screen.findByRole('dialog');
  expect(within(dialog).getByText(/2 of 2 agents used/)).toBeVisible();
  await userEvent.type(within(dialog).getByLabelText('Name'), 'x');
  expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
});

it('lets an admin override a role in the Overview', async () => {
  const bobRow = {
    user: bob,
    role: 'user',
    configRole: 'user',
    provider: 'github',
    username: 'bobby',
    agents: 1,
    local: false,
    createdAt: '2026-01-01T00:00:00Z',
  };
  const { calls } = mockApi({
    'GET /me': me('admin'),
    'GET /agents': [],
    'GET /apps': [],
    // The machines part is tested in OverviewPage.test.tsx.
    'GET /admin/overview': () => ({ status: 503, body: { error: 'later' } }),
    'GET /admin/users': [
      {
        ...bobRow,
        user: alice,
        role: 'admin',
        configRole: 'admin',
        provider: 'local',
        username: 'root',
        local: true,
      },
      bobRow,
    ],
    'PUT /admin/users/u2/role': ({ body }: { body: unknown }) => ({
      body: {
        ...bobRow,
        role: 'disabled',
        override: (body as { override: string }).override,
      },
    }),
  });
  renderApp('/overview');
  const select = await screen.findByRole('combobox', { name: 'Role of Bob' });
  expect(
    screen.getByRole('combobox', { name: 'Role of Alice' }),
  ).toBeDisabled();
  await userEvent.selectOptions(select, 'disabled');
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'PUT /admin/users/u2/role')?.body,
    ).toEqual({
      override: 'disabled',
    }),
  );
});
