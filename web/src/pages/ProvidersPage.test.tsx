import { stubGlobal } from '@/test-setup';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it } from 'bun:test';
import type { Agent, ProviderLoginInfo } from '@/api/queries';
import { mockApi, renderApp } from '@/test-utils';

const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
};
const claude = (connected: boolean): ProviderLoginInfo => ({
  provider: 'claude-code',
  name: 'Claude Code',
  kinds: [
    {
      id: 'oauth-token',
      name: 'Claude subscription',
      description: 'A long-lived token.',
      command: 'claude setup-token',
      inputLabel: 'Token',
      multiline: false,
    },
    {
      id: 'api-key',
      name: 'Anthropic API key',
      description: 'Per use.',
      inputLabel: 'API key',
      multiline: false,
    },
  ],
  agents: connected ? [{ id: 'a1', name: 'builder' }] : [],
  ...(connected && {
    login: {
      kind: 'oauth-token',
      label: 'Token …abcd',
      status: 'ok',
      statusMessage: '',
      connectedAt: '2030-01-01T00:00:00Z',
    },
  }),
});

it('connects a provider login without echoing it', async () => {
  let connected = false;
  const { calls } = mockApi({
    'GET /me': me,
    'GET /me/provider-logins': () => ({ body: [claude(connected)] }),
    'GET /me/harnesses': [],
    'GET /org/providers': [],
    'PUT /me/provider-logins/claude-code': () => {
      connected = true;
      return { body: claude(true) };
    },
  });
  renderApp('/settings/providers');
  const section = await screen.findByRole('region', { name: 'Claude Code' });
  expect(within(section).getByText('not connected')).toBeInTheDocument();
  const user = userEvent.setup();
  await user.click(within(section).getByRole('button', { name: 'Connect' }));
  expect(within(section).getByText('claude setup-token')).toBeInTheDocument();
  await user.type(within(section).getByLabelText('Token'), 'sk-ant-oat01-x');
  await user.click(within(section).getByRole('button', { name: 'Save login' }));
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'PUT /me/provider-logins/claude-code')?.body,
    ).toEqual({ kind: 'oauth-token', credential: 'sk-ant-oat01-x' }),
  );
  expect(await within(section).findByText('connected')).toBeInTheDocument();
  expect(within(section).getByRole('link', { name: 'builder' })).toBeVisible();
  expect(section).not.toHaveTextContent('sk-ant-oat01-x');
});

const agent = (login: Agent['login'], role: Agent['role']): Agent => ({
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
  login,
  cpus: 2,
  memoryMb: 2048,
  idleMinutes: 15,
  state: 'sleeping',
  stateError: '',
  stateChangedAt: '2030-01-01T00:00:00Z',
  needsRebuild: false,
  isTemplate: false,
  role,
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
});

it('agent harness settings link to settings when the login is missing, and offers your own', async () => {
  const bob = { id: 'u2', email: 'b@x.io', name: 'Bob', avatarUrl: '' };
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents/a1': agent(
      { source: 'account', connected: false, mine: false, user: bob },
      'owner',
    ),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [
      {
        id: 'claude-code',
        name: 'Claude Code',
        default: true,
        secretPresets: [],
        accountLogins: true,
      },
    ],
    'PUT /agents/a1/login': () => ({
      body: agent(
        { source: 'account', connected: false, mine: true, user: me.user },
        'owner',
      ),
    }),
  });
  renderApp('/a/a1/settings?tab=harness');
  // The harness name arrives with its own request, after the agent.
  await waitFor(() =>
    expect(screen.getByTestId('agent-login')).toHaveTextContent(
      "Bob's account has no Claude Code login.",
    ),
  );
  await userEvent.click(screen.getByRole('button', { name: 'Use my logins' }));
  await waitFor(() =>
    expect(screen.getByTestId('agent-login')).toHaveTextContent(
      'Your Claude Code login is not connected.',
    ),
  );
  expect(calls.some((c) => c.key === 'PUT /agents/a1/login')).toBe(true);
  expect(
    within(screen.getByTestId('agent-login')).getByRole('link', {
      name: 'Provider logins',
    }),
  ).toHaveAttribute('href', '/settings/providers');
});

