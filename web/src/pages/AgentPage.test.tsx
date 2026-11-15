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
  personalWorkspaceId: 'w1',
};
const ws = {
  id: 'w1',
  name: 'Acme',
  personal: false,
  role: 'member',
  createdAt: '2030-01-01T00:00:00Z',
};
const agent = (over: Partial<Agent> = {}): Agent => ({
  id: 'a1',
  workspaceId: 'w1',
  name: 'builder',
  owner: me.user,
  image: 'ubuntu:24.04',
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
  role: 'operator',
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
  ...over,
});

it('renders agent list with state badges', async () => {
  mockApi({
    'GET /me': me,
    'GET /workspaces': [ws],
    'GET /workspaces/w1': ws,
    'GET /workspaces/w1/members': [],
    'GET /workspaces/w1/agents': [
      agent({ id: 'a1', name: 'one', state: 'running' }),
      agent({ id: 'a2', name: 'two', state: 'error', needsRebuild: true }),
    ],
  });
  renderApp('/w/w1');
  const main = await screen.findByRole('main');
  const one = (await within(main).findByText('one')).closest('a')!;
  expect(within(one).getByText('running')).toHaveAttribute(
    'data-state',
    'running',
  );
  const two = within(main).getByText('two').closest('a')!;
  expect(within(two).getByText('error')).toBeInTheDocument();
  expect(within(two).getByText('rebuild pending')).toBeInTheDocument();
});

it('shows Start for sleeping agent and hides admin controls for operator', async () => {
  mockApi({
    'GET /me': me,
    'GET /workspaces': [ws],
    'GET /agents/a1': agent(),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
  });
  renderApp('/a/a1/settings');
  expect(await screen.findByRole('button', { name: 'Start' })).toBeVisible();
  expect(screen.getByRole('button', { name: 'Rebuild' })).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
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
    'GET /workspaces': [ws],
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

it('sharing tab lists access and posts a user grant', async () => {
  const { calls } = mockApi({
    'GET /me': me,
    'GET /workspaces': [ws],
    'GET /agents/a1': agent({ role: 'admin' }),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
    'GET /workspaces/w1/members': [
      { user: me.user, role: 'owner', joinedAt: '2030-01-01T00:00:00Z' },
      { user: bob, role: 'member', joinedAt: '2030-01-01T00:00:00Z' },
      { user: carol, role: 'member', joinedAt: '2030-01-01T00:00:00Z' },
    ],
    'GET /agents/a1/grants': {
      grants: [],
      effective: [
        { user: me.user, role: 'admin', via: 'owner' },
        { user: bob, role: 'viewer', via: 'workspace_grant' },
      ],
    },
    'POST /agents/a1/grants': {
      id: 'g1',
      subjectType: 'user',
      role: 'operator',
      createdAt: '2030-01-01T00:00:00Z',
    },
  });
  renderApp('/a/a1/settings?tab=sharing');
  expect(await screen.findByText('Owner')).toBeInTheDocument();
  expect(screen.getByText('Everyone in workspace')).toBeInTheDocument();
  const withSel = await screen.findByLabelText('With');
  // Alice (owner) is excluded from the picker.
  expect(within(withSel).queryByRole('option', { name: /Alice/ })).toBeNull();
  await userEvent.selectOptions(withSel, 'u3');
  await userEvent.selectOptions(screen.getByLabelText('Role'), 'operator');
  await userEvent.click(screen.getByRole('button', { name: 'Share' }));
  await vi.waitFor(() =>
    expect(calls.find((c) => c.key === 'POST /agents/a1/grants')?.body).toEqual(
      { subjectType: 'user', userId: 'u3', role: 'operator' },
    ),
  );
});

it('viewer sees no composer, terminal, activity or share form', async () => {
  mockApi({
    'GET /me': me,
    'GET /workspaces': [ws],
    'GET /agents/a1': agent({ role: 'viewer', state: 'running' }),
    'GET /agents/a1/sessions': [],
    'GET /agents/a1/grants': { grants: [], effective: [] },
  });
  renderApp('/a/a1');
  expect(
    await screen.findByText(/read-only access to this agent/),
  ).toBeInTheDocument();
  expect(screen.queryByRole('textbox', { name: 'Message' })).toBeNull();
  expect(screen.queryByRole('link', { name: 'New chat' })).toBeNull();

  await userEvent.click(screen.getByRole('link', { name: 'Agent settings' }));
  const nav = await screen.findByRole('navigation', { name: 'Agent settings' });
  expect(within(nav).queryByRole('link', { name: 'Terminal' })).toBeNull();
  expect(within(nav).queryByRole('link', { name: 'Activity' })).toBeNull();
  await userEvent.click(within(nav).getByRole('link', { name: 'Sharing' }));
  await screen.findByText('People with access', { selector: 'h2' });
  expect(screen.queryByRole('button', { name: 'Share' })).toBeNull();
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
    'GET /workspaces': [ws],
    'GET /harnesses': [],
    'GET /workspaces/w1/agents': [
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
    'GET /workspaces': [ws],
    'GET /harnesses': [],
    'GET /workspaces/w1/agents': [
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
    'GET /workspaces': [ws],
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
    'GET /workspaces': [ws],
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
  expect(within(panel).getByRole('link', { name: /Network/ })).toHaveAttribute(
    'href',
    '/a/a1/settings?tab=network',
  );
  await userEvent.click(
    within(panel).getByRole('button', { name: 'Close panel' }),
  );
  expect(
    screen.queryByRole('complementary', { name: 'Agent panel' }),
  ).toBeNull();
});
