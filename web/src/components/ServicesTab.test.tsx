import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, it } from 'vitest';
import { mockApi } from '@/test-utils';
import { ServicesTab } from './ServicesTab';

const web = {
  name: 'web',
  command: 'npm run dev',
  workdir: 'app',
  env: {},
  enabled: true,
  status: { state: 'backoff', restarts: 3, exitCode: 1 },
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
};

function renderTab(isOwner: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ServicesTab agentId="ag1" isOwner={isOwner} />
    </QueryClientProvider>,
  );
}

it('shows service status and restarts, stops and adds services', async () => {
  const { calls } = mockApi({
    'GET /agents/ag1/services': { running: true, services: [web] },
    'POST /agents/ag1/services/web/restart': web,
    'POST /agents/ag1/services/web/stop': { ...web, enabled: false },
    'PUT /agents/ag1/services/api': { ...web, name: 'api' },
  });
  renderTab(true);
  const list = await screen.findByRole('list', { name: 'Services' });
  expect(list).toHaveTextContent('app$ npm run dev');
  expect(screen.getByLabelText('web status')).toHaveTextContent(
    'backoff · 3 restarts · exit 1',
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: 'Restart web' }));
  await user.click(screen.getByRole('button', { name: 'Stop web' }));
  await waitFor(() => {
    const keys = calls.map((c) => c.key);
    expect(keys).toContain('POST /agents/ag1/services/web/restart');
    expect(keys).toContain('POST /agents/ag1/services/web/stop');
  });
  await user.type(screen.getByRole('textbox', { name: 'Name' }), 'API');
  await user.type(
    screen.getByRole('textbox', { name: 'Command' }),
    'node server.js',
  );
  await user.click(screen.getByRole('button', { name: 'Add and start' }));
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'PUT /agents/ag1/services/api')?.body,
    ).toEqual({ command: 'node server.js' }),
  );
});

it('gives shared users status without controls, and explains a stopped machine', async () => {
  mockApi({
    'GET /agents/ag1/services': {
      running: false,
      services: [{ ...web, status: undefined }],
    },
  });
  renderTab(false);
  await screen.findByText(/The machine is not running/);
  expect(screen.getByLabelText('web status')).toHaveTextContent(
    'down (machine off)',
  );
  expect(
    screen.queryByRole('button', { name: 'Stop web' }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole('button', { name: 'Add and start' }),
  ).not.toBeInTheDocument();
});
