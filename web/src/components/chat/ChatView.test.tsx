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

const renderChat = (operator: boolean, agentState?: 'sleeping') =>
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ChatView sessionId="s1" operator={operator} agentState={agentState} />
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

it('shows working indicator and cancel while running', async () => {
  const { calls } = mockApi({ 'POST /sessions/s1/cancel': { status: 202 } });
  renderChat(true);
  act(() => FakeSocket.all[0].emit({ type: 'status', running: true }));
  expect(await screen.findByText('working...')).toBeInTheDocument();
  expect(screen.getByLabelText('Message')).toBeDisabled();
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
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
