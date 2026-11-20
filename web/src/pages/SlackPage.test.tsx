import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { mockApi, renderApp } from '@/test-utils';

afterEach(() => vi.unstubAllGlobals());

const user = { id: 'u1', name: 'Alice', email: 'a@example.com', avatarUrl: '' };
const admin = { admin: true, user };
const member = { admin: false, user };

const identity = {
  teamId: 'T1',
  teamName: 'Acme',
  slackUserId: 'U1',
  slackUserName: 'alice',
  createdAt: '2030-01-01T00:00:00Z',
};

it('lists linked Slack users and unlinks one after confirmation', async () => {
  vi.stubGlobal('confirm', () => true);
  const api = mockApi({
    'GET /me': member,
    'GET /me/slack': [identity],
    'DELETE /me/slack/T1/U1': () => ({ status: 204 }),
  });
  renderApp('/settings/slack');
  const list = await screen.findByRole('list', { name: 'Linked Slack users' });
  expect(within(list).getByText('@alice')).toBeVisible();
  expect(within(list).getByText('in Acme')).toBeVisible();
  await userEvent.click(screen.getByRole('button', { name: 'Unlink @alice' }));
  await waitFor(() =>
    expect(api.calls.some((c) => c.key === 'DELETE /me/slack/T1/U1')).toBe(
      true,
    ),
  );
});

it('explains how to link when nothing is linked, and hides admin setup', async () => {
  mockApi({ 'GET /me': member, 'GET /me/slack': [] });
  renderApp('/settings/slack');
  expect(await screen.findByText(/No Slack user is linked/)).toHaveTextContent(
    '/agenthold link',
  );
  expect(screen.queryByText('Workspace connection')).toBeNull();
});

it('shows setup steps and the status of an unconfigured Slack app to admins', async () => {
  mockApi({
    'GET /me': admin,
    'GET /me/slack': [],
    'GET /admin/slack': { configured: false, enabled: false, connected: false },
    'GET /admin/slack/manifest': { display_information: { name: 'agenthold' } },
  });
  renderApp('/settings/slack');
  const status = await screen.findByLabelText('Slack status');
  expect(within(status).getByText('Not configured')).toBeVisible();
  const create = await screen.findByRole('link', { name: 'Create Slack app' });
  expect(create).toHaveAttribute('target', '_blank');
  const href = create.getAttribute('href')!;
  expect(href).toContain('https://api.slack.com/apps?new_app=1&manifest_json=');
  expect(
    JSON.parse(decodeURIComponent(href.split('manifest_json=')[1])),
  ).toEqual({ display_information: { name: 'agenthold' } });
  expect(screen.getByRole('button', { name: 'Copy manifest' })).toBeEnabled();
});

it('saves tokens and shows the connection error inline', async () => {
  let attempts = 0;
  const connected = {
    configured: true,
    enabled: true,
    connected: true,
    teamName: 'Acme',
    botUserId: 'B1',
  };
  const api = mockApi({
    'GET /me': admin,
    'GET /me/slack': [],
    'GET /admin/slack': () => ({
      body:
        attempts < 2
          ? { configured: false, enabled: false, connected: false }
          : connected,
    }),
    'GET /admin/slack/manifest': {},
    'PUT /admin/slack': () =>
      ++attempts === 1
        ? { status: 400, body: { error: 'invalid_auth' } }
        : { body: connected },
  });
  renderApp('/settings/slack');
  const bot = await screen.findByLabelText('Bot User OAuth Token');
  await userEvent.type(bot, 'xoxb-1');
  await userEvent.type(screen.getByLabelText('App-level token'), 'xapp-1');
  await userEvent.click(screen.getByRole('button', { name: 'Save' }));
  expect(await screen.findByText('invalid_auth')).toBeVisible();
  expect(api.calls.find((c) => c.key === 'PUT /admin/slack')?.body).toEqual({
    botToken: 'xoxb-1',
    appToken: 'xapp-1',
  });
  await userEvent.click(screen.getByRole('button', { name: 'Save' }));
  const status = await screen.findByLabelText('Slack status');
  await waitFor(() =>
    expect(within(status).getByText('Connected')).toBeVisible(),
  );
  expect(within(status).getByText('Acme')).toBeVisible();
  expect(within(status).getByText('B1')).toBeVisible();
});

it('shows the last error of a down connection and disconnects after confirmation', async () => {
  vi.stubGlobal('confirm', () => true);
  const api = mockApi({
    'GET /me': admin,
    'GET /me/slack': [],
    'GET /admin/slack': {
      configured: true,
      enabled: true,
      connected: false,
      teamName: 'Acme',
      botUserId: 'B1',
      lastError: 'token_revoked',
    },
    'GET /admin/slack/manifest': {},
    'DELETE /admin/slack': () => ({ status: 204 }),
  });
  renderApp('/settings/slack');
  const status = await screen.findByLabelText('Slack status');
  expect(within(status).getByText('Not connected')).toBeVisible();
  expect(within(status).getByRole('alert')).toHaveTextContent('token_revoked');
  await userEvent.click(
    within(status).getByRole('button', { name: 'Disconnect' }),
  );
  await waitFor(() =>
    expect(api.calls.some((c) => c.key === 'DELETE /admin/slack')).toBe(true),
  );
});
