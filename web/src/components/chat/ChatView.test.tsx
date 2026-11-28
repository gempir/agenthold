import { stubGlobal } from '@/test-setup';
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, expect, it, vi } from 'bun:test';
import ChatView from './ChatView';
import type { Session } from '@/api/queries';
import {
  addComposerContext,
  clearComposerContext,
} from '@/lib/composerContext';
import { setFollowUpMode } from '@/lib/followUpPref';
import { mockApi } from '@/test-utils';

class FakeSocket {
  static all: FakeSocket[] = [];
  onopen?: () => void;
  onmessage?: (e: { data: string }) => void;
  onclose?: () => void;
  constructor(public url: string) {
    FakeSocket.all.push(this);
  }
  close() {}
  emit(o: unknown) {
    this.onmessage?.({ data: JSON.stringify(o) });
  }
}

beforeEach(() => {
  FakeSocket.all = [];
  clearComposerContext('s1');
  setFollowUpMode('queue');
  stubGlobal('WebSocket', FakeSocket);
});

const renderChat = (
  canUse: boolean,
  agentState?: 'sleeping',
  extra?: { agentId?: string; session?: Session },
) =>
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ChatView
        sessionId="s1"
        canUse={canUse}
        agentState={agentState}
        agentId={extra?.agentId}
        session={extra?.session}
      />
    </QueryClientProvider>,
  );

const ev = (seq: number, type: string, data: unknown) => ({
  seq,
  at: '2030-01-01T00:00:00Z',
  type,
  data,
});

it('renders a diff from a tool call and connects with the cursor', async () => {
  mockApi({});
  renderChat(true);
  const sock = FakeSocket.all[0];
  expect(sock.url).toMatch(/\/api\/v1\/sessions\/s1\/stream\?after=0$/);
  act(() => {
    sock.emit(
      ev(1, 'update', {
        sessionUpdate: 'tool_call',
        toolCallId: 't1',
        title: 'Edit main.go',
        kind: 'edit',
        status: 'completed',
        content: [
          {
            type: 'diff',
            path: 'main.go',
            oldText: 'a\nb\n',
            newText: 'a\nc\n',
          },
        ],
      }),
    );
  });
  expect(await screen.findByText('Edit main.go')).toBeInTheDocument();
  const diff = screen.getByTestId('diff');
  expect(diff).toHaveTextContent('-b');
  expect(diff).toHaveTextContent('+c');
});

it('shows working indicator, allows queuing/steering while running, and supports stop', async () => {
  const { calls } = mockApi({
    'GET /sessions/s1/queue': [],
    'POST /sessions/s1/queue': {
      status: 201,
      body: { started: false },
    },
    'POST /sessions/s1/cancel': { status: 202 },
  });
  renderChat(true);
  act(() => FakeSocket.all[0].emit({ type: 'status', running: true }));
  expect(await screen.findByRole('status')).toHaveTextContent(/Working\s*0s/);
  const box = screen.getByLabelText('Message');
  // Enter queues while running instead of blocking send.
  await userEvent.type(box, 'next{Enter}');
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /sessions/s1/queue')?.body,
    ).toEqual({ text: 'next', steer: false }),
  );
  // Cmd/Ctrl+Enter steers (opposite action).
  await userEvent.type(box, 'urgent');
  fireEvent.keyDown(box, { key: 'Enter', metaKey: true });
  await waitFor(() =>
    expect(
      calls.filter((c) => c.key === 'POST /sessions/s1/queue').at(-1)?.body,
    ).toEqual({ text: 'urgent', steer: true }),
  );
  expect(calls.some((c) => c.key === 'POST /sessions/s1/prompt')).toBe(false);
  await userEvent.click(screen.getByRole('button', { name: 'Stop' }));
  expect(calls.some((c) => c.key === 'POST /sessions/s1/cancel')).toBe(true);
});

it('sends on Enter', async () => {
  const { calls } = mockApi({ 'POST /sessions/s1/prompt': { status: 202 } });
  renderChat(true);
  await userEvent.type(screen.getByLabelText('Message'), 'hi{Enter}');
  expect(calls.find((c) => c.key === 'POST /sessions/s1/prompt')?.body).toEqual(
    { text: 'hi' },
  );
});

