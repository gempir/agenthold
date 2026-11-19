import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router';
import { afterEach, expect, it, vi } from 'vitest';
import type { Agent } from '@/api/queries';
import { mockApi, renderApp } from '@/test-utils';
import { NewAgentDialog } from './AgentsSection';

afterEach(() => vi.unstubAllGlobals());

const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
};
const agent = (over: Partial<Agent> = {}): Agent => ({
  id: 'a1',
  name: 'builder',
  owner: me.user,
  harness: 'claude-code',
  model: '',
  modelOptions: {},
  permissionPolicy: 'ask',
  credentialsInSandbox: false,
  login: { source: 'sandbox', connected: false, mine: false },
  cpus: 2,
  memoryMb: 2048,
  idleMinutes: 15,
  state: 'sleeping',
  stateError: '',
  stateChangedAt: '2030-01-01T00:00:00Z',
  needsRebuild: false,
  isTemplate: false,
  role: 'owner',
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
  ...over,
});
const template = (over: Partial<Agent> = {}) =>
  agent({ id: 't1', name: 'toolchain', isTemplate: true, ...over });

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

it('offers templates in the new-agent dialog and clones the chosen one', async () => {
  const { calls } = mockApi({
    'GET /sandbox/limits': limits,
    'GET /harnesses': [],
    'GET /agents?template=true': [template()],
    'POST /agents/t1/clone': { status: 500, body: { error: 'stop' } },
  });
  renderDialog();
  const from = await screen.findByLabelText('Start from');
  expect(
    within(from).getByRole('option', { name: 'Blank agent' }),
  ).toBeInTheDocument();
  // Only templates are offered, not the owner's ordinary agents.
  expect(within(from).queryByRole('option', { name: /builder/ })).toBeNull();
  await userEvent.selectOptions(from, 't1');
  // The copy brings its own resources: no fields to fill in.
  expect(screen.queryByLabelText('Memory (MB)')).toBeNull();
  expect(screen.getByText(/A copy of toolchain/)).toBeVisible();
  await userEvent.type(screen.getByLabelText('Name'), 'fresh');
  await userEvent.click(screen.getByRole('button', { name: 'Create' }));
  await vi.waitFor(() =>
    expect(calls.some((c) => c.key === 'POST /agents/t1/clone')).toBe(true),
  );
  expect(calls.find((c) => c.key === 'POST /agents/t1/clone')?.body).toEqual({
    name: 'fresh',
  });
  expect(calls.some((c) => c.key === 'POST /agents')).toBe(false);
});

it('has no template choice without templates, or for templates shared by others', async () => {
  mockApi({
    'GET /sandbox/limits': limits,
    'GET /harnesses': [],
    'GET /agents?template=true': [
      template({
        role: 'shared',
        owner: { ...me.user, id: 'u2', name: 'Bob' },
      }),
    ],
  });
  renderDialog();
  await screen.findByLabelText('Memory (MB)');
  await screen.findByDisplayValue('4096');
  expect(screen.queryByLabelText('Start from')).toBeNull();
});

it('clones an agent from its settings', async () => {
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents': [agent()],
    'GET /agents/a1': agent({ state: 'running' }),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
    'POST /agents/a1/clone': agent({
      id: 'a2',
      name: 'builder copy',
      state: 'creating',
    }),
    'GET /agents/a2': agent({
      id: 'a2',
      name: 'builder copy',
      state: 'creating',
    }),
    'GET /agents/a2/sessions': [],
  });
  renderApp('/a/a1/settings');
  expect(await screen.findByText(/stopped while it is copied/)).toBeVisible();
  await userEvent.click(await screen.findByRole('button', { name: 'Clone' }));
  const dialog = await screen.findByRole('dialog');
  const name = within(dialog).getByLabelText('Name');
  expect(name).toHaveValue('builder copy');
  await userEvent.clear(name);
  await userEvent.type(name, 'second');
  await userEvent.click(within(dialog).getByLabelText(/as a template/));
  await userEvent.click(within(dialog).getByRole('button', { name: 'Clone' }));
  await vi.waitFor(() =>
    expect(calls.some((c) => c.key === 'POST /agents/a1/clone')).toBe(true),
  );
  expect(calls.find((c) => c.key === 'POST /agents/a1/clone')?.body).toEqual({
    name: 'second',
    asTemplate: true,
  });
});

