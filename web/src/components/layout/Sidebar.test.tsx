import { stubGlobal } from '@/test-setup';
import { waitFor, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it } from 'bun:test';
import type { Agent, Session } from '@/api/queries';
import { initials } from '@/components/AgentBadge';
import { SETTLED_OPEN_PREF } from '@/components/layout/Sidebar';
import { mockApi, renderApp } from '@/test-utils';

const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
};
const agent = (over: Partial<Agent> = {}): Agent => ({
  id: 'a1',
  name: 'web-dev',
  owner: me.user,
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
const settled = (i: number) =>
  session({
    id: `old${i}`,
    title: `Old thread ${i}`,
    updatedAt: `2029-12-${String(30 - i).padStart(2, '0')}T00:00:00Z`,
    settledAt: '2030-01-01T00:00:00Z',
    settledBy: 'auto',
  });

class FakeSocket {
  onopen?: () => void;
  onmessage?: (e: { data: string }) => void;
  onclose?: () => void;
  constructor(public url: string) {}
  close() {}
}

beforeEach(() => {
  localStorage.clear();
  stubGlobal('WebSocket', FakeSocket);
});

function setup(sessions: Session[]) {
  let list = sessions;
  const replace = (id: string, over: Partial<Session>) => {
    list = list.map((s) => (s.id === id ? { ...s, ...over } : s));
    return list.find((s) => s.id === id);
  };
  const { calls } = mockApi({
    'GET /me': me,
    'GET /harnesses': [],
    'GET /apps': [],
    'GET /agents': [agent()],
    'GET /agents/a1': agent(),
    'GET /agents/a1/sessions': () => ({ body: list }),
    'POST /sessions/s1/settle': () => ({
      body: replace('s1', {
        settledAt: '2030-01-02T00:00:00Z',
        settledBy: 'user',
      }),
    }),
    'POST /sessions/old0/unsettle': () => ({
      body: replace('old0', { settledAt: undefined, settledBy: undefined }),
    }),
  });
  return { calls };
}

it('shows active threads as cards and settles one from its hover button', async () => {
  const { calls } = setup([session(), settled(0)]);
  renderApp('/settings/threads');
  const threads = await screen.findByRole('list', { name: 'Threads' });
  const card = (await within(threads).findByText('Fix the build')).closest(
    'li',
  )!;
  expect(within(card).getByText('web-dev')).toBeVisible();
  expect(within(card).getByText('WD')).toBeInTheDocument();
  const settle = within(card).getByRole('button', {
    name: 'Settle thread Fix the build',
  });
  expect(settle).toHaveAttribute('title', 'Settle thread');
  await userEvent.click(settle);
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'POST /sessions/s1/settle')).toBe(true),
  );
  await waitFor(() =>
    expect(within(threads).queryByText('Fix the build')).toBeNull(),
  );
  expect(within(threads).getByText(/No active threads/)).toBeVisible();
  // It moved into the folded Settled section.
  const header = screen.getByRole('button', { name: /Settled/ });
  expect(header).toHaveTextContent('2');
});

it('folds and unfolds the Settled section, remembering it, with Show more', async () => {
  setup([session(), ...Array.from({ length: 13 }, (_, i) => settled(i))]);
  renderApp('/settings/threads');
  const header = await screen.findByRole('button', { name: /Settled/ });
  await waitFor(() => expect(header).toHaveTextContent('13'));
  expect(header).toHaveAttribute('aria-expanded', 'false');
  expect(screen.queryByRole('list', { name: 'Settled threads' })).toBeNull();

  await userEvent.click(header);
  expect(header).toHaveAttribute('aria-expanded', 'true');
  expect(localStorage.getItem(`agenthold.${SETTLED_OPEN_PREF}`)).toBe('1');
  const list = screen.getByRole('list', { name: 'Settled threads' });
  expect(within(list).getAllByRole('link')).toHaveLength(10);
  await userEvent.click(
    within(list).getByRole('button', { name: 'Show 3 more' }),
  );
  expect(within(list).getAllByRole('link')).toHaveLength(13);
  // Relative time of the last activity, right-aligned in the row.
  const row = within(list).getByText('Old thread 0').closest('a')!;
  expect(row).toHaveAttribute('href', '/a/a1?s=old0');

  await userEvent.click(header);
  expect(header).toHaveAttribute('aria-expanded', 'false');
  expect(localStorage.getItem(`agenthold.${SETTLED_OPEN_PREF}`)).toBe('');
});

it('unsettles a settled thread from its row', async () => {
  localStorage.setItem(`agenthold.${SETTLED_OPEN_PREF}`, '1');
  const { calls } = setup([settled(0)]);
  renderApp('/settings/threads');
  const list = await screen.findByRole('list', { name: 'Settled threads' });
  await userEvent.click(
    await within(list).findByRole('button', {
      name: 'Unsettle thread Old thread 0',
    }),
  );
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'POST /sessions/old0/unsettle')).toBe(
      true,
    ),
  );
  const threads = screen.getByRole('list', { name: 'Threads' });
  expect(await within(threads).findByText('Old thread 0')).toBeVisible();
});