it('answers an unresolved permission request', async () => {
  const { calls } = mockApi({
    'POST /sessions/s1/permissions/r1': { status: 204 },
  });
  renderChat(true);
  act(() =>
    FakeSocket.all[0].emit(
      ev(1, 'permission', {
        requestId: 'r1',
        toolCall: { toolCallId: 't', title: 'Run ls' },
        options: [
          { optionId: 'allow', name: 'Allow once', kind: 'allow_once' },
        ],
        policy: 'ask',
      }),
    ),
  );
  await userEvent.click(
    await screen.findByRole('button', { name: 'Allow once' }),
  );
  expect(
    calls.find((c) => c.key === 'POST /sessions/s1/permissions/r1')?.body,
  ).toEqual({ optionId: 'allow' });
});

it('renders an inline secret request card and collapses it once resolved', async () => {
  const { calls } = mockApi({
    'POST /sessions/s1/secret-requests/sreq_1': {
      status: 'submitted',
      name: 'STRIPE_KEY',
    },
  });
  renderChat(true);
  const sock = FakeSocket.all[0];
  act(() => {
    sock.emit({ type: 'status', running: true });
    sock.emit(
      ev(1, 'secret_request', {
        requestId: 'sreq_1',
        name: 'STRIPE_KEY',
        description: 'For test charges',
        hosts: ['api.stripe.com'],
      }),
    );
  });
  const card = await screen.findByRole('group', { name: 'Secret request' });
  expect(card).toHaveTextContent('STRIPE_KEY');
  expect(screen.getByText(/Waiting for your secret/)).toBeVisible();
  await userEvent.type(
    within(card).getByLabelText('Value for STRIPE_KEY'),
    'sk_test_123',
  );
  await userEvent.click(
    within(card).getByRole('button', { name: 'Save securely' }),
  );
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /sessions/s1/secret-requests/sreq_1')
        ?.body,
    ).toEqual({ value: 'sk_test_123' }),
  );
  act(() =>
    sock.emit(
      ev(2, 'secret_request_resolved', {
        requestId: 'sreq_1',
        status: 'submitted',
        name: 'STRIPE_KEY',
        by: 'u1',
      }),
    ),
  );
  await waitFor(() =>
    expect(screen.getByTestId('chat-scroll')).toHaveTextContent(
      'Secret STRIPE_KEY saved securely',
    ),
  );
  expect(screen.queryByRole('group', { name: 'Secret request' })).toBeNull();
  expect(screen.getByTestId('chat-scroll')).not.toHaveTextContent(
    'sk_test_123',
  );
});

it('renders an inline account connect card that connects from the thread', async () => {
  const { calls } = mockApi({
    'GET /org/tools': [],
    'POST /me/org-tools/otl_1/connect': {
      toolId: 'otl_1',
      slug: 'linear',
      connected: true,
    },
  });
  renderChat(true);
  act(() =>
    FakeSocket.all[0].emit(
      ev(1, 'account_connect_request', {
        requestId: 'acreq_1',
        toolId: 'otl_1',
        slug: 'linear',
        name: 'Linear',
        kind: 'executor_app',
        authMode: 'user_oauth',
        reason: 'To read your issues',
        connected: false,
      }),
    ),
  );
  const card = await screen.findByRole('group', { name: 'Connect Linear' });
  expect(card).toHaveTextContent('To read your issues');
  await userEvent.click(
    within(card).getByRole('button', { name: 'Connect Linear' }),
  );
  expect(calls.some((c) => c.key === 'POST /me/org-tools/otl_1/connect')).toBe(
    true,
  );
  expect(await screen.findByText('Linear connected')).toBeVisible();
});