it('browses the ACP Store and adds a registry harness', async () => {
  const custom: Array<{
    id: string;
    kind: 'registry' | 'local';
    name: string;
    description: string;
    registryId?: string;
    registryVersion?: string;
    args: string[];
    env: Record<string, string>;
    customModels: string[];
    createdAt: string;
    updatedAt: string;
  }> = [];
  const { calls } = mockApi({
    'GET /me': me,
    'GET /me/provider-logins': [],
    'GET /me/harnesses': () => ({ body: custom }),
    'GET /harnesses': [],
    'GET /acp-registry': () => ({
      body: {
        version: '1.0.0',
        agents: [
          {
            id: 'goose',
            name: 'Goose',
            version: '1.2.3',
            description: 'Open source AI developer agent',
            distributions: ['binary', 'npx'],
            addedId: custom.find((c) => c.registryId === 'goose')?.id,
          },
        ],
      },
    }),
    'POST /me/harnesses': ({ body }: { body: unknown }) => {
      const b = body as { kind: 'registry' | 'local'; registryId?: string };
      const created = {
        id: 'acp_1',
        kind: b.kind,
        name: 'Goose',
        description: 'Open source AI developer agent',
        registryId: b.registryId,
        registryVersion: '1.2.3',
        args: [],
        env: {},
        customModels: [],
        createdAt: '2030-01-01T00:00:00Z',
        updatedAt: '2030-01-01T00:00:00Z',
      };
      custom.push(created);
      return { status: 201, body: created };
    },
  });
  renderApp('/settings/providers');
  const user = userEvent.setup();
  const browseBtn = await screen.findByRole('button', {
    name: /Browse ACP Store/i,
  });
  await user.click(browseBtn);
  expect(await screen.findByText('Goose')).toBeVisible();
  await user.click(screen.getByRole('button', { name: /^Add$/i }));
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'POST /me/harnesses')).toBe(true),
  );
  // Close the modal dialog so the background custom harnesses region is visible.
  await user.keyboard('{Escape}');
  const storeSection = await screen.findByRole('region', {
    name: /ACP Store and custom harnesses/i,
  });
  expect(await within(storeSection).findByText('Goose')).toBeVisible();
});

it('runs Antigravity interactive Google sign-in from harness settings', async () => {
  const agyAgent: Agent = {
    ...agent({ source: 'sandbox', connected: false, mine: false }, 'owner'),
    harness: 'antigravity',
  };
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents/a1': agyAgent,
    'GET /agents/a1/sessions': [],
    'GET /agents/a1/models': {
      harness: 'antigravity',
      source: 'fallback',
      switchable: true,
      customModels: false,
      models: [],
    },
    'GET /harnesses': [
      {
        id: 'antigravity',
        name: 'Antigravity',
        default: false,
        secretPresets: ['gemini'],
        accountLogins: true,
      },
    ],
    'POST /agents/a1/harness/auth/start': {
      flowId: 'af_1',
      status: 'waiting_for_code',
      authorizationUrl:
        'https://accounts.google.com/o/oauth2/auth?redirect_uri=https%3A%2F%2Fantigravity.google%2Foauth-callback&response_type=code&state=s1',
    },
    'POST /agents/a1/harness/auth/submit': {
      flowId: 'af_1',
      status: 'completed',
      message: 'Signed in to Antigravity.',
    },
  });
  renderApp('/a/a1/settings?tab=harness');
  const user = userEvent.setup();
  await user.click(
    await screen.findByRole('button', { name: /Sign in with Google/i }),
  );
  const link = await screen.findByRole('link', {
    name: /Open Google sign-in/i,
  });
  expect(link).toHaveAttribute(
    'href',
    expect.stringContaining('accounts.google.com/o/oauth2/auth'),
  );
  await user.type(
    screen.getByLabelText(/Paste authorization code or callback URL/i),
    'my-auth-code',
  );
  await user.click(screen.getByRole('button', { name: /Complete sign-in/i }));
  expect(await screen.findByText('Signed in to Antigravity.')).toBeVisible();
  expect(
    calls.find((c) => c.key === 'POST /agents/a1/harness/auth/submit')?.body,
  ).toEqual({ flowId: 'af_1', code: 'my-auth-code' });
});

const pool = (
  provider: string,
  configured: boolean,
  over: Record<string, unknown> = {},
) => ({
  provider,
  configured,
  enabled: configured,
  ...(configured && { kind: 'api_key', label: 'Team key' }),
  ...over,
});