it('the search looks into settled threads too', async () => {
  setup([session(), settled(0), settled(1)]);
  renderApp('/settings/threads');
  await screen.findByRole('button', { name: /Settled/ });
  await userEvent.type(
    screen.getByRole('searchbox', { name: 'Search threads, agents and apps' }),
    'thread 1',
  );
  const list = screen.getByRole('list', { name: 'Settled threads' });
  expect(within(list).getAllByRole('link')).toHaveLength(1);
});

it('settles and unsettles from the thread view header', async () => {
  const { calls } = setup([session()]);
  renderApp('/a/a1?s=s1');
  await userEvent.click(await screen.findByRole('button', { name: 'Settle' }));
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'POST /sessions/s1/settle')).toBe(true),
  );
  expect(
    await screen.findByRole('button', { name: 'Unsettle' }),
  ).toHaveAttribute('title', 'Unsettle thread');
  // The title in the header says it is settled.
  const crumb = screen.getByText('Fix the build', { ignore: 'nav *' });
  expect(crumb.parentElement).toHaveTextContent('Fix the buildSettled');
});

it('makes initials from agent names', () => {
  expect(initials('web-dev')).toBe('WD');
  expect(initials('builder')).toBe('BU');
  expect(initials('my cool agent')).toBe('MC');
  expect(initials('x')).toBe('X');
  expect(initials('--')).toBe('?');
});

it('shows the branch of active and settled threads', async () => {
  setup([
    session({ git: { project: 'web', branch: 'feat/login' } }),
    { ...settled(0), git: { project: 'web', branch: 'fix/old-bug' } },
  ]);
  renderApp('/settings/threads');
  const threads = await screen.findByRole('list', { name: 'Threads' });
  expect(await within(threads).findByTestId('chat-git')).toHaveTextContent(
    'feat/login',
  );
  await userEvent.click(screen.getByRole('button', { name: /Settled/ }));
  const list = screen.getByRole('list', { name: 'Settled threads' });
  expect(within(list).getByTestId('chat-branch')).toHaveTextContent(
    'fix/old-bug',
  );
});

it('switches the sidebar between threads, agents and apps from the footer bar', async () => {
  mockApi({
    'GET /me': me,
    'GET /harnesses': [],
    'GET /agents': [
      agent({ id: 'a1', name: 'web-dev', state: 'running' }),
      agent({ id: 'a2', name: 'worker', state: 'sleeping' }),
    ],
    'GET /agents/a1/sessions': [session()],
    'GET /agents/a2/sessions': [],
    'GET /apps': [
      {
        id: 'ap1',
        name: 'demo-app',
        owner: me.user,
        cpus: 1,
        memoryMb: 1024,
        idleMinutes: 0,
        state: 'running',
        stateError: '',
        stateChangedAt: '2030-01-01T00:00:00Z',
        needsRebuild: false,
        isTemplate: false,
        role: 'owner',
        url: 'https://demo.apps.example.com/',
        links: {},
        createdAt: '2030-01-01T00:00:00Z',
        updatedAt: '2030-01-01T00:00:00Z',
      },
    ],
  });
  renderApp('/settings/threads');
  const nav = await screen.findByRole('navigation', { name: 'Sidebar' });

  // No Projects section in the sidebar; Settings button lives in the footer.
  expect(within(nav).queryByRole('button', { name: /Projects/ })).toBeNull();
  expect(within(nav).getByRole('link', { name: 'Settings' })).toHaveAttribute(
    'href',
    '/settings',
  );
  expect(
    within(nav).getByRole('button', { name: 'Account menu' }),
  ).toBeVisible();

  const agentsBtn = await within(nav).findByRole('button', {
    name: 'Agents (2)',
  });
  const appsBtn = within(nav).getByRole('button', { name: 'Apps (1)' });
  expect(within(nav).getByRole('list', { name: 'Threads' })).toBeVisible();

  // Clicking Agents switches the sidebar to list all agents.
  await userEvent.click(agentsBtn);
  expect(agentsBtn).toHaveAttribute('aria-pressed', 'true');
  expect(within(nav).queryByRole('list', { name: 'Threads' })).toBeNull();
  const agentsList = within(nav).getByRole('list', { name: 'Agents' });
  expect(within(agentsList).getByText('web-dev')).toBeVisible();
  expect(within(agentsList).getByText('worker')).toBeVisible();

  // Clicking Apps switches the sidebar to list apps.
  await userEvent.click(appsBtn);
  expect(appsBtn).toHaveAttribute('aria-pressed', 'true');
  expect(agentsBtn).toHaveAttribute('aria-pressed', 'false');
  const appsList = within(nav).getByRole('list', { name: 'Apps' });
  expect(within(appsList).getByText('demo-app')).toBeVisible();
  expect(within(appsList).getByText('demo.apps.example.com')).toBeVisible();

  // Clicking Apps again toggles back to Threads.
  await userEvent.click(appsBtn);
  expect(appsBtn).toHaveAttribute('aria-pressed', 'false');
  expect(within(nav).getByRole('list', { name: 'Threads' })).toBeVisible();
});