it('folds consecutive tool calls into one expandable work row', async () => {
  mockApi({});
  renderChat(true);
  const tool = (seq: number, id: string, title: string) =>
    ev(seq, 'update', {
      sessionUpdate: 'tool_call',
      toolCallId: id,
      title,
      kind: 'execute',
      status: 'completed',
    });
  act(() => {
    const s = FakeSocket.all[0];
    s.emit(ev(1, 'prompt', { text: 'go' }));
    s.emit(tool(2, 't1', 'Run ls'));
    s.emit(tool(3, 't2', 'Run make'));
    s.emit(
      ev(4, 'update', {
        sessionUpdate: 'agent_message_chunk',
        content: { type: 'text', text: 'All done' },
      }),
    );
  });
  const row = await screen.findByRole('button', { name: /Worked/ });
  expect(row).toHaveTextContent('2 tool calls');
  expect(row).toHaveAttribute('aria-expanded', 'false');
  expect(screen.queryByText('Run ls')).toBeNull();
  await userEvent.click(row);
  expect(screen.getByText('Run ls')).toBeVisible();
  expect(screen.getByText('Run make')).toBeVisible();
});

it('hides the composer for viewers', () => {
  mockApi({});
  renderChat(false);
  expect(screen.queryByLabelText('Message')).toBeNull();
  expect(screen.getByText(/read-only/)).toBeInTheDocument();
});

it('composer invites waking a sleeping agent', () => {
  mockApi({});
  renderChat(true, 'sleeping');
  expect(
    screen.getByPlaceholderText('Send a message to wake the agent…'),
  ).toBeEnabled();
});

it('shows presence avatars and marks you', async () => {
  mockApi({
    'GET /me': {
      user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
      personalWorkspaceId: 'w1',
    },
  });
  renderChat(true);
  act(() =>
    FakeSocket.all[0].emit({
      type: 'presence',
      viewers: [
        { id: 'u1', name: 'Alice', avatarUrl: '', role: 'admin' },
        { id: 'u2', name: 'Bob', avatarUrl: '', role: 'viewer' },
      ],
    }),
  );
  expect(await screen.findByTitle('Bob')).toBeInTheDocument();
  expect(await screen.findByTitle('Alice (you)')).toBeInTheDocument();
});

it('keeps the activity link out of the chat while a turn runs', async () => {
  mockApi({});
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ChatView
        sessionId="s1"
        onOpenActivity={vi.fn()}
        canUse
        agentState="sleeping"
      />
    </QueryClientProvider>,
  );
  act(() => {
    FakeSocket.all[0].emit(
      ev(1, 'prompt', { text: 'hi', user: { id: 'u', name: 'U' } }),
    );
    FakeSocket.all[0].emit({ type: 'status', running: true });
  });
  expect(await screen.findByRole('status')).toHaveTextContent('Waking');
  expect(screen.queryByRole('button', { name: /happening/ })).toBeNull();
});

it('offers the Activity tab after a failed turn, and nothing without one', async () => {
  mockApi({});
  const onOpenActivity = vi.fn();
  const { rerender } = render(
    <QueryClientProvider client={new QueryClient()}>
      <ChatView sessionId="s1" onOpenActivity={onOpenActivity} canUse />
    </QueryClientProvider>,
  );
  act(() => {
    FakeSocket.all[0].emit(ev(1, 'error', { message: 'boom' }));
  });
  await userEvent.click(
    await screen.findByRole('button', { name: 'Show what happened' }),
  );
  expect(onOpenActivity).toHaveBeenCalledOnce();
  rerender(
    <QueryClientProvider client={new QueryClient()}>
      <ChatView sessionId="s1" canUse />
    </QueryClientProvider>,
  );
  expect(
    screen.queryByRole('button', { name: 'Show what happened' }),
  ).toBeNull();
});

