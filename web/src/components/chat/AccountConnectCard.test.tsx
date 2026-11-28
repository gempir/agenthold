import { stubGlobal } from '@/test-setup';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router';
import { expect, it, vi } from 'bun:test';
import { AccountConnectCard } from './AccountConnectCard';
import type { AccountConnectItem } from '@/lib/session';
import { mockApi } from '@/test-utils';

const item = (over: Partial<AccountConnectItem> = {}): AccountConnectItem => ({
  kind: 'account_connect',
  seq: 4,
  requestId: 'acreq_1',
  toolId: 'otl_lin',
  slug: 'linear',
  name: 'Linear',
  description: 'Issue tracking',
  toolKind: 'executor_app',
  authMode: 'user_oauth',
  reason: 'To file the bug you described',
  connected: false,
  ...over,
});

const renderCard = (it: AccountConnectItem, canUse = true) => {
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <AccountConnectCard item={it} canUse={canUse} />,
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  });
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
};

const connectPath = 'POST /me/org-tools/otl_lin/connect';
const secretSession = {
  connectionId: 'con_1',
  token: 'tok_1',
  providerId: 'prv_lin',
  providerName: 'Linear',
  methods: [
    {
      name: 'apiKey',
      kind: 'secrets',
      label: 'API key',
      fields: [{ name: 'key', title: 'API key', required: true, secret: true }],
    },
  ],
};

it('connects an org tool with an inline token form from the thread', async () => {
  const { calls } = mockApi({
    'GET /org/tools': [],
    [connectPath]: {
      toolId: 'otl_lin',
      slug: 'linear',
      connected: false,
      session: secretSession,
    },
    'POST /me/executor/connections/submit': { id: 'acc_1' },
  });
  renderCard(item());
  const card = await screen.findByRole('group', { name: 'Connect Linear' });
  expect(card).toHaveTextContent('The agent wants to use Linear');
  expect(card).toHaveTextContent('To file the bug you described');
  const user = userEvent.setup();
  await user.click(
    within(card).getByRole('button', { name: 'Connect Linear' }),
  );
  const key = await within(card).findByLabelText(/API key/);
  expect(key).toHaveAttribute('type', 'password');
  await user.type(key, 'lin_api_1');
  await user.click(
    within(card).getByRole('button', { name: 'Save connection' }),
  );
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /me/executor/connections/submit')?.body,
    ).toEqual({
      connectionId: 'con_1',
      token: 'tok_1',
      method: 'apiKey',
      fields: { key: 'lin_api_1' },
    }),
  );
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Linear connected',
  );
});

it('opens the OAuth sign-in right away when that is the only method', async () => {
  const open = vi.fn();
  stubGlobal('open', open);
  mockApi({
    'GET /org/tools': [],
    [connectPath]: {
      toolId: 'otl_lin',
      slug: 'linear',
      connected: false,
      session: {
        ...secretSession,
        browserUrl: 'https://int.example.com/connect/con_1#token=tok_1',
        methods: [{ name: 'oauth', kind: 'oauth2', label: 'Sign in' }],
      },
    },
  });
  renderCard(item());
  const user = userEvent.setup();
  await user.click(
    await screen.findByRole('button', { name: 'Connect Linear' }),
  );
  await waitFor(() =>
    expect(open).toHaveBeenCalledWith(
      'https://int.example.com/connect/con_1#token=tok_1',
      '_blank',
      'noopener',
    ),
  );
  expect(screen.getByRole('link', { name: /Open sign-in/ })).toHaveAttribute(
    'href',
    'https://int.example.com/connect/con_1#token=tok_1',
  );
  await user.click(
    screen.getByRole('button', { name: 'I finished connecting' }),
  );
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Linear connected',
  );
});

it('confirms immediately when the account is already connected', async () => {
  mockApi({
    'GET /org/tools': [],
    [connectPath]: { toolId: 'otl_lin', slug: 'linear', connected: true },
  });
  renderCard(item());
  const user = userEvent.setup();
  await user.click(
    await screen.findByRole('button', { name: 'Connect Linear' }),
  );
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Linear connected',
  );
});

it('shows connected state from the event or the live tool list', async () => {
  mockApi({});
  const { unmount } = renderCard(item({ connected: true }));
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Linear connected',
  );
  unmount();
  mockApi({
    'GET /org/tools': [
      {
        id: 'otl_lin',
        slug: 'linear',
        name: 'Linear',
        connected: true,
      },
    ],
  });
  renderCard(item());
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Linear connected',
  );
});

it('points org-provided tools to Integrations instead of a Connect button', async () => {
  mockApi({ 'GET /org/tools': [] });
  renderCard(item({ authMode: 'org_shared', name: 'Sentry' }));
  const status = await screen.findByRole('status');
  expect(status).toHaveTextContent('Sentry is provided by your organization');
  expect(within(status).getByRole('link')).toHaveAttribute(
    'href',
    '/settings/integrations',
  );
  expect(screen.queryByRole('button')).toBeNull();
});

it('read-only viewers cannot connect', async () => {
  mockApi({ 'GET /org/tools': [] });
  renderCard(item(), false);
  const card = await screen.findByRole('group', { name: 'Connect Linear' });
  expect(card).toHaveTextContent('Waiting for the thread owner');
  expect(within(card).queryByRole('button')).toBeNull();
});
