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

const GIB = 1024 ** 3;
const admin = {
  admin: true,
  user: { id: 'u1', name: 'Alice', email: 'a@example.com', avatarUrl: '' },
};
const node = (over: Record<string, unknown>) => ({
  id: 'local',
  name: 'local',
  online: true,
  local: true,
  agents: 1,
  cpus: 8,
  availableMemoryBytes: 32 * GIB,
  freeDiskBytes: 200 * GIB,
  ...over,
});
const twoNodes = [
  node({}),
  node({
    id: 'node_edge',
    name: 'edge',
    local: false,
    availableMemoryBytes: 16 * GIB,
  }),
];
const nodeRoutes = {
  'GET /sandbox/limits': limits,
  'GET /harnesses': [],
  'GET /me': admin,
};

it('creates on automatic placement unless an admin picks a node', async () => {
  const { calls } = mockApi({
    ...nodeRoutes,
    'GET /nodes': twoNodes,
    'POST /agents': { status: 500, body: { error: 'stop' } },
  });
  renderDialog();
  const select = await screen.findByLabelText('Node');
  expect(select).toHaveValue('');
  expect(screen.getByRole('option', { name: /Automatic/ })).toBeInTheDocument();
  expect(
    screen.getByRole('option', {
      name: 'edge · online · 16.0 GiB memory free · 200.0 GiB disk free · 1 agent',
    }),
  ).toBeInTheDocument();
  await userEvent.type(screen.getByLabelText('Name'), 'a');
  await userEvent.click(screen.getByRole('button', { name: 'Create' }));
  expect(calls.find((c) => c.key === 'POST /agents')?.body).not.toHaveProperty(
    'nodeId',
  );

  await userEvent.selectOptions(select, 'node_edge');
  await userEvent.click(screen.getByRole('button', { name: 'Create' }));
  const posts = calls.filter((c) => c.key === 'POST /agents');
  expect(posts[posts.length - 1].body).toMatchObject({ nodeId: 'node_edge' });
});

it('flags nodes that are offline, unreported or short of memory', async () => {
  mockApi({
    ...nodeRoutes,
    'GET /nodes': [
      node({}),
      node({ id: 'n_off', name: 'off', online: false, local: false }),
      node({
        id: 'n_full',
        name: 'full',
        local: false,
        availableMemoryBytes: 2 * GIB,
      }),
      node({
        id: 'n_new',
        name: 'new',
        local: false,
        availableMemoryBytes: undefined,
      }),
    ],
  });
  renderDialog();
  await screen.findByLabelText('Node');
  const option = (re: RegExp) => screen.getByRole('option', { name: re });
  expect(option(/^local/)).toBeEnabled();
  expect(option(/^off · offline/)).toBeDisabled();
  expect(option(/^full · not enough memory/)).toBeDisabled();
  expect(option(/^new · capacity unknown/)).toBeDisabled();
});

it('stops a picked node that no longer fits the memory asked for', async () => {
  mockApi({ ...nodeRoutes, 'GET /nodes': twoNodes });
  renderDialog();
  const select = await screen.findByLabelText('Node');
  await userEvent.type(screen.getByLabelText('Name'), 'a');
  await userEvent.selectOptions(select, 'node_edge');
  const create = screen.getByRole('button', { name: 'Create' });
  expect(create).toBeEnabled();
  const mem = screen.getByLabelText('Memory (MB)');
  await userEvent.clear(mem);
  await userEvent.type(mem, '16384');
  // 16384 MB is 16.0 GiB: exactly what edge has, so it still fits...
  expect(create).toBeEnabled();
  await userEvent.clear(mem);
  await userEvent.type(mem, '16385');
  expect(create).toBeDisabled();
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'edge has 16.0 GiB of memory available; this agent needs 16385 MB',
  );
});

it('offers no node choice to non-admins or when there is one node', async () => {
  const { calls } = mockApi({
    'GET /sandbox/limits': limits,
    'GET /harnesses': [],
    'GET /me': { ...admin, admin: false },
    'GET /nodes': twoNodes,
  });
  renderDialog();
  await screen.findByDisplayValue('4096');
  expect(screen.queryByLabelText('Node')).not.toBeInTheDocument();
  expect(calls.some((c) => c.key === 'GET /nodes')).toBe(false);
});

it('hides the node choice when there is only one node', async () => {
  mockApi({ ...nodeRoutes, 'GET /nodes': [node({})] });
  renderDialog();
  await screen.findByDisplayValue('4096');
  await screen.findByRole('button', { name: 'Create' });
  expect(screen.queryByLabelText('Node')).not.toBeInTheDocument();
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
