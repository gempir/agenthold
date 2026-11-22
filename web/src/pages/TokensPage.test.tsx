import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, it } from 'bun:test';
import { mockApi } from '@/test-utils';
import { TokensPage } from './TokensPage';

it('creates a token and shows it once', async () => {
  const { calls } = mockApi({
    'GET /me/tokens': [],
    'POST /me/tokens': () => ({
      status: 201,
      body: {
        id: 't1',
        name: 'CI',
        prefix: 'ahp_abc123',
        createdAt: '2026-01-01T00:00:00Z',
        token: 'ahp_abc123secret',
      },
    }),
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <TokensPage />
    </QueryClientProvider>,
  );
  const user = userEvent.setup();
  await user.type(screen.getByRole('textbox', { name: 'Name' }), 'CI');
  await user.click(screen.getByRole('button', { name: 'Create token' }));
  expect(await screen.findByRole('status')).toHaveTextContent(
    'ahp_abc123secret',
  );
  await waitFor(() =>
    expect(calls.find((c) => c.key === 'POST /me/tokens')?.body).toEqual({
      name: 'CI',
      expiresInDays: 90,
    }),
  );
});