it('renders queued messages from live queue_updated frames with Send now, Edit, Remove, and Option+Up', async () => {
  let queueItems = [
    {
      id: 'q1',
      sessionId: 's1',
      userId: 'u1',
      userName: 'Alice',
      text: 'Run tests next',
      steer: false,
      createdAt: '2030-01-01T00:00:00Z',
    },
    {
      id: 'q2',
      sessionId: 's1',
      userId: 'u1',
      userName: 'Alice',
      text: 'Then update docs',
      steer: true,
      createdAt: '2030-01-01T00:00:01Z',
    },
    {
      id: 'q3',
      sessionId: 's1',
      userId: 'u1',
      userName: 'Alice',
      text: 'Finally clean up',
      steer: false,
      createdAt: '2030-01-01T00:00:02Z',
    },
  ];
  const { calls } = mockApi({
    'GET /sessions/s1/queue': () => ({ body: queueItems }),
    'POST /sessions/s1/queue/q1/send-now': () => {
      queueItems = queueItems.filter((m) => m.id !== 'q1');
      return { status: 202, body: { started: false, steered: true } };
    },
    'DELETE /sessions/s1/queue/q2': () => {
      queueItems = queueItems.filter((m) => m.id !== 'q2');
      return { status: 204 };
    },
    'DELETE /sessions/s1/queue/q3': () => {
      queueItems = queueItems.filter((m) => m.id !== 'q3');
      return { status: 204 };
    },
  });
  renderChat(true);
  act(() => {
    FakeSocket.all[0].emit({
      type: 'queue_updated',
      sessionId: 's1',
      count: queueItems.length,
      messages: queueItems,
    });
  });

  const stack = await screen.findByTestId('queued-messages');
  expect(stack).toHaveTextContent('Queued (3)');
  const items = within(stack).getAllByTestId('queued-message-item');
  expect(items).toHaveLength(3);
  expect(items[0]).toHaveTextContent('Run tests next');
  expect(items[1]).toHaveTextContent('Steer');

  await userEvent.click(
    within(items[0]).getByRole('button', { name: 'Send now' }),
  );
  await waitFor(() =>
    expect(
      calls.some((c) => c.key === 'POST /sessions/s1/queue/q1/send-now'),
    ).toBe(true),
  );

  // Remove q2 via Remove button
  await waitFor(() =>
    expect(
      within(screen.getByTestId('queued-messages')).getAllByTestId(
        'queued-message-item',
      ),
    ).toHaveLength(2),
  );
  await userEvent.click(
    within(
      within(screen.getByTestId('queued-messages')).getAllByTestId(
        'queued-message-item',
      )[0],
    ).getByRole('button', { name: 'Remove queued message' }),
  );
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'DELETE /sessions/s1/queue/q2')).toBe(
      true,
    ),
  );

  // Option+Up edits the latest remaining queued message (q3)
  await waitFor(() =>
    expect(
      within(screen.getByTestId('queued-messages')).getAllByTestId(
        'queued-message-item',
      ),
    ).toHaveLength(1),
  );
  fireEvent.keyDown(screen.getByLabelText('Message'), {
    key: 'ArrowUp',
    altKey: true,
  });
  expect(screen.getByLabelText('Message')).toHaveValue('Finally clean up');
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'DELETE /sessions/s1/queue/q3')).toBe(
      true,
    ),
  );
});

it('handles /side and /plan slash commands', async () => {
  const { calls } = mockApi({
    'GET /sessions/s1/queue': [],
    'POST /sessions/s1/side-chats': {
      status: 201,
      body: {
        id: 'side1',
        agentId: 'a1',
        title: 'Why does auth fail?',
        running: true,
        createdBy: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
        createdAt: '2030-01-01T00:00:00Z',
        updatedAt: '2030-01-01T00:00:00Z',
      },
    },
    'PATCH /sessions/s1/settings': { status: 200, body: {} },
  });
  renderChat(true, undefined, { agentId: 'a1' });
  const box = screen.getByLabelText('Message');

  await userEvent.type(box, '/btw Why does auth fail?{Enter}');
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /sessions/s1/side-chats')?.body,
    ).toEqual({
      text: 'Why does auth fail?',
      title: 'Why does auth fail?',
    }),
  );

  await userEvent.type(box, '/plan{Enter}');
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'PATCH /sessions/s1/settings')?.body,
    ).toEqual({ permissionMode: 'plan' }),
  );
});

