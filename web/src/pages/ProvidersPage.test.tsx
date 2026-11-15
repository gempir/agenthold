import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import type { Agent, ProviderLoginInfo } from '@/api/queries';
import { mockApi, renderApp } from '@/test-utils';

afterEach(() => vi.unstubAllGlobals());

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
  permissionPolicy: 'ask',
  credentialsInSandbox: false,
  login,
  cpus: 2,
  memoryMb: 2048,
  idleMinutes: 15,
  state: 'sleeping',
  stateError: '',
  stateChangedAt: '2030-01-01T00:00:00Z',
  needsRebuild: false,
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
        installHint: '',
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