it('shows which providers the organization provides, and personal logins override them', async () => {
  mockApi({
    'GET /me': me,
    'GET /me/provider-logins': [claude(false)],
    'GET /me/harnesses': [],
    'GET /org/providers': [
      pool('claude-code', true),
      pool('codex', false),
      pool('antigravity', true, { enabled: false }),
      pool('opencode', false),
    ],
  });
  renderApp('/settings/providers');
  const pools = await screen.findByRole('region', {
    name: 'Organization provider pools',
  });
  // Users only see providers that are actually provided.
  expect(
    within(pools).getByRole('listitem', {
      name: 'Claude Code organization pool',
    }),
  ).toHaveTextContent('Provided by organization');
  expect(within(pools).queryByText('Codex')).toBeNull();
  expect(within(pools).queryByText('Antigravity')).toBeNull();
  expect(within(pools).queryByRole('button')).toBeNull();

  const section = screen.getByRole('region', { name: 'Claude Code' });
  expect(section).toHaveTextContent('Provided by organization');
  expect(section).toHaveTextContent('you can chat right away');
  expect(
    within(section).getByRole('button', { name: 'Use my own' }),
  ).toBeVisible();
});

it('hides org pools from users when the organization provides none', async () => {
  mockApi({
    'GET /me': me,
    'GET /me/provider-logins': [claude(false)],
    'GET /me/harnesses': [],
    'GET /org/providers': [pool('claude-code', false), pool('codex', false)],
  });
  renderApp('/settings/providers');
  await screen.findByRole('region', { name: 'Claude Code' });
  expect(
    screen.queryByRole('region', { name: 'Organization provider pools' }),
  ).toBeNull();
});

it('lets admins configure, update and remove org provider logins', async () => {
  let pools = [pool('claude-code', false), pool('codex', true)];
  const { calls } = mockApi({
    'GET /me': { ...me, admin: true },
    'GET /me/provider-logins': [claude(false)],
    'GET /me/harnesses': [],
    'GET /org/providers': () => ({ body: pools }),
    'PUT /org/providers/claude-code': (r: { body: unknown }) => {
      pools = [pool('claude-code', true, { kind: 'gateway' }), pools[1]];
      return { body: { ...pools[0], ...(r.body as object) } };
    },
    'DELETE /org/providers/codex': () => {
      pools = [pools[0], pool('codex', false)];
      return { status: 204 };
    },
  });
  const user = userEvent.setup();
  stubGlobal('confirm', () => true);
  renderApp('/settings/providers');
  const region = await screen.findByRole('region', {
    name: 'Organization provider pools',
  });
  const claudeRow = within(region).getByRole('listitem', {
    name: 'Claude Code organization pool',
  });
  expect(claudeRow).toHaveTextContent('not provided');
  await user.click(
    within(claudeRow).getByRole('button', { name: 'Configure' }),
  );
  const form = within(claudeRow).getByRole('form', {
    name: 'Organization Claude Code login',
  });
  await user.selectOptions(within(form).getByLabelText('Credential type'), [
    'gateway',
  ]);
  await user.type(
    within(form).getByLabelText('Gateway URL'),
    'https://llm.example.com',
  );
  await user.type(within(form).getByLabelText('API key'), 'gw-secret');
  await user.type(within(form).getByLabelText('Label (optional)'), 'Gateway');
  await user.click(
    within(form).getByRole('button', { name: 'Save organization login' }),
  );
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'PUT /org/providers/claude-code')?.body,
    ).toEqual({
      kind: 'gateway',
      enabled: true,
      label: 'Gateway',
      credential: 'gw-secret',
      baseUrl: 'https://llm.example.com',
    }),
  );
  expect(
    await within(claudeRow).findByText('Provided by organization'),
  ).toBeVisible();
  expect(region).not.toHaveTextContent('gw-secret');

  const codexRow = within(region).getByRole('listitem', {
    name: 'Codex organization pool',
  });
  await user.click(within(codexRow).getByRole('button', { name: 'Remove' }));
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'DELETE /org/providers/codex')).toBe(
      true,
    ),
  );
  expect(await within(codexRow).findByText('not provided')).toBeVisible();
});
