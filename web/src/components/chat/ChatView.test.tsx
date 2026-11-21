import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import ChatView from './ChatView';
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
  vi.stubGlobal('WebSocket', FakeSocket);
});
afterEach(() => vi.unstubAllGlobals());

const renderChat = (canUse: boolean, agentState?: 'sleeping') =>
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ChatView sessionId="s1" canUse={canUse} agentState={agentState} />
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

it('shows working indicator and stop while running', async () => {
  const { calls } = mockApi({ 'POST /sessions/s1/cancel': { status: 202 } });
  renderChat(true);
  act(() => FakeSocket.all[0].emit({ type: 'status', running: true }));
  expect(await screen.findByRole('status')).toHaveTextContent(/Working\s*0s/);
  // The next message can be drafted but not sent while the agent works.
  await userEvent.type(screen.getByLabelText('Message'), 'next{Enter}');
  expect(screen.getByLabelText('Message')).toHaveValue('next');
  expect(calls.some((c) => c.key === 'POST /sessions/s1/prompt')).toBe(false);
  expect(screen.queryByRole('button', { name: 'Send' })).toBeNull();
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

it('links to the Activity tab while a turn waits for its first answer', async () => {
  mockApi({});
  const onOpenActivity = vi.fn();
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ChatView
        sessionId="s1"
        onOpenActivity={onOpenActivity}
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
  // No inline log any more: a small link that opens the tab.
  await userEvent.click(
    await screen.findByRole('button', { name: /What is happening/ }),
  );
  expect(onOpenActivity).toHaveBeenCalledOnce();
  expect(screen.queryByTestId('live-log')).toBeNull();
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
