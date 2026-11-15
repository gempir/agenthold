import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import {
  agentPollInterval,
  agentsPollInterval,
  type Agent,
  type Session,
} from '@/api/queries';
import { AgentStateBadge } from '@/components/AgentStateBadge';
import { mockApi, renderApp } from '@/test-utils';

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
  role: 'shared',
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
  ...over,
});

it('renders agent list with state badges', async () => {
  mockApi({
    'GET /me': me,
    'GET /agents': [
      agent({ id: 'a1', name: 'one', state: 'running' }),
      agent({ id: 'a2', name: 'two', state: 'error', needsRebuild: true }),
    ],
  });
  renderApp('/settings/agents');
  const main = await screen.findByRole('main');
  const one = (await within(main).findByText('one')).closest('a')!;
  expect(within(one).getByText('running')).toHaveAttribute(
    'data-state',
    'running',
  );
  const two = within(main).getByText('two').closest('a')!;
  expect(within(two).getByText('error')).toBeInTheDocument();
  expect(within(two).getByText('reset pending')).toBeInTheDocument();
});

it('shows Start for a sleeping agent and hides owner controls from a shared user', async () => {
  mockApi({
    'GET /me': me,
    'GET /agents/a1': agent(),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
  });
  renderApp('/a/a1/settings');
  expect(await screen.findByRole('button', { name: 'Start' })).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Reset system' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
});

it('lets an owner reset the system without an image setting', async () => {
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents/a1': agent({ role: 'owner', needsRebuild: true }),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
    'POST /agents/a1/rebuild': agent({ role: 'owner', state: 'rebuilding' }),
  });
  renderApp('/a/a1/settings');
  expect(await screen.findByText(/Reset the system to apply/)).toBeVisible();
  expect(screen.queryByLabelText('Image')).toBeNull();
  await userEvent.click(screen.getByRole('button', { name: 'Reset system' }));
  await vi.waitFor(() =>
    expect(calls.some((c) => c.key === 'POST /agents/a1/rebuild')).toBe(true),
  );
});

it('polls fast while transient and slowly while running', () => {
  expect(agentPollInterval(agent({ state: 'starting' }))).toBe(1000);
  expect(agentPollInterval(agent({ state: 'destroying' }))).toBe(1000);
  expect(agentPollInterval(agent({ state: 'running' }))).toBe(10_000);
  expect(agentPollInterval(agent({ state: 'sleeping' }))).toBe(false);
  expect(agentPollInterval(undefined)).toBe(false);
  expect(
    agentsPollInterval([
      agent({ state: 'running' }),
      agent({ state: 'creating' }),
    ]),
  ).toBe(1000);
  expect(agentsPollInterval([agent({ state: 'sleeping' })])).toBe(false);
});

it('badge shows Asleep and sleeps-in text', () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date('2030-01-01T00:00:00Z'));
  try {
    const { unmount } = render(<AgentStateBadge state="sleeping" />);
    expect(screen.getByText('Asleep')).toBeInTheDocument();
    unmount();
    render(<AgentStateBadge state="running" sleepsAt="2030-01-01T00:05:00Z" />);
    expect(screen.getByText('sleeps in 5 min')).toBeInTheDocument();
  } finally {
    vi.useRealTimers();
  }
});

it('sleeping banner Wake now posts start', async () => {
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents/a1': agent(),
    'GET /agents/a1/sessions': [],
    'POST /agents/a1/start': agent({ state: 'starting' }),
  });
  renderApp('/a/a1');
  expect(await screen.findByText(/This agent is asleep\./)).toBeInTheDocument();
  await userEvent.click(screen.getByRole('button', { name: 'Wake now' }));
  await vi.waitFor(() =>
    expect(calls.some((c) => c.key === 'POST /agents/a1/start')).toBe(true),
  );
});

const bob = { id: 'u2', email: 'b@x.io', name: 'Bob', avatarUrl: '' };
const carol = { id: 'u3', email: 'c@x.io', name: 'Carol', avatarUrl: '' };

