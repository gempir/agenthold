import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import type { Agent } from '@/api/queries';
import { mockApi, renderApp } from '@/test-utils';

afterEach(() => vi.unstubAllGlobals());

const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
  personalWorkspaceId: 'w1',
};
const ws = {
  id: 'w1',
  name: 'Acme',
  personal: false,
  role: 'admin',
  createdAt: '2030-01-01T00:00:00Z',
};
const rule = {
  hosts: ['api.github.com'],
  header: 'Authorization',
  template: 'Bearer {{.Value}}',
};
const secret = {
  id: 's1',
  workspaceId: 'w1',
  name: 'GitHub',
  rule,
  placeholder: 'ah_placeholder_123',
  owner: me.user,
  canManage: true,
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
};
const preset = {
  id: 'github',
  name: 'GitHub',
  rule,
  envVar: 'GITHUB_TOKEN',
  description: '',
};
const agent: Agent = {
  id: 'a1',
  workspaceId: 'w1',
  name: 'builder',
  owner: me.user,
  image: 'ubuntu:24.04',
  harness: 'claude-code',
  model: '',
  permissionPolicy: 'ask',
  credentialsInSandbox: false,
  login: { source: 'sandbox', connected: false, mine: false },
  cpus: 2,
  memoryMb: 2048,
  idleMinutes: 15,
  state: 'running',
  stateError: '',
  stateChangedAt: '2030-01-01T00:00:00Z',
  needsRebuild: false,
  role: 'admin',
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
};

it('lists secrets and creates one from a preset', async () => {
  const { calls } = mockApi({
    'GET /me': me,
    'GET /workspaces': [ws],
    'GET /workspaces/w1': ws,
    'GET /workspaces/w1/members': [],
    'GET /workspaces/w1/agents': [],
    'GET /workspaces/w1/secrets': [secret],
    'GET /secrets/presets': [preset],
    'POST /workspaces/w1/secrets': { status: 201, body: secret },
  });
  renderApp('/w/w1');
  await userEvent.click(await screen.findByRole('tab', { name: 'Secrets' }));
  expect(await screen.findByText('ah_placeholder_123')).toBeVisible();
  expect(screen.getByText(/api\.github\.com/)).toBeVisible();

  await userEvent.click(screen.getByRole('button', { name: 'New secret' }));
  await screen.findByRole('option', { name: 'GitHub' });
  await userEvent.selectOptions(screen.getByLabelText('Preset'), 'github');
  expect(screen.getByLabelText('Name')).toHaveValue('GitHub');
  await userEvent.type(screen.getByLabelText('Value'), 'tok123');
  await userEvent.click(screen.getByRole('button', { name: 'Create secret' }));
  await vi.waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /workspaces/w1/secrets')?.body,
    ).toEqual({ name: 'GitHub', value: 'tok123', preset: 'github' }),
  );
});

function networkMocks(extra: Record<string, unknown> = {}) {
  return mockApi({
    'GET /me': me,
    'GET /workspaces': [ws],
    'GET /agents/a1': agent,
    'GET /agents/a1/network-policy': {
      mode: 'allowlist',
      allow: ['github.com'],
    },
    'PUT /agents/a1/network-policy': ({ body }: { body: unknown }) => ({
      body,
    }),
    'GET /agents/a1/secrets': [{ secret, envVar: 'GITHUB_TOKEN' }],
    'GET /workspaces/w1/secrets': [secret],
    'GET /secrets/presets': [preset],
    'GET /agents/a1/egress-log': [
      {
        id: 7,
        at: '2030-01-01T00:00:00Z',
        kind: 'https',
        host: 'evil.example',
        ip: '1.2.3.4',
        port: 443,
        method: 'GET',
        path: '/x',
        status: 0,
        allowed: false,
        reason: 'host not in allowlist',
        injected: false,
        bytesUp: 0,
        bytesDown: 2048,
        durationMs: 1,
      },
    ],
    ...extra,
  });
}

it('network tab saves allowlist lines as an array', async () => {
  const { calls } = networkMocks();
  renderApp('/a/a1');
  await userEvent.click(await screen.findByRole('tab', { name: 'network' }));
  const box = await screen.findByLabelText(/Allowed destinations/);
  expect(box).toHaveValue('github.com');
  await userEvent.type(box, '{Enter}*.npmjs.org{Enter}');
  await userEvent.click(screen.getByRole('button', { name: 'Save policy' }));
  await vi.waitFor(() =>
    expect(
      calls.find((c) => c.key === 'PUT /agents/a1/network-policy')?.body,
    ).toEqual({ mode: 'allowlist', allow: ['github.com', '*.npmjs.org'] }),
  );
  expect(screen.getByText('GITHUB_TOKEN')).toBeVisible();
});

it('request log renders denied entries with reason', async () => {
  networkMocks();
  renderApp('/a/a1');
  await userEvent.click(await screen.findByRole('tab', { name: 'network' }));
  expect(await screen.findByText('evil.example:443')).toBeVisible();
  expect(screen.getByText('denied')).toHaveAttribute(
    'title',
    'host not in allowlist',
  );
  expect(
    screen.getByText('host not in allowlist', { selector: 'div' }),
  ).toBeVisible();
});
