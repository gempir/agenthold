import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { mockApi, renderApp } from '@/test-utils';

afterEach(() => vi.unstubAllGlobals());

const preview = {
  workspaceId: 'ws_acme',
  workspaceName: 'Acme',
  role: 'member',
  invitedBy: 'Alice',
  expiresAt: '2030-01-01T00:00:00Z',
  alreadyMember: false,
};
const ws = {
  id: 'w1',
  name: 'Acme',
  personal: false,
  role: 'member',
  createdAt: '2030-01-01T00:00:00Z',
};
const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
  personalWorkspaceId: 'p1',
};

it('shows the preview and accepts the invite', async () => {
  const { calls } = mockApi({
    'GET /invites/tok123': preview,
    'POST /invites/tok123/accept': ws,
    'GET /me': me,
    'GET /workspaces': [ws],
    'GET /workspaces/w1': ws,
    'GET /workspaces/w1/members': [],
  });
  const { router } = renderApp('/invite/tok123');
  expect(await screen.findByText('Acme')).toBeInTheDocument();
  expect(screen.getByText('Alice')).toBeInTheDocument();
  await userEvent.click(screen.getByRole('button', { name: 'Join workspace' }));
  await waitFor(() => expect(router.state.location.pathname).toBe('/w/w1'));
  expect(calls.some((c) => c.key === 'POST /invites/tok123/accept')).toBe(true);
});

it('redirects to login on 401', async () => {
  mockApi({
    'GET /invites/tok123': () => ({
      status: 401,
      body: { error: 'unauthorized' },
    }),
  });
  const { router } = renderApp('/invite/tok123');
  await waitFor(() => expect(router.state.location.pathname).toBe('/login'));
  expect(router.state.location.search).toMatchObject({
    redirect: '/invite/tok123',
  });
});

it('links to the workspace when already a member', async () => {
  mockApi({
    'GET /invites/tok123': { ...preview, alreadyMember: true },
    'GET /workspaces': [ws],
  });
  renderApp('/invite/tok123');
  expect(
    await screen.findByRole('link', { name: 'Open Acme' }),
  ).toHaveAttribute('href', '/w/ws_acme');
  expect(
    screen.queryByRole('button', { name: 'Join workspace' }),
  ).not.toBeInTheDocument();
});
