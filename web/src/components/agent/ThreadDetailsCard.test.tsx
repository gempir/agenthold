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
import type {
  Agent,
  ProjectScript,
  Session,
  SessionPullRequest,
} from '@/api/queries';
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

function renderCard(
  a: Agent,
  routes: Record<string, unknown> = {},
  sess: Session = session,
) {
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
        session={sess}
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

const linkedPR = (
  over: Partial<SessionPullRequest> = {},
): SessionPullRequest => ({
  id: 'spr1',
  sessionId: 's1',
  host: 'github.com',
  repo: 'o/web',
  number: 12,
  url: 'https://github.com/o/web/pull/12',
  title: 'Fix the login',
  state: 'open',
  headSha: 'abc',
  checksState: 'failure',
  watching: false,
  wakeCount: 0,
  failCount: 0,
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
  ...over,
});

it('shows linked pull requests and toggles watching', async () => {
  const { calls } = renderCard(agent(), {
    'GET /sessions/s1/pull-requests': {
      pullRequests: [
        linkedPR(),
        linkedPR({
          id: 'spr2',
          number: 9,
          title: 'Old one',
          state: 'merged',
          checksState: 'none',
        }),
      ],
    },
    'PATCH /sessions/s1/pull-requests/spr1': () => ({
      body: linkedPR({ watching: true }),
    }),
  });
  await userEvent.click(
    await screen.findByRole('button', { name: 'Thread details' }),
  );
  const list = await screen.findByRole('list', {
    name: 'Linked pull requests',
  });
  const open = within(list).getByText('Fix the login').closest('li')!;
  expect(open).toHaveTextContent('Open');
  expect(
    within(open).getByRole('img', { name: 'Checks failed' }),
  ).toBeVisible();
  expect(
    within(open).getByRole('link', { name: /#12\s*Fix the login/ }),
  ).toHaveAttribute('href', '/pulls?pr=github.com%2Fo%2Fweb%2312');
  const merged = within(list).getByText('Old one').closest('li')!;
  expect(merged).toHaveTextContent('Merged');
  // Merged pull requests have nothing left to watch.
  expect(within(merged).queryByRole('button', { name: /Watch/ })).toBeNull();
  // An open one is linked: no Create PR.
  expect(screen.queryByRole('button', { name: 'Create PR' })).toBeNull();

  const toggle = within(open).getByRole('button', { name: 'Watch #12' });
  expect(toggle).toHaveAttribute('aria-pressed', 'false');
  await userEvent.click(toggle);
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'PATCH /sessions/s1/pull-requests/spr1')
        ?.body,
    ).toEqual({ watching: true }),
  );
});

it('offers Create PR when the branch has commits ahead of its base', async () => {
  let created = false;
  const { calls } = renderCard(agent(), {
    'GET /sessions/s1/pull-requests': () => ({
      body: {
        pullRequests: created ? [linkedPR({ checksState: 'pending' })] : [],
      },
    }),
    'GET /agents/a1/git/diff?path=web&mode=branch': {
      mode: 'branch',
      repo: {
        path: 'web',
        project: 'web',
        branch: 'feat/login',
        detached: false,
        dirty: false,
        ahead: 1,
        behind: 0,
        isWorktree: false,
      },
      files: [
        {
          path: 'login.ts',
          status: 'M',
          additions: 3,
          deletions: 0,
          binary: false,
          truncated: false,
        },
      ],
      additions: 3,
      deletions: 0,
      truncated: false,
    },
    'POST /sessions/s1/pull-requests': () => {
      created = true;
      return { status: 201, body: linkedPR({ checksState: 'pending' }) };
    },
  });
  await userEvent.click(
    await screen.findByRole('button', { name: 'Thread details' }),
  );
  await userEvent.click(
    await screen.findByRole('button', { name: 'Create PR' }),
  );
  const title = screen.getByRole('textbox', { name: 'Pull request title' });
  expect(title).toHaveValue('Fix login');
  await userEvent.clear(title);
  await userEvent.type(title, 'Login page fixes');
  await userEvent.click(screen.getByRole('button', { name: 'Push and open' }));
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /sessions/s1/pull-requests')?.body,
    ).toEqual({ create: { title: 'Login page fixes' } }),
  );
  const list = await screen.findByRole('list', {
    name: 'Linked pull requests',
  });
  expect(
    await within(list).findByRole('img', { name: 'Checks running' }),
  ).toBeVisible();
});

it('does not let a sub-thread watch', async () => {
  renderCard(
    agent(),
    {
      'GET /sessions/s1/pull-requests': { pullRequests: [linkedPR()] },
    },
    { ...session, parentKind: 'subthread' },
  );
  await userEvent.click(
    await screen.findByRole('button', { name: 'Thread details' }),
  );
  const toggle = await screen.findByRole('button', { name: 'Watch #12' });
  expect(toggle).toBeDisabled();
  expect(toggle).toHaveAttribute(
    'title',
    expect.stringContaining('parent thread'),
  );
});
