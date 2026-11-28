import { stubGlobal } from '@/test-setup';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it } from 'bun:test';
import type { Agent, ExecutorOverview, ExecutorStatus } from '@/api/queries';
import { mockApi, renderApp } from '@/test-utils';

const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
};

const statusReady = (over: Partial<ExecutorStatus> = {}): ExecutorStatus => ({
  available: true,
  enabled: true,
  running: true,
  version: '2.0.0-beta.79',
  dashboardUrl: 'https://executor-demo.agenthold.dev',
  oauthReady: true,
  customAppsEnabled: false,
  ...over,
});

const sampleOverview = (
  over: Partial<ExecutorOverview> = {},
): ExecutorOverview => ({
  status: statusReady(),
  apps: [
    {
      id: 'app_gh',
      slug: 'github',
      name: 'GitHub',
      activeVersion: 'dpl_1',
      requirements: [
        {
          name: 'github',
          scope: 'user',
          providerId: 'prv_gh',
          providerName: 'GitHub',
          many: false,
          authMethods: [
            {
              name: 'pat',
              kind: 'secrets',
              label: 'Personal Access Token',
              guide: 'Create a fine-grained PAT.',
              fields: [
                {
                  name: 'token',
                  title: 'Token',
                  required: true,
                  secret: true,
                },
              ],
            },
          ],
          linkedAccountIds: ['acc_gh'],
        },
      ],
    },
  ],
  accounts: [
    {
      id: 'acc_gh',
      providerId: 'prv_gh',
      providerName: 'GitHub',
      label: 'Personal',
      description: 'My GitHub account',
      method: 'pat',
      status: 'ready',
      identity: 'octocat',
      linkedApps: ['GitHub'],
    },
  ],
  ...over,
});

it('shows Executor apps and accounts, checks health, installs from catalog, and connects a secret account', async () => {
  let overview = sampleOverview();
  const { calls } = mockApi({
    'GET /me': me,
    'GET /me/executor': () => ({ body: overview.status }),
    'GET /me/executor/overview': () => ({ body: overview }),
    'GET /me/executor/catalog': {
      apps: [
        {
          id: 'linear',
          name: 'Linear',
          description: 'Issue tracking',
          category: 'productivity',
          auth: ['apiKey'],
        },
      ],
    },
    'POST /me/executor/catalog/import': () => {
      overview = {
        ...overview,
        apps: [
          ...overview.apps,
          {
            id: 'app_lin',
            slug: 'linear',
            name: 'Linear',
            requirements: [],
          },
        ],
      };
      return { status: 201, body: overview.apps[overview.apps.length - 1] };
    },
    'POST /me/executor/accounts/acc_gh/health': {
      status: 'ok',
      message: 'Token valid',
    },
    'POST /me/executor/apps/app_gh/accounts/user/github/connect': () => ({
      status: 201,
      body: {
        connectionId: 'con_1',
        token: 'tok_abc',
        browserUrl:
          'https://executor-demo.agenthold.dev/account-connect/con_1#token=tok_abc',
        providerId: 'prv_gh',
        providerName: 'GitHub',
        methods: overview.apps[0].requirements[0].authMethods,
      },
    }),
    'POST /me/executor/connections/submit': () => {
      const created = {
        id: 'acc_work',
        providerId: 'prv_gh',
        providerName: 'GitHub',
        label: 'Work',
        method: 'pat',
        status: 'ready',
        linkedApps: ['GitHub'],
      };
      overview = { ...overview, accounts: [...overview.accounts, created] };
      return { body: created };
    },
  });

  renderApp('/settings/accounts');

  const accountsRegion = await screen.findByRole('region', {
    name: 'Connected accounts',
  });
  expect(within(accountsRegion).getByText('Personal')).toBeVisible();
  expect(within(accountsRegion).getByText('octocat')).toBeVisible();

  const user = userEvent.setup();

  // Check health.
  await user.click(
    within(accountsRegion).getByRole('button', { name: 'Check' }),
  );
  expect(
    await within(accountsRegion).findByText(/Health: ok — Token valid/),
  ).toBeVisible();

  // Install an app from the catalog.
  await user.click(screen.getByRole('button', { name: 'Add integration' }));
  const catalogList = await screen.findByRole('list', { name: 'Catalog apps' });
  expect(within(catalogList).getByText('Linear')).toBeVisible();
  await user.click(
    within(catalogList).getByRole('button', { name: 'Install' }),
  );
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /me/executor/catalog/import')?.body,
    ).toEqual({ entry: 'linear', name: 'Linear' }),
  );
  expect(await screen.findByText('Linear')).toBeVisible();

  // Connect a secret-based account directly in Agenthold.
  await user.click(screen.getByRole('button', { name: 'Connect account' }));
  const dialog = await screen.findByRole('dialog');
  await user.type(within(dialog).getByLabelText('Label (optional)'), 'Work');
  await user.type(within(dialog).getByLabelText(/Token/), 'ghp_test_token');
  await user.click(
    within(dialog).getByRole('button', { name: 'Save account' }),
  );
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /me/executor/connections/submit')?.body,
    ).toEqual({
      connectionId: 'con_1',
      token: 'tok_abc',
      method: 'pat',
      label: 'Work',
      fields: { token: 'ghp_test_token' },
    }),
  );
  expect(await within(accountsRegion).findByText('Work')).toBeVisible();
});