it('sharing tab lists access and shares by email', async () => {
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents/a1': agent({ role: 'owner' }),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
    'GET /agents/a1/shares': {
      owner: me.user,
      shares: [{ user: bob, createdAt: '2030-01-01T00:00:00Z' }],
    },
    'POST /agents/a1/shares': {
      user: carol,
      createdAt: '2030-01-01T00:00:00Z',
    },
  });
  renderApp('/a/a1/settings?tab=sharing');
  const people = await screen.findByRole('list', {
    name: 'People with access',
  });
  expect(within(people).getByText('Alice')).toBeInTheDocument();
  expect(within(people).getByText('Bob')).toBeInTheDocument();
  expect(
    within(people).getByRole('button', { name: 'Remove Bob' }),
  ).toBeVisible();
  await userEvent.type(screen.getByLabelText('Email'), 'c@x.io');
  await userEvent.click(screen.getByRole('button', { name: 'Share' }));
  await vi.waitFor(() =>
    expect(calls.find((c) => c.key === 'POST /agents/a1/shares')?.body).toEqual(
      { email: 'c@x.io' },
    ),
  );
});

it('shared user chats but sees no activity, owner settings or share form', async () => {
  vi.stubGlobal('WebSocket', FakeSocket);
  mockApi({
    'GET /me': { user: bob },
    'GET /harnesses': [],
    'GET /agents/a1': agent({ role: 'shared', state: 'running' }),
    'GET /agents/a1/sessions': [],
    'GET /agents/a1/shares': {
      owner: me.user,
      shares: [{ user: bob, createdAt: '2030-01-01T00:00:00Z' }],
    },
  });
  renderApp('/a/a1');
  expect(
    await screen.findByRole('textbox', { name: 'Message' }),
  ).toBeInTheDocument();

  await userEvent.click(screen.getByRole('link', { name: 'Agent settings' }));
  const nav = await screen.findByRole('navigation', { name: 'Agent settings' });
  expect(within(nav).getByRole('link', { name: 'Terminal' })).toBeVisible();
  expect(within(nav).queryByRole('link', { name: 'Activity' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
  await userEvent.click(within(nav).getByRole('link', { name: 'Sharing' }));
  await screen.findByText('People with access', { selector: 'h2' });
  expect(screen.queryByRole('button', { name: 'Share' })).toBeNull();
  expect(screen.getByRole('button', { name: 'Leave' })).toBeVisible();
});

const session = (over: Partial<Session> = {}): Session => ({
  id: 's1',
  agentId: 'a1',
  title: 'Fix the build',
  running: false,
  createdBy: me.user,
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
  ...over,
});

class FakeSocket {
  onopen?: () => void;
  onmessage?: (e: { data: string }) => void;
  onclose?: () => void;
  constructor(public url: string) {}
  close() {}
}

it('lands in the most recent chat across agents', async () => {
  vi.stubGlobal('WebSocket', FakeSocket);
  mockApi({
    'GET /me': me,
    'GET /harnesses': [],
    'GET /agents': [
      agent({ id: 'a1', name: 'one', state: 'running' }),
      agent({ id: 'a2', name: 'two' }),
    ],
    'GET /agents/a1/sessions': [
      session({ id: 's1', title: 'Older', updatedAt: '2030-01-01T00:00:00Z' }),
    ],
    'GET /agents/a2/sessions': [
      session({
        id: 's2',
        agentId: 'a2',
        title: 'Newest',
        running: true,
        updatedAt: '2030-01-02T00:00:00Z',
      }),
    ],
    'GET /agents/a2': agent({ id: 'a2', name: 'two', state: 'running' }),
  });
  const { router } = renderApp('/');
  await vi.waitFor(() => expect(router.state.location.href).toBe('/a/a2?s=s2'));
  const chats = await screen.findByRole('list', { name: 'Chats' });
  const rows = within(chats).getAllByRole('link');
  expect(rows.map((r) => r.textContent)).toEqual([
    expect.stringContaining('Newest'),
    expect.stringContaining('Older'),
  ]);
  expect(rows[0]).toHaveAttribute('aria-current', 'page');
  expect(within(rows[0]).getByRole('img', { name: 'running' })).toBeVisible();
  // The running chat is also surfaced in the Working group.
  const working = screen.getByRole('list', { name: 'Working' });
  expect(within(working).getByText('Newest')).toBeVisible();
  // The sidebar search filters chats.
  await userEvent.type(
    screen.getByRole('searchbox', { name: 'Search chats and agents' }),
    'old',
  );
  expect(within(chats).getAllByRole('link')).toHaveLength(1);
});

it('does not land in an agent that is being deleted', async () => {
  vi.stubGlobal('WebSocket', FakeSocket);
  mockApi({
    'GET /me': me,
    'GET /harnesses': [],
    'GET /agents': [
      agent({ id: 'a1', name: 'one', state: 'running' }),
      agent({ id: 'a2', name: 'two', state: 'destroying' }),
    ],
    'GET /agents/a1/sessions': [
      session({ id: 's1', title: 'Older', updatedAt: '2030-01-01T00:00:00Z' }),
    ],
    'GET /agents/a2/sessions': [
      session({
        id: 's2',
        agentId: 'a2',
        title: 'Newest',
        updatedAt: '2030-01-02T00:00:00Z',
      }),
    ],
    'GET /agents/a1': agent({ id: 'a1', name: 'one', state: 'running' }),
  });
  const { router } = renderApp('/');
  await vi.waitFor(() => expect(router.state.location.href).toBe('/a/a1?s=s1'));
  const chats = await screen.findByRole('list', { name: 'Chats' });
  expect(within(chats).queryByText('Newest')).not.toBeInTheDocument();
});

it('starts a chat from the draft composer', async () => {
  vi.stubGlobal('WebSocket', FakeSocket);
  const { calls } = mockApi({
    'GET /me': me,
    'GET /harnesses': [
      { id: 'claude-code', name: 'Claude Code', default: true },
    ],
    'GET /agents/a1': agent({ state: 'running' }),
    'GET /agents/a1/sessions': [],
    'POST /agents/a1/sessions': () => ({
      status: 201,
      body: session({ id: 's9' }),
    }),
    'POST /sessions/s9/prompt': { status: 202 },
  });
  const { router } = renderApp('/a/a1');
  expect(
    await screen.findByRole('heading', { name: /Start a chat with builder/ }),
  ).toBeVisible();
  // The composer names the harness and model.
  expect(await screen.findByText(/Claude Code/)).toBeVisible();
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Message' }),
    'build it{Enter}',
  );
  await vi.waitFor(() => expect(router.state.location.href).toBe('/a/a1?s=s9'));
  expect(calls.find((c) => c.key === 'POST /sessions/s9/prompt')?.body).toEqual(
    { text: 'build it' },
  );
});

it('toggles the agent panel with its chats and manage links', async () => {
  vi.stubGlobal('WebSocket', FakeSocket);
  mockApi({
    'GET /me': me,
    'GET /harnesses': [],
    'GET /agents/a1': agent({ state: 'running' }),
    'GET /agents/a1/sessions': [session()],
  });
  const { router } = renderApp('/a/a1?s=s1');
  await userEvent.click(
    await screen.findByRole('button', { name: 'Agent panel' }),
  );
  const panel = await screen.findByRole('complementary', {
    name: 'Agent panel',
  });
  expect(router.state.location.search).toEqual({ s: 's1', panel: 'overview' });
  const list = within(panel).getByRole('list', { name: 'Sessions' });
  expect(within(list).getByText('Fix the build')).toBeVisible();
  expect(within(panel).getByRole('link', { name: /Secrets/ })).toHaveAttribute(
    'href',
    '/a/a1/settings?tab=secrets',
  );
  await userEvent.click(
    within(panel).getByRole('button', { name: 'Close panel' }),
  );
  expect(
    screen.queryByRole('complementary', { name: 'Agent panel' }),
  ).toBeNull();
});
