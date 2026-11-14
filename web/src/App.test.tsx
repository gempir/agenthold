import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, expect, it, vi } from 'vitest';
import App from './App';

afterEach(() => vi.unstubAllGlobals());

function renderApp() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <App />
    </QueryClientProvider>,
  );
}

it('shows the server version', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ version: '1.2.3', commit: 'abc' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    ),
  );
  renderApp();
  expect(await screen.findByText('version 1.2.3')).toBeInTheDocument();
});

it('shows unreachable on error', async () => {
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('down')));
  renderApp();
  expect(await screen.findByText('server unreachable')).toBeInTheDocument();
});