it('shows unavailable reason when Executor binary is missing', async () => {
  mockApi({
    'GET /me': me,
    'GET /me/executor': statusReady({
      available: false,
      running: false,
      dashboardUrl: undefined,
      oauthReady: false,
      reason: 'Executor binary not found',
    }),
  });
  renderApp('/settings/accounts');
  const banner = await screen.findByRole('status', {
    name: 'Integrations unavailable',
  });
  expect(banner).toHaveTextContent(
    'Connected accounts are unavailable on this server right now.',
  );
  expect(banner).toHaveTextContent('Executor binary not found');
  // Runtime internals stay hidden.
  expect(screen.queryByRole('button', { name: 'Start' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Restart' })).toBeNull();
});

it('hides Executor runtime controls and dashboard links when it is healthy', async () => {
  mockApi({
    'GET /me': me,
    'GET /me/executor': statusReady({ running: false }),
    'GET /me/executor/overview': sampleOverview(),
  });
  renderApp('/settings/accounts');
  expect(
    await screen.findByRole('region', { name: 'Connected accounts' }),
  ).toBeVisible();
  expect(screen.queryByText('Executor runtime')).toBeNull();
  expect(screen.queryByRole('button', { name: 'Start' })).toBeNull();
  expect(screen.queryByRole('link', { name: /Executor dashboard/ })).toBeNull();
  expect(
    screen.queryByRole('status', { name: 'Integrations unavailable' }),
  ).toBeNull();
  // It is the Connected accounts tab of Integrations.
  expect(
    screen.getByRole('link', { name: 'Connected accounts' }),
  ).toHaveAttribute('aria-current', 'page');
});

it('toggles Executor settings on an agent Tools tab', async () => {
  stubGlobal('confirm', () => true);
  let current: Agent = {
    id: 'a1',
    name: 'builder',
    owner: me.user,
    harness: 'claude-code',
    model: '',
    modelOptions: {},
    permissionPolicy: 'ask',
    executorEnabled: true,
    executorApprovalMode: 'model',
    credentialsInSandbox: false,
    login: { source: 'sandbox', connected: false, mine: false },
    cpus: 2,
    memoryMb: 2048,
    idleMinutes: 15,
    state: 'running',
    stateError: '',
    stateChangedAt: '2030-01-01T00:00:00Z',
    needsRebuild: false,
    isTemplate: false,
    role: 'owner',
    createdAt: '2030-01-01T00:00:00Z',
    updatedAt: '2030-01-01T00:00:00Z',
  };
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents/a1': () => ({ body: current }),
    'GET /me/executor': statusReady(),
    'GET /agents/a1/mcp-servers': [],
    'GET /agents/a1/skills': [],
    'GET /mcp-servers': [],
    'GET /skills': [],
    'PATCH /agents/a1': (r: { body: unknown }) => {
      const b = r.body as Partial<Agent>;
      current = { ...current, ...b };
      return { body: current };
    },
  });

  renderApp('/a/a1/settings?tab=tools');
  const section = await screen.findByRole('region', {
    name: 'Executor accounts',
  });
  const user = userEvent.setup();
  await user.selectOptions(
    within(section).getByRole('combobox', { name: 'Executor approval mode' }),
    'browser',
  );
  await waitFor(() =>
    expect(calls.find((c) => c.key === 'PATCH /agents/a1')?.body).toEqual({
      executorEnabled: true,
      executorApprovalMode: 'browser',
    }),
  );
});
