import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it } from 'bun:test';
import { mockApi, renderApp } from '@/test-utils';

const me = {
  admin: false,
  user: { id: 'u1', name: 'Alice', email: 'a@example.com', avatarUrl: '' },
};
const preview = {
  teamId: 'T1',
  teamName: 'Acme',
  slackUserId: 'U1',
  slackUserName: 'alice',
  expiresAt: '2030-01-01T00:15:00Z',
};

it('confirms the Slack user and links it', async () => {
  const api = mockApi({
    'GET /me': me,
    'GET /me/slack/links/abc123': preview,
    'POST /me/slack/links': () => ({
      body: { ...preview, createdAt: '2030-01-01T00:00:00Z' },
    }),
  });
  renderApp('/slack/link?code=abc123');
  expect(await screen.findByText(/Connect Slack user/)).toHaveTextContent(
    'Connect Slack user @alice in workspace Acme to your agenthold account (Alice)?',
  );
  await userEvent.click(screen.getByRole('button', { name: 'Connect' }));
  expect(await screen.findByText(/go back to Slack/)).toBeVisible();
  expect(api.calls.find((c) => c.key === 'POST /me/slack/links')?.body).toEqual(
    { code: 'abc123' },
  );
});

it('says so when the code is expired, used or unknown', async () => {
  mockApi({
    'GET /me': me,
    'GET /me/slack/links/old': () => ({
      status: 404,
      body: { error: 'link not found' },
    }),
  });
  renderApp('/slack/link?code=old');
  expect(await screen.findByText('This link does not work')).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Connect' })).toBeNull();
});

it('shows a missing code as an invalid link', async () => {
  mockApi({ 'GET /me': me });
  renderApp('/slack/link');
  expect(await screen.findByText('This link does not work')).toBeVisible();
});

it('shows the API error when linking fails and lets the user cancel', async () => {
  mockApi({
    'GET /me': me,
    'GET /me/slack/links/abc': preview,
    'POST /me/slack/links': () => ({
      status: 400,
      body: { error: 'link expired' },
    }),
  });
  renderApp('/slack/link?code=abc');
  await userEvent.click(await screen.findByRole('button', { name: 'Connect' }));
  expect(await screen.findByText('link expired')).toBeVisible();
  expect(screen.getByRole('link', { name: 'Cancel' })).toBeVisible();
});

it('sends signed-out users to login and back', async () => {
  mockApi({
    'GET /me': () => ({ status: 401, body: { error: 'unauthorized' } }),
    'GET /me/slack/links/abc': () => ({
      status: 401,
      body: { error: 'unauthorized' },
    }),
    'GET /auth/providers': [],
  });
  const { router } = renderApp('/slack/link?code=abc');
  await waitFor(() => expect(router.state.location.pathname).toBe('/login'));
  expect(router.state.location.search).toMatchObject({
    redirect: '/slack/link?code=abc',
  });
});
