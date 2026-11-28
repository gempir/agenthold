import { stubGlobal } from '@/test-setup';
import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it } from 'bun:test';
import type { Agent, Session } from '@/api/queries';
import { clearPanelTabs } from '@/components/agent/panelTabs';
import { mockApi, renderApp } from '@/test-utils';

const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
};
const agent: Agent = {
  id: 'a1',
  name: 'builder',
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
};
const session = (over: Partial<Session> = {}): Session => ({
  id: 's1',
  agentId: 'a1',
  title: 'Implement auth',
  running: false,
  cwd: 'web',
  git: { project: 'web', branch: 'feat/auth', repoPath: 'web' },
  createdBy: me.user,
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
  ...over,
});
const sub = session({
  id: 's2',
  title: 'Review auth diff',
  running: true,
  origin: {
    agentId: 'a1',
    agentName: 'builder',
    sessionId: 's1',
    sessionTitle: 'Implement auth',
  },
});

class FakeSocket {
  static all: FakeSocket[] = [];
  onopen?: () => void;
  onmessage?: (e: { data: string }) => void;
  onclose?: () => void;
  constructor(public url: string) {
    FakeSocket.all.push(this);
  }
  close() {}
}

const textFile = (path: string, content: string) => ({
  path,
  size: content.length,
  modTime: '2030-01-01T00:00:00Z',
  mimeType: 'text/plain',
  isBinary: false,
  truncated: false,
  content,
});

beforeEach(() => {
  FakeSocket.all = [];
  stubGlobal('WebSocket', FakeSocket);
  clearPanelTabs('a1');
});

function setup() {
  return mockApi({
    'GET /me': me,
    'GET /harnesses': [],
    'GET /agents': [agent],
    'GET /agents/a1': agent,
    'GET /agents/a1/sessions': [session(), sub],
    'GET /agents/a1/models': { models: [], source: 'fallback' },
    'GET /agents/a1/files': {
      path: '/home/agent/web',
      entries: [
        {
          name: 'main.go',
          size: 20,
          mode: '0644',
          isDir: false,
          isLink: false,
          modTime: '2030-01-01T00:00:00Z',
        },
      ],
    },
    'GET /agents/a1/files/read?path=web%2Fmain.go': textFile(
      'web/main.go',
      'package main\nfunc main() {}\nvar x = 1\n',
    ),
    'GET /agents/a1/files/read?path=web%2Fsrc%2Fapp.ts': textFile(
      'web/src/app.ts',
      'export const a = 1;\n',
    ),
  });
}

const panel = () => screen.getByRole('complementary', { name: 'Agent panel' });

it('opens a file tab from a link, beside the built-in tabs, and closes it', async () => {
  setup();
  const { router } = renderApp('/a/a1?s=s1&panel=file:web/main.go:3');
  const tab = await within(
    await screen.findByRole('tablist', { name: 'Open tabs' }),
  ).findByRole('tab', { name: 'main.go:3' });
  expect(tab).toHaveAttribute('aria-selected', 'true');
  const viewer = await within(panel()).findByTestId('file-viewer');
  expect(
    await within(viewer).findByRole('row', { current: true }),
  ).toHaveTextContent('3var x = 1');
  // Switching to a built-in tab keeps the file tab open.
  await userEvent.click(within(panel()).getByRole('tab', { name: 'Overview' }));
  expect(router.state.location.search).toEqual({ s: 's1', panel: 'overview' });
  expect(
    within(panel()).getByRole('tab', { name: 'main.go:3' }),
  ).toHaveAttribute('aria-selected', 'false');
  await userEvent.click(
    within(panel()).getByRole('tab', { name: 'main.go:3' }),
  );
  await userEvent.click(
    within(panel()).getByRole('button', { name: 'Close main.go:3' }),
  );
  await waitFor(() =>
    expect(router.state.location.search).toEqual({ s: 's1', panel: 'files' }),
  );
  expect(screen.queryByRole('tablist', { name: 'Open tabs' })).toBeNull();
});

it('opens a file from the Files tab in the viewer', async () => {
  setup();
  const { router } = renderApp('/a/a1?s=s1&panel=files');
  const aside = await screen.findByRole('complementary', {
    name: 'Agent panel',
  });
  await userEvent.click(
    await within(aside).findByRole('button', { name: 'main.go' }),
  );
  await waitFor(() =>
    expect(router.state.location.search).toEqual({
      s: 's1',
      panel: 'file:web/main.go',
    }),
  );
  expect(await within(panel()).findByText('package')).toBeVisible();
  expect(
    within(panel()).queryByRole('link', { name: 'Download main.go' }),
  ).toBeNull();
});

it('opens a sub-thread in the main view on Cmd-click', async () => {
  setup();
  const { router } = renderApp('/a/a1?s=s1');
  const bar = await screen.findByTestId('subthreads-bar');
  const user = userEvent.setup();
  await user.keyboard('{Meta>}');
  await user.click(within(bar).getByRole('link', { name: /Review auth diff/ }));
  await user.keyboard('{/Meta}');
  await waitFor(() =>
    expect(router.state.location.search).toEqual({ s: 's2' }),
  );
});

it('opens file links in replies at their line, relative to the thread', async () => {
  setup();
  const { router } = renderApp('/a/a1?s=s1');
  await waitFor(() => expect(FakeSocket.all.length).toBeGreaterThan(0));
  const sock = FakeSocket.all.find((s) => s.url.includes('/sessions/s1/'))!;
  act(() =>
    sock.onmessage?.({
      data: JSON.stringify({
        seq: 1,
        at: '2030-01-01T00:00:00Z',
        type: 'update',
        data: {
          sessionUpdate: 'agent_message_chunk',
          content: { type: 'text', text: 'See [app.ts:1](src/app.ts:1).' },
        },
      }),
    }),
  );
  await userEvent.click(await screen.findByRole('link', { name: 'app.ts:1' }));
  await waitFor(() =>
    expect(router.state.location.search).toEqual({
      s: 's1',
      panel: 'file:web/src/app.ts:1',
    }),
  );
  expect(await within(panel()).findByText('const')).toBeVisible();
});