it('warns that a clone carries a login held inside the sandbox', async () => {
  mockApi({
    'GET /me': me,
    'GET /agents/a1': agent({ credentialsInSandbox: true }),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
  });
  renderApp('/a/a1/settings');
  expect(await screen.findByText(/the copy carries it/)).toBeVisible();
});

it('saves a machine as a template and converts it back', async () => {
  let current = agent();
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents': [],
    'GET /agents/a1': () => ({ body: current }),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
    'PATCH /agents/a1': ({ body }: { body: unknown }) => {
      current = agent({
        isTemplate: (body as { isTemplate: boolean }).isTemplate,
      });
      return { body: current };
    },
  });
  renderApp('/a/a1/settings');
  await userEvent.click(
    await screen.findByRole('button', { name: 'Save as template' }),
  );
  await vi.waitFor(() =>
    expect(calls.find((c) => c.key === 'PATCH /agents/a1')?.body).toEqual({
      isTemplate: true,
    }),
  );
  // The page now shows a template: no lifecycle buttons, a way back, and cloning is "new from template".
  await userEvent.click(
    await screen.findByRole('button', { name: 'Convert to agent' }),
  );
  await vi.waitFor(() =>
    expect(
      calls.filter((c) => c.key === 'PATCH /agents/a1').map((c) => c.body),
    ).toEqual([{ isTemplate: true }, { isTemplate: false }]),
  );
});

it('shows a template without lifecycle controls', async () => {
  mockApi({
    'GET /me': me,
    'GET /agents/t1': template(),
    'GET /agents/t1/sessions': [],
    'GET /harnesses': [],
  });
  renderApp('/a/t1/settings');
  expect(await screen.findByText(/This machine is a template/)).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Start' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Reset system' })).toBeNull();
  expect(
    screen.getByRole('button', { name: 'New agent from template' }),
  ).toBeVisible();
  expect(
    screen.getByRole('button', { name: 'Convert to agent' }),
  ).toBeVisible();
});

it('lists templates apart from agents', async () => {
  mockApi({
    'GET /me': me,
    'GET /agents': [agent({ name: 'plain' })],
    'GET /agents?template=true': [template()],
    'GET /agents/a1/sessions': [],
    'GET /agents/t1/sessions': [],
  });
  renderApp('/settings/agents');
  const main = await screen.findByRole('main');
  const templates = await within(main).findByRole('list', {
    name: 'Templates',
  });
  expect(within(templates).getByText('toolchain')).toBeVisible();
  expect(within(templates).queryByText('plain')).toBeNull();
  // The agent list holds only the ordinary agent.
  const agents = within(main).getByRole('list', { name: 'Agents' });
  expect(within(agents).getByText('plain')).toBeVisible();
  expect(within(agents).queryByText('toolchain')).toBeNull();
  // The sidebar does not list the template among agents either.
  const sidebar = screen.getByRole('navigation', { name: 'Sidebar' });
  const sideAgents = await within(sidebar).findByRole('list', {
    name: 'Agents',
  });
  expect(within(sideAgents).getByText('plain')).toBeVisible();
  expect(within(sideAgents).queryByText('toolchain')).toBeNull();
});

it('asks for a system reset before a clone when changes are pending', async () => {
  mockApi({
    'GET /me': me,
    'GET /agents/a1': agent({ needsRebuild: true }),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
  });
  renderApp('/a/a1/settings');
  expect(await screen.findByText(/Reset the system first/)).toBeVisible();
  expect(screen.getByRole('button', { name: 'Clone' })).toBeDisabled();
});
