import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, it } from 'bun:test';
import { mockApi } from '@/test-utils';
import { PreviewTab } from './PreviewTab';

const exposure = {
  id: 'exp1',
  agentId: 'ag1',
  port: 3000,
  name: 'web-3000',
  access: 'private',
  wakeOnRequest: false,
  url: 'https://web-3000.apps.example.com/',
  createdBy: 'u1',
  createdAt: '2026-01-01T00:00:00Z',
};

function renderTab(isOwner: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <PreviewTab agentId="ag1" isOwner={isOwner} />
    </QueryClientProvider>,
  );
}

it('lists exposures and exposes a port', async () => {
  const { calls } = mockApi({
    'GET /agents/ag1/exposures': {
      enabled: true,
      hostSuffix: '.apps.example.com',
      exposures: [exposure],
    },
    'POST /agents/ag1/exposures': { status: 201, body: exposure },
    'PATCH /agents/ag1/exposures/exp1': (req: { body: unknown }) => ({
      body: { ...exposure, ...(req.body as object) },
    }),
  });
  renderTab(true);
  const list = await screen.findByRole('list', { name: 'Exposed ports' });
  expect(list).toHaveTextContent('https://web-3000.apps.example.com/');
  expect(screen.getByRole('link', { name: 'Open web-3000' })).toHaveAttribute(
    'href',
    exposure.url,
  );

  const user = userEvent.setup();
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Access to web-3000' }),
    'public',
  );
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'PATCH /agents/ag1/exposures/exp1')?.body,
    ).toEqual({ access: 'public' }),
  );

  await user.type(screen.getByRole('textbox', { name: 'Port' }), '8080');
  await user.type(
    screen.getByRole('textbox', { name: 'Name (optional)' }),
    'Docs',
  );
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Who can open it' }),
    'authenticated',
  );
  await user.click(
    screen.getByRole('checkbox', { name: /may wake it from sleep/ }),
  );
  await user.click(screen.getByRole('button', { name: 'Expose' }));
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /agents/ag1/exposures')?.body,
    ).toEqual({
      port: 8080,
      access: 'authenticated',
      wakeOnRequest: true,
      name: 'docs',
    }),
  );
});

it('shows shared users the links without controls', async () => {
  mockApi({
    'GET /agents/ag1/exposures': {
      enabled: true,
      hostSuffix: '.apps.example.com',
      exposures: [exposure],
    },
  });
  renderTab(false);
  await screen.findByRole('list', { name: 'Exposed ports' });
  expect(
    screen.queryByRole('combobox', { name: 'Access to web-3000' }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole('button', { name: 'Expose' }),
  ).not.toBeInTheDocument();
});

it('explains when previews are off', async () => {
  mockApi({
    'GET /agents/ag1/exposures': {
      enabled: false,
      hostSuffix: '',
      exposures: [],
    },
  });
  renderTab(true);
  expect(
    await screen.findByText(/Preview URLs are not set up on this server/),
  ).toBeInTheDocument();
});
