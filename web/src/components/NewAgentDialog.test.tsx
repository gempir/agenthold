import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router';
import { expect, it } from 'vitest';
import { mockApi } from '@/test-utils';
import { NewAgentDialog } from './AgentsSection';

const limits = {
  driver: 'firecracker',
  defaultCpus: 2,
  defaultMemoryMb: 4096,
  maxCpus: 8,
  maxMemoryMb: 16384,
  minMemoryMb: 1024,
};

function renderDialog() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <NewAgentDialog onClose={() => {}} />,
    }),
    history: createMemoryHistory(),
  });
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

it('submits the server defaults once limits loaded, never the input minimums', async () => {
  const { calls } = mockApi({
    'GET /sandbox/limits': limits,
    'GET /harnesses': [],
    'POST /agents': { status: 500, body: { error: 'stop' } },
  });
  renderDialog();
  const create = await screen.findByRole('button', { name: 'Create' });
  const mem = await screen.findByLabelText('Memory (MB)');
  await screen.findByDisplayValue('4096');
  expect(mem).toHaveAttribute('min', '1024');
  await userEvent.type(screen.getByLabelText('Name'), 'a');
  await userEvent.click(create);
  const post = calls.find((c) => c.key === 'POST /agents');
  expect(post?.body).toMatchObject({ cpus: 2, memoryMb: 4096 });
});

it('blocks submit while limits are loading and for values under the floor', async () => {
  mockApi({
    'GET /sandbox/limits': limits,
    'GET /harnesses': [],
  });
  renderDialog();
  const name = await screen.findByLabelText('Name');
  await userEvent.type(name, 'a');
  const create = screen.getByRole('button', { name: 'Create' });
  await screen.findByDisplayValue('4096');
  expect(create).toBeEnabled();
  const mem = screen.getByLabelText('Memory (MB)');
  await userEvent.clear(mem);
  expect(create).toBeDisabled();
  await userEvent.type(mem, '128');
  expect(create).toBeDisabled();
  await userEvent.clear(mem);
  await userEvent.type(mem, '2048');
  expect(create).toBeEnabled();
});
