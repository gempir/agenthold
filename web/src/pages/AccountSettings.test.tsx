import { screen } from '@testing-library/react';
import { expect, it } from 'bun:test';
import { mockApi, renderApp } from '@/test-utils';

const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
};

it('shows the server version and commit hash next to Settings', async () => {
  mockApi({
    'GET /me': me,
    'GET /version': { version: 'v0.4.2', commit: '9c481dea1234' },
    'GET /agents': [],
    'GET /apps': [],
    'GET /me/settings': { threadAutoSettleDays: 0 },
  });
  renderApp('/settings/threads');
  const badge = await screen.findByTestId('app-version');
  expect(badge).toHaveTextContent('v0.4.2 · 9c481de');
  expect(badge).toHaveAttribute('title', 'v0.4.2 · 9c481dea1234');
});

it('avoids duplicating the commit hash when the version already contains it', async () => {
  mockApi({
    'GET /me': me,
    'GET /version': { version: 'sha-9c481de', commit: '9c481dea1234' },
    'GET /agents': [],
    'GET /apps': [],
    'GET /me/settings': { threadAutoSettleDays: 0 },
  });
  renderApp('/settings/threads');
  const badge = await screen.findByTestId('app-version');
  expect(badge).toHaveTextContent('sha-9c481de');
});