it('supports @ file search, $ skills, and external composer context chips', async () => {
  const { calls } = mockApi({
    'GET /sessions/s1/queue': [],
    'GET /agents': [],
    'GET /harnesses': [],
    'GET /agents/a1/models': {
      harness: 'claude-code',
      source: 'harness',
      switchable: true,
      customModels: false,
      models: [],
    },
    'GET /agents/a1/files/search?q=app&limit=8': {
      base: '',
      truncated: false,
      results: [{ path: 'src/app.tsx', name: 'app.tsx', isDir: false }],
    },
    'GET /agents/a1/files/search?q=&limit=8': {
      base: '',
      truncated: false,
      results: [{ path: 'src/app.tsx', name: 'app.tsx', isDir: false }],
    },
    'GET /skills': [
      {
        id: 'sk1',
        ownerId: 'u1',
        name: 'review-diff',
        description: 'Review changes carefully',
        content: '',
        createdAt: '2030-01-01T00:00:00Z',
        updatedAt: '2030-01-01T00:00:00Z',
      },
    ],
    'GET /agents/a1/skills': [],
    'POST /sessions/s1/prompt': { status: 202 },
  });
  renderChat(true, undefined, { agentId: 'a1' });

  // External chip from DiffPane/FileViewer appears immediately
  act(() => {
    addComposerContext('s1', {
      kind: 'review_comment',
      path: 'src/main.ts',
      startLine: 10,
      comment: 'Handle null',
    });
  });
  expect(await screen.findByText('main.ts:10')).toBeVisible();

  const box = screen.getByLabelText('Message');
  await userEvent.type(box, '@app');
  const fileOpt = await screen.findByRole('option', { name: /src\/app\.tsx/ });
  await userEvent.click(fileOpt);
  expect(await screen.findByText('app.tsx')).toBeVisible();

  await userEvent.type(box, '$rev');
  const skillOpt = await screen.findByRole('option', {
    name: /\$review-diff/,
  });
  await userEvent.click(skillOpt);
  expect(box).toHaveValue('$review-diff ');

  await userEvent.type(box, 'please check{Enter}');
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'POST /sessions/s1/prompt')).toBe(true),
  );
  const sent = calls.find((c) => c.key === 'POST /sessions/s1/prompt')
    ?.body as { text: string };
  expect(sent.text).toContain('[Review comment on src/main.ts:10]');
  expect(sent.text).toContain('Handle null');
  expect(sent.text).toContain('[File: src/app.tsx]');
  expect(sent.text).toContain('$review-diff please check');
});

it('cites selected assistant text into the composer and rewinds from a user message', async () => {
  const { calls } = mockApi({
    'GET /sessions/s1/queue': [],
    'POST /sessions/s1/prompt': { status: 202 },
    'POST /sessions/s1/rewind': {
      status: 200,
      body: {
        seq: 1,
        promptText: 'Initial prompt',
        deletedEvents: 2,
        filesReverted: true,
      },
    },
  });
  const worktreeSession: Session = {
    id: 's1',
    agentId: 'a1',
    title: 'Thread',
    running: false,
    git: { project: 'repo', branch: 'feat/x' },
    createdBy: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
    createdAt: '2030-01-01T00:00:00Z',
    updatedAt: '2030-01-01T00:00:00Z',
  };
  renderChat(true, undefined, { agentId: 'a1', session: worktreeSession });
  act(() => {
    FakeSocket.all[0].emit(ev(1, 'prompt', { text: 'Initial prompt' }));
    FakeSocket.all[0].emit(
      ev(2, 'update', {
        sessionUpdate: 'agent_message_chunk',
        content: { type: 'text', text: 'Important assistant finding' },
      }),
    );
  });

  const assistantText = await screen.findByText('Important assistant finding');
  // Simulate selecting text inside the assistant message
  const fakeSel = {
    isCollapsed: false,
    anchorNode: assistantText,
    focusNode: assistantText,
    rangeCount: 0,
    toString: () => 'assistant finding',
    removeAllRanges: vi.fn(),
  };
  const origGetSel = window.getSelection;
  window.getSelection = (() =>
    fakeSel) as unknown as typeof window.getSelection;
  try {
    fireEvent.mouseUp(assistantText);
    const citeBtn = await screen.findByRole('button', {
      name: 'Cite in composer',
    });
    await userEvent.click(citeBtn);
  } finally {
    window.getSelection = origGetSel;
  }

  expect(await screen.findByTestId('composer-context-chip')).toHaveTextContent(
    'assistant finding',
  );

  // Rewind ("Edit from here") with "Revert files too"
  await userEvent.click(screen.getByRole('button', { name: 'Edit from here' }));
  const dialog = await screen.findByRole('dialog', {
    name: 'Rewind conversation',
  });
  expect(
    within(dialog).getByRole('button', { name: 'Revert conversation' }),
  ).toBeVisible();
  await userEvent.click(
    within(dialog).getByRole('button', { name: 'Revert files too' }),
  );
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /sessions/s1/rewind')?.body,
    ).toEqual({ seq: 1, revertFiles: true }),
  );
  // Prompt restored to composer and stream reconnected from after=0
  expect(screen.getByLabelText('Message')).toHaveValue('Initial prompt');
  expect(FakeSocket.all.length).toBeGreaterThanOrEqual(2);
  expect(FakeSocket.all[FakeSocket.all.length - 1].url).toMatch(/after=0$/);
});

