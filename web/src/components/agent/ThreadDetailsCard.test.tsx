import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router';
import { expect, it, vi } from 'bun:test';
import type { Agent, ProjectScript, Session } from '@/api/queries';
import { ThreadDetailsCard } from '@/components/agent/ThreadDetailsCard';
import { mockApi } from '@/test-utils';

const me = { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' };
const agent = (over: Partial<Agent> = {}): Agent => ({
  id: 'a1',
  name: 'builder',
  owner: me,
  harness: 'claude-code',
  model: '',
  modelOptions: {},
  permissionPolicy: 'ask',
  executorEnabled: true,
  executorApprovalMode: 'model',
  credentialsInSandbox: false,
  login: { source: 'sandbox', connected: false, mine: false },
  cpus: 2,
  memoryMb: 2048,
  idleMinutes: 15,
  state: 'running',
  stateError: '',
  stateChangedAt: '2030-01-01T00:00:00Z',
  needsRebuild: false,
  isTemplate: false,
  role: 'owner',
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
  ...over,
});
const session: Session = {
  id: 's1',
  agentId: 'a1',
  title: 'Fix login',
  running: false,
  cwd: 'web',
  git: { project: 'web', branch: 'feat/login', repoPath: 'web' },
  createdBy: me,
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
};
const script = (over: Partial<ProjectScript>): ProjectScript => ({
  id: 'dev',
  name: 'Dev',
  command: 'bun dev',
  icon: 'play',
  runOnWorktreeCreate: false,
  async: false,
  stopOnSettle: true,
  runOnSettle: false,
  autoOpenPreview: false,
  longRunning: true,
  ...over,
});

function renderCard(a: Agent, routes: Record<string, unknown> = {}) {
  const api = mockApi({
    'GET /agents/a1/git/diff?path=web&mode=uncommitted': {
      mode: 'uncommitted',
      repo: {
        path: 'web',
        project: 'web',
        branch: 'feat/login',
        detached: false,
        dirty: true,
        ahead: 0,
        behind: 0,
        isWorktree: false,
      },
      files: [
        {
          path: 'a.ts',
          status: 'M',
          additions: 5,
          deletions: 2,
          binary: false,
          truncated: false,
        },
      ],
      additions: 5,
      deletions: 2,
      truncated: false,
    },
    'GET /agents/a1/scripts?path=web': {
      path: 'web',
      source: 'agenthold.json',
      scripts: [
        script({
          previewUrl: 'https://dev.example.com',
          status: { state: 'running', restarts: 0 },
        }),
        script({
          id: 'test',
          name: 'Test',
          command: 'bun test',
          icon: 'test',
          longRunning: false,
          stopOnSettle: false,
        }),
      ],
    },
    'GET /agents/a1/exposures': {
      enabled: true,
      hostSuffix: 'example.com',
      exposures: [
        {
          id: 'x1',
          agentId: 'a1',
          port: 5173,
          name: 'web',
          access: 'private',
          wakeOnRequest: false,
          url: 'https://web.example.com',
          createdBy: 'u1',
          createdAt: '2030-01-01T00:00:00Z',
        },
      ],
    },
    'GET /agents/a1/services': { running: true, services: [] },
    ...routes,
  });
  const onOpenDiff = vi.fn();
  // useAgentActions needs a router (it navigates after a delete).
  const root = createRootRoute({
    component: () => (
      <ThreadDetailsCard
        agent={a}
        session={session}
        canUse
        onOpenDiff={onOpenDiff}
      />
    ),
  });
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  });
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { ...api, onOpenDiff };
}

it('shows the environment, branch, changes, scripts and servers', async () => {
  const { onOpenDiff } = renderCard(agent());
  await userEvent.click(
    await screen.findByRole('button', { name: 'Thread details' }),
  );
  const card = screen.getByRole('dialog', { name: 'Thread details' });
  expect(card).toHaveTextContent('builder');
  expect(card).toHaveTextContent('feat/login');
  expect(within(card).getByRole('button', { name: 'Sleep' })).toBeVisible();
  const stats = await within(card).findByRole('button', { name: 'Open diff' });
  expect(stats).toHaveTextContent('+5 −2');
  const dev = (await within(card).findByText('Dev')).closest('li')!;
  expect(within(dev).getByText('running')).toBeVisible();
  expect(
    within(card).getByRole('link', { name: 'Open Dev preview' }),
  ).toHaveAttribute('href', 'https://dev.example.com');
  expect(
    await within(card).findByRole('link', { name: /web\s*:5173/ }),
  ).toHaveAttribute('href', 'https://web.example.com');
  await userEvent.click(stats);
  expect(onOpenDiff).toHaveBeenCalled();
  expect(screen.queryByRole('dialog', { name: 'Thread details' })).toBeNull();
});

it('runs a project script and shows what it did', async () => {
  const { calls } = renderCard(agent(), {
    'POST /agents/a1/scripts/run': {
      script: script({
        id: 'test',
        name: 'Test',
        command: 'bun test',
        longRunning: false,
      }),
      mode: 'exec',
      exitCode: 0,
      output: '12 pass',
    },
  });
  await userEvent.click(
    await screen.findByRole('button', { name: 'Thread details' }),
  );
  await userEvent.click(
    await screen.findByRole('button', { name: 'Run Test' }),
  );
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /agents/a1/scripts/run')?.body,
    ).toEqual({
      path: 'web',
      name: 'test',
    }),
  );
  const result = await screen.findByRole('status', { name: 'Script result' });
  expect(result).toHaveTextContent('exited 0');
  expect(result).toHaveTextContent('12 pass');
});

it('offers to wake a sleeping agent and closes on Escape', async () => {
  const { calls } = renderCard(agent({ state: 'sleeping' }), {
    'POST /agents/a1/start': agent({ state: 'starting' }),
  });
  await userEvent.click(
    await screen.findByRole('button', { name: 'Thread details' }),
  );
  const card = screen.getByRole('dialog', { name: 'Thread details' });
  expect(card).toHaveTextContent('Wake the agent to see');
  await userEvent.click(within(card).getByRole('button', { name: 'Wake' }));
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'POST /agents/a1/start')).toBe(true),
  );
  // Nothing is asked of a sleeping machine.
  expect(calls.some((c) => c.key.startsWith('GET /agents/a1/scripts'))).toBe(
    false,
  );
  await userEvent.keyboard('{Escape}');
  expect(screen.queryByRole('dialog', { name: 'Thread details' })).toBeNull();
});
