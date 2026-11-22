import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, expect, it, vi } from 'vitest';
import { mockApi } from '@/test-utils';
import { ThreadsSettingsPage } from './ThreadsSettingsPage';

afterEach(() => vi.unstubAllGlobals());

const renderPage = () =>
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <ThreadsSettingsPage />
    </QueryClientProvider>,
  );

it('shows and changes when idle threads settle', async () => {
  const { calls } = mockApi({
    'GET /me/settings': { threadAutoSettleDays: 3 },
    'PUT /me/settings': ({ body }: { body: unknown }) => ({ body }),
  });
  renderPage();
  const select = await screen.findByRole('combobox', {
    name: 'Settle idle threads',
  });
  expect(select).toHaveValue('3');
  expect(
    screen.getByRole('option', { name: 'After 3 days (default)' }),
  ).toBeInTheDocument();
  await userEvent.selectOptions(select, '0');
  await waitFor(() =>
    expect(calls.find((c) => c.key === 'PUT /me/settings')?.body).toEqual({
      threadAutoSettleDays: 0,
    }),
  );
  expect(await screen.findByRole('status')).toHaveTextContent('Saved.');
  expect(select).toHaveValue('0');
  expect(screen.getByRole('option', { name: 'Never' })).toBeInTheDocument();
});

it('keeps a value set outside the presets', async () => {
  mockApi({ 'GET /me/settings': { threadAutoSettleDays: 5 } });
  renderPage();
  expect(
    await screen.findByRole('combobox', { name: 'Settle idle threads' }),
  ).toHaveValue('5');
});

it('is listed in the account settings', async () => {
  const { renderApp } = await import('@/test-utils');
  mockApi({
    'GET /me': {
      user: { id: 'u1', email: '', name: 'A', avatarUrl: '' },
    },
    'GET /me/settings': { threadAutoSettleDays: 3 },
    'GET /agents': [],
    'GET /apps': [],
  });
  renderApp('/settings/threads');
  const nav = await screen.findByRole('navigation', {
    name: 'Account settings',
  });
  expect(nav).toHaveTextContent('Threads');
  expect(await screen.findByRole('heading', { name: 'Threads' })).toBeVisible();
});