it('uploads non-image file attachments to the agent workspace before sending', async () => {
  const uploaded: string[] = [];
  const origFetch = globalThis.fetch;
  stubGlobal(
    'fetch',
    vi.fn(async (input: Request | string | URL, init?: RequestInit) => {
      const url =
        typeof input === 'string'
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url;
      const method =
        init?.method ?? (input instanceof Request ? input.method : 'GET');
      if (method === 'PUT' && url.includes('/files/content')) {
        uploaded.push(url);
        return new Response(null, { status: 204 });
      }
      if (method === 'GET' && url.endsWith('/me')) {
        return new Response(
          JSON.stringify({
            user: {
              id: 'u1',
              email: 'a@x.io',
              name: 'Alice',
              avatarUrl: '',
            },
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        );
      }
      if (method === 'GET' && url.includes('/queue')) {
        return new Response('[]', {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      if (method === 'POST' && url.includes('/prompt')) {
        const body =
          input instanceof Request
            ? await input.text()
            : String(init?.body ?? '');
        uploaded.push(`PROMPT:${body}`);
        return new Response(null, { status: 202 });
      }
      return new Response('{}', {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }),
  );
  try {
    renderChat(true, undefined, { agentId: 'a1' });
    const logFile = new File(['error line 1'], 'build.log', {
      type: 'text/plain',
    });
    fireEvent.change(screen.getByLabelText('Choose files to attach'), {
      target: { files: [logFile] },
    });
    expect(await screen.findByText('build.log')).toBeVisible();
    await userEvent.type(screen.getByLabelText('Message'), 'check log{Enter}');
    await waitFor(() =>
      expect(uploaded.some((u) => u.startsWith('PROMPT:'))).toBe(true),
    );
    expect(
      uploaded.some(
        (u) =>
          u.includes('.agenthold%2Fattachments%2F') && u.includes('build.log'),
      ),
    ).toBe(true);
    const promptLine = uploaded.find((u) => u.startsWith('PROMPT:'))!;
    expect(promptLine).toContain('.agenthold/attachments/');
    expect(promptLine).toContain('check log');
  } finally {
    stubGlobal('fetch', origFetch);
  }
});

it('renders inline visual cards and interactive MCP Apps below tool calls', async () => {
  mockApi({});
  renderChat(true);
  const sock = FakeSocket.all[0];
  act(() => {
    sock.emit(
      ev(1, 'visual', {
        title: 'Build Duration',
        html: '<div>42s</div>',
        height: 220,
      }),
    );
    sock.emit(
      ev(2, 'update', {
        sessionUpdate: 'tool_call',
        toolCallId: 'tc_app',
        title: 'Open Issue Board',
        kind: 'other',
        status: 'completed',
        rawOutput: {
          _meta: {
            ui: {
              title: 'Issue Board',
              html: '<section>board</section>',
              server: 'linear',
            },
          },
        },
      }),
    );
  });

  expect(await screen.findByTestId('html-render-card')).toBeInTheDocument();
  expect(screen.getByTitle('Build Duration')).toHaveAttribute(
    'sandbox',
    'allow-scripts',
  );
  expect(await screen.findByTestId('mcp-app-card')).toBeInTheDocument();
  expect(screen.getByText('Open Issue Board')).toBeInTheDocument();
  expect(screen.getByText('linear')).toBeInTheDocument();
  expect(screen.getByTitle('Issue Board')).toHaveAttribute(
    'sandbox',
    'allow-scripts',
  );
});
