import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import {
  agentPollInterval,
  agentsPollInterval,
  type Agent,
  type GitRepo,
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
    'GET /agents?template=true': [],
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

it('shows an offline node and disables sandbox controls', async () => {
  mockApi({
    'GET /me': me,
    'GET /agents/a1': agent({
      role: 'owner',
      state: 'unreachable',
      nodeId: 'edge-1',
      nodeName: 'Remote host',
    }),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
  });
  renderApp('/a/a1/settings');
  expect(await screen.findByText('Node offline')).toBeVisible();
  expect(screen.getByText(/Remote host is offline/)).toBeVisible();
  expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Stop' })).toBeDisabled();
});

it('disables the chat composer while the assigned node is offline', async () => {
  mockApi({
    'GET /me': me,
    'GET /agents/a1': agent({
      state: 'unreachable',
      nodeId: 'edge-1',
      nodeName: 'Remote host',
    }),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
  });
  renderApp('/a/a1');
  expect(await screen.findByText(/Remote host is offline/)).toBeVisible();
  expect(screen.getByRole('textbox', { name: 'Message' })).toBeDisabled();
});

it('polls fast while transient and slowly while running', () => {
  expect(agentPollInterval(agent({ state: 'starting' }))).toBe(1000);
  expect(agentPollInterval(agent({ state: 'destroying' }))).toBe(1000);
  expect(agentPollInterval(agent({ state: 'running' }))).toBe(10_000);
  expect(agentPollInterval(agent({ state: 'sleeping' }))).toBe(10_000);
  expect(agentPollInterval(undefined)).toBe(false);
  expect(
    agentsPollInterval([
      agent({ state: 'running' }),
      agent({ state: 'creating' }),
    ]),
  ).toBe(1000);
  expect(agentsPollInterval([agent({ state: 'sleeping' })])).toBe(10_000);
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
  expect(within(nav).queryByRole('link', { name: 'Audit log' })).toBeNull();
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

it('lands in the most recent thread across agents', async () => {
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
  const chats = await screen.findByRole('list', { name: 'Threads' });
  const rows = within(chats).getAllByRole('link');
  expect(rows.map((r) => r.textContent)).toEqual([
    expect.stringContaining('Newest'),
    expect.stringContaining('Older'),
  ]);
  expect(rows[0]).toHaveAttribute('aria-current', 'page');
  expect(within(rows[0]).getByRole('img', { name: 'running' })).toBeVisible();
  // The running thread says so on its card.
  expect(within(rows[0]).getByText('Working…')).toBeVisible();
  // The sidebar search filters threads.
  await userEvent.type(
    screen.getByRole('searchbox', { name: 'Search threads and agents' }),
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
  const chats = await screen.findByRole('list', { name: 'Threads' });
  expect(within(chats).queryByText('Newest')).not.toBeInTheDocument();
});

it('starts a thread from the draft composer', async () => {
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
    await screen.findByRole('heading', { name: /Start a thread with builder/ }),
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

it('toggles the agent panel with its threads and manage links', async () => {
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
  const list = within(panel).getByRole('list', { name: 'Threads' });
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

it('a new thread starts with the model picked in the composer', async () => {
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents/a1': agent({ role: 'owner', state: 'running' }),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
    'GET /agents/a1/models': {
      harness: 'claude-code',
      source: 'harness',
      seenAt: '2030-01-01T00:00:00Z',
      switchable: true,
      customModels: false,
      models: [
        {
          id: 'opus',
          name: 'Opus 5.5',
          group: 'Anthropic',
          options: [
            {
              id: 'effort',
              name: 'Effort',
              kind: 'effort',
              values: [
                { value: 'low', name: 'Low' },
                { value: 'high', name: 'High' },
              ],
            },
          ],
        },
      ],
    },
    'POST /agents/a1/sessions': {
      id: 's1',
      agentId: 'a1',
      title: 'New session',
      running: false,
      createdBy: me.user,
      createdAt: '2030-01-01T00:00:00Z',
      updatedAt: '2030-01-01T00:00:00Z',
    },
    'PUT /sessions/s1/model': {},
    'POST /sessions/s1/prompt': { status: 202 },
  });
  renderApp('/a/a1');
  const user = userEvent.setup();
  const trigger = await screen.findByRole('button', {
    name: /Model for this conversation: Agent default/,
  });
  await user.click(trigger);
  await user.click(await screen.findByRole('option', { name: /Opus 5.5/ }));
  // The picker stays open: reasoning is set right after the model.
  await user.click(screen.getByRole('radio', { name: 'High' }));
  expect(trigger).toHaveTextContent('Opus 5.5 · High');
  await user.type(
    screen.getByRole('textbox', { name: 'Message' }),
    'hi{Enter}',
  );
  await vi.waitFor(() =>
    expect(calls.map((c) => c.key)).toContain('POST /sessions/s1/prompt'),
  );
  const keys = calls.map((c) => c.key);
  expect(keys.indexOf('PUT /sessions/s1/model')).toBeLessThan(
    keys.indexOf('POST /sessions/s1/prompt'),
  );
  expect(calls.find((c) => c.key === 'PUT /sessions/s1/model')?.body).toEqual({
    model: 'opus',
    options: { effort: 'high' },
  });
});

const logLines = [
  {
    seq: 1,
    at: '2030-01-01T00:00:00Z',
    level: 'info',
    source: 'machine',
    message: 'wake: machine started, waiting for sandboxd took=420ms',
  },
  {
    seq: 2,
    at: '2030-01-01T00:00:01Z',
    level: 'warn',
    source: 'tunnel',
    message: 'tunnel: rejected err="invalid boot token"',
  },
];

it('streams the activity log in its own panel tab', async () => {
  vi.stubGlobal('WebSocket', FakeSocket);
  const { calls } = mockApi({
    'GET /me': me,
    'GET /harnesses': [],
    'GET /agents/a1': agent({ role: 'owner', state: 'running' }),
    'GET /agents/a1/sessions': [session()],
    'GET /agents/a1/activity': logLines,
  });
  const { router } = renderApp('/a/a1?s=s1&panel=activity');
  const panel = await screen.findByRole('complementary', {
    name: 'Agent panel',
  });
  const tab = within(panel).getByRole('tab', { name: 'Activity' });
  expect(tab).toHaveAttribute('aria-selected', 'true');
  expect(await within(panel).findByText(/invalid boot token/)).toBeVisible();
  expect(within(panel).getByTestId('live-log')).toHaveTextContent(
    'waiting for sandboxd',
  );
  expect(calls.some((c) => c.key === 'GET /agents/a1/activity')).toBe(true);
  // The tabs: Overview, Git, Files, Terminal, Activity.
  expect(
    within(panel)
      .getAllByRole('tab')
      .map((t) => t.textContent),
  ).toEqual(['Overview', 'Git', 'Files', 'Terminal', 'Activity']);
  await userEvent.click(within(panel).getByRole('tab', { name: 'Overview' }));
  expect(router.state.location.search).toEqual({ s: 's1', panel: 'overview' });
  expect(screen.queryByTestId('live-log')).toBeNull();
});

it('the chat links to the Activity tab while it waits', async () => {
  vi.stubGlobal('WebSocket', FakeSocket);
  mockApi({
    'GET /me': me,
    'GET /harnesses': [],
    'GET /agents/a1': agent({ role: 'owner', state: 'sleeping' }),
    'GET /agents/a1/sessions': [session({ running: true })],
    'GET /agents/a1/activity': logLines,
  });
  const { router } = renderApp('/a/a1?s=s1');
  await userEvent.click(
    await screen.findByRole('button', { name: /What is happening/ }),
  );
  expect(router.state.location.search).toEqual({ s: 's1', panel: 'activity' });
  const panel = await screen.findByRole('complementary', {
    name: 'Agent panel',
  });
  expect(await within(panel).findByText(/invalid boot token/)).toBeVisible();
});

const harnessTypes = [
  { id: 'claude-code', name: 'Claude Code', default: true },
  { id: 'codex', name: 'Codex', default: false },
  { id: 'opencode', name: 'OpenCode', default: false },
];
const claudeModels = {
  harness: 'claude-code',
  source: 'harness',
  seenAt: '2030-01-01T00:00:00Z',
  switchable: true,
  customModels: false,
  models: [{ id: 'opus', name: 'Opus 5.5', group: 'Anthropic' }],
  login: { source: 'account', connected: true, mine: true },
};
const codexModels = {
  harness: 'codex',
  source: 'fallback',
  switchable: true,
  customModels: false,
  models: [],
  login: { source: 'account', connected: true, mine: true },
};
const opencodeModels = {
  harness: 'opencode',
  source: 'harness',
  seenAt: '2030-01-01T00:00:00Z',
  switchable: true,
  customModels: true,
  models: [
    { id: 'anthropic/sonnet', name: 'Sonnet', group: 'anthropic' },
    { id: 'openai/gpt-5', name: 'GPT-5', group: 'openai' },
  ],
  login: { source: 'sandbox', connected: false, mine: true },
};

it('the owner switches the agent to another harness from the model picker', async () => {
  let harness = 'claude-code';
  const { calls } = mockApi({
    'GET /me': me,
    'GET /harnesses': harnessTypes,
    'GET /agents/a1': () => ({
      body: agent({ role: 'owner', state: 'running', harness, name: 'demo2' }),
    }),
    'GET /agents/a1/sessions': [],
    'GET /agents/a1/models': claudeModels,
    'GET /agents/a1/models?harness=codex': codexModels,
    'GET /agents/a1/models?harness=opencode': opencodeModels,
    'PATCH /agents/a1': ({ body }: { body: unknown }) => {
      harness = (body as { harness: string }).harness;
      return {
        body: agent({
          role: 'owner',
          state: 'running',
          harness,
          name: 'demo2',
        }),
      };
    },
  });
  renderApp('/a/a1');
  const user = userEvent.setup();
  const trigger = await screen.findByRole('button', {
    name: /Model for this conversation: Agent default/,
  });
  expect(trigger).toHaveTextContent('Claude Code');
  await user.click(trigger);
  const list = await screen.findByRole('listbox', { name: 'Models' });
  // Codex never reported: one row, "Agent default".
  await user.click(await within(list).findByRole('option', { name: /Codex/ }));
  const confirm = screen.getByRole('group', { name: 'Switch to Codex' });
  expect(confirm).toHaveTextContent('Switches demo2 to Codex (all chats)');
  await user.click(
    within(confirm).getByRole('button', { name: 'Switch to Codex' }),
  );
  await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(calls.find((c) => c.key === 'PATCH /agents/a1')?.body).toEqual({
    harness: 'codex',
    model: '',
    modelOptions: {},
  });
  // The composer now names the new harness.
  await vi.waitFor(() =>
    expect(
      screen.getByRole('button', { name: /Model for this conversation/ }),
    ).toHaveTextContent('Codex'),
  );
});

it('switching to a model of another harness sends it along', async () => {
  const { calls } = mockApi({
    'GET /me': me,
    'GET /harnesses': harnessTypes,
    'GET /agents/a1': agent({ role: 'owner', state: 'running' }),
    'GET /agents/a1/sessions': [],
    'GET /agents/a1/models': claudeModels,
    'GET /agents/a1/models?harness=codex': codexModels,
    'GET /agents/a1/models?harness=opencode': opencodeModels,
    'PATCH /agents/a1': agent({
      role: 'owner',
      state: 'running',
      harness: 'opencode',
      model: 'openai/gpt-5',
    }),
  });
  renderApp('/a/a1');
  const user = userEvent.setup();
  await user.click(
    await screen.findByRole('button', { name: /Model for this conversation/ }),
  );
  const list = await screen.findByRole('listbox', { name: 'Models' });
  await user.click(
    await within(list).findByRole('option', { name: /OpenCode/ }),
  );
  await user.click(within(list).getByRole('option', { name: 'GPT-5' }));
  const confirm = screen.getByRole('group', { name: 'Switch to OpenCode' });
  // It has no account login: signing in inside the sandbox is the way.
  expect(confirm).toHaveTextContent('Model: GPT-5');
  await user.click(within(confirm).getByRole('button', { name: /Switch/ }));
  await vi.waitFor(() =>
    expect(calls.find((c) => c.key === 'PATCH /agents/a1')?.body).toEqual({
      harness: 'opencode',
      model: 'openai/gpt-5',
      modelOptions: {},
    }),
  );
});

it('says when the new harness has no login and shows the server error of a refused switch', async () => {
  mockApi({
    'GET /me': me,
    'GET /harnesses': harnessTypes,
    'GET /agents/a1': agent({ role: 'owner', state: 'running' }),
    'GET /agents/a1/sessions': [],
    'GET /agents/a1/models': claudeModels,
    'GET /agents/a1/models?harness=codex': {
      ...codexModels,
      login: { source: 'account', connected: false, mine: true },
    },
    'GET /agents/a1/models?harness=opencode': opencodeModels,
    'PATCH /agents/a1': () => ({
      status: 409,
      body: { error: 'a conversation of this agent is still running' },
    }),
  });
  renderApp('/a/a1');
  const user = userEvent.setup();
  await user.click(
    await screen.findByRole('button', { name: /Model for this conversation/ }),
  );
  await user.click(await screen.findByRole('option', { name: /Codex/ }));
  const confirm = screen.getByRole('group', { name: 'Switch to Codex' });
  expect(confirm).toHaveTextContent('No Codex login');
  expect(
    within(confirm).getByRole('link', { name: /Provider logins/ }),
  ).toHaveAttribute('href', '/settings/providers');
  await user.click(
    within(confirm).getByRole('button', { name: 'Switch anyway' }),
  );
  expect(await within(confirm).findByRole('alert')).toHaveTextContent(
    'still running',
  );
  expect(screen.getByRole('dialog')).toBeVisible();
});

it('shared users see only the current harness in the model picker', async () => {
  const { calls } = mockApi({
    'GET /me': { user: bob },
    'GET /harnesses': harnessTypes,
    'GET /agents/a1': agent({ role: 'shared', state: 'running' }),
    'GET /agents/a1/sessions': [],
    'GET /agents/a1/models': claudeModels,
  });
  renderApp('/a/a1');
  const user = userEvent.setup();
  await user.click(
    await screen.findByRole('button', { name: /Model for this conversation/ }),
  );
  const list = await screen.findByRole('listbox', { name: 'Models' });
  expect(within(list).getByRole('option', { name: /Opus/ })).toBeVisible();
  expect(within(list).queryByRole('option', { name: /Codex/ })).toBeNull();
  expect(screen.queryByText(/Switch .* to/)).toBeNull();
  expect(screen.queryByRole('link', { name: 'Harness settings' })).toBeNull();
  // Not even asked: other harnesses are the owner's to see.
  expect(calls.some((c) => c.key.includes('models?harness'))).toBe(false);
});

const repo = (over: Partial<GitRepo> = {}): GitRepo => ({
  path: 'projects/web',
  project: 'web',
  branch: 'main',
  detached: false,
  dirty: false,
  ahead: 0,
  behind: 0,
  isWorktree: false,
  ...over,
});

it('the sidebar shows project and branch of a thread, and nothing for one without', async () => {
  vi.stubGlobal('WebSocket', FakeSocket);
  mockApi({
    'GET /me': me,
    'GET /harnesses': [],
    'GET /apps': [],
    'GET /agents': [agent({ id: 'a1', state: 'running' })],
    'GET /agents/a1': agent({ id: 'a1', state: 'running' }),
    'GET /agents/a1/sessions': [
      session({
        id: 's1',
        title: 'On a branch',
        updatedAt: '2030-01-02T00:00:00Z',
        git: { project: 'web', branch: 'feat/login' },
      }),
      session({ id: 's2', title: 'Plain', updatedAt: '2030-01-01T00:00:00Z' }),
    ],
  });
  renderApp('/a/a1?s=s2');
  const chats = await screen.findByRole('list', { name: 'Threads' });
  const [withGit, plain] = await vi.waitFor(() => {
    const rows = within(chats).getAllByRole('link');
    expect(rows).toHaveLength(2);
    return rows;
  });
  const line = within(withGit).getByTestId('chat-git');
  expect(line).toHaveTextContent('web');
  expect(line).toHaveTextContent('feat/login');
  expect(line).toHaveAttribute('title', 'web · feat/login');
  expect(within(plain).queryByTestId('chat-git')).toBeNull();
});

it('the sidebar search matches threads by project and branch', async () => {
  vi.stubGlobal('WebSocket', FakeSocket);
  mockApi({
    'GET /me': me,
    'GET /harnesses': [],
    'GET /apps': [],
    'GET /agents': [agent({ id: 'a1', state: 'running' })],
    'GET /agents/a1': agent({ id: 'a1', state: 'running' }),
    'GET /agents/a1/sessions': [
      session({
        id: 's1',
        title: 'First',
        updatedAt: '2030-01-02T00:00:00Z',
        git: { project: 'storefront', branch: 'feat/login' },
      }),
      session({
        id: 's2',
        title: 'Second',
        updatedAt: '2030-01-01T00:00:00Z',
        git: { project: 'backend', branch: 'main' },
      }),
    ],
  });
  renderApp('/a/a1?s=s1');
  const chats = await screen.findByRole('list', { name: 'Threads' });
  await vi.waitFor(() =>
    expect(within(chats).getAllByRole('link')).toHaveLength(2),
  );
  const search = screen.getByRole('searchbox', {
    name: 'Search threads and agents',
  });
  await userEvent.type(search, 'storefr');
  expect(within(chats).getAllByRole('link')).toHaveLength(1);
  expect(within(chats).getByText('First')).toBeVisible();
  await userEvent.clear(search);
  await userEvent.type(search, 'feat/log');
  expect(within(chats).getAllByRole('link')).toHaveLength(1);
  expect(within(chats).getByText('First')).toBeVisible();
  await userEvent.clear(search);
  await userEvent.type(search, 'backend');
  expect(within(chats).getAllByRole('link')).toHaveLength(1);
  expect(within(chats).getByText('Second')).toBeVisible();
});

it('starting a chat with a selected repository passes its cwd', async () => {
  vi.stubGlobal('WebSocket', FakeSocket);
  const { calls } = mockApi({
    'GET /me': me,
    'GET /harnesses': [
      { id: 'claude-code', name: 'Claude Code', default: true },
    ],
    'GET /agents/a1': agent({ state: 'running' }),
    'GET /agents/a1/sessions': [],
    'GET /agents/a1/git/repos': {
      connected: true,
      repos: [repo(), repo({ path: 'projects/api', project: 'api' })],
    },
    'POST /agents/a1/sessions': () => ({
      status: 201,
      body: session({ id: 's9' }),
    }),
    'POST /sessions/s9/prompt': { status: 202 },
  });
  const { router } = renderApp('/a/a1');
  await userEvent.click(await screen.findByRole('button', { name: 'Project' }));
  await userEvent.click(await screen.findByRole('menuitem', { name: /api/ }));
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Message' }),
    'go{Enter}',
  );
  await vi.waitFor(() => expect(router.state.location.href).toBe('/a/a1?s=s9'));
  expect(calls.find((c) => c.key === 'POST /agents/a1/sessions')?.body).toEqual(
    { cwd: 'projects/api' },
  );
});

it('a ?cwd= preselects the repository of the draft', async () => {
  vi.stubGlobal('WebSocket', FakeSocket);
  const { calls } = mockApi({
    'GET /me': me,
    'GET /harnesses': [],
    'GET /agents/a1': agent({ state: 'running' }),
    'GET /agents/a1/sessions': [],
    'GET /agents/a1/git/repos': { connected: true, repos: [repo()] },
    'POST /agents/a1/sessions': () => ({
      status: 201,
      body: session({ id: 's9' }),
    }),
    'POST /sessions/s9/prompt': { status: 202 },
  });
  renderApp('/a/a1?cwd=projects/web');
  const trigger = await screen.findByRole('button', { name: 'Project' });
  await vi.waitFor(() => expect(trigger).toHaveTextContent('web'));
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Message' }),
    'hi{Enter}',
  );
  await vi.waitFor(() =>
    expect(calls.some((c) => c.key === 'POST /agents/a1/sessions')).toBe(true),
  );
  expect(calls.find((c) => c.key === 'POST /agents/a1/sessions')?.body).toEqual(
    { cwd: 'projects/web' },
  );
});

it('a session on a branch shows it in the composer and refreshes it', async () => {
  vi.stubGlobal('WebSocket', FakeSocket);
  const { calls } = mockApi({
    'GET /me': me,
    'GET /harnesses': [],
    'GET /agents/a1': agent({ role: 'owner', state: 'running' }),
    'GET /agents/a1/sessions': [
      session({ git: { project: 'web', branch: 'feat/login' } }),
    ],
    'PATCH /sessions/s1/git': session({
      git: { project: 'web', branch: 'feat/login' },
    }),
  });
  renderApp('/a/a1?s=s1');
  const badge = await screen.findByTestId('session-git');
  expect(badge).toHaveTextContent('web');
  expect(badge).toHaveTextContent('feat/login');
  await userEvent.click(
    within(badge).getByRole('button', { name: 'Refresh git branch' }),
  );
  await vi.waitFor(() =>
    expect(calls.find((c) => c.key === 'PATCH /sessions/s1/git')?.body).toEqual(
      {
        refresh: true,
      },
    ),
  );
});
