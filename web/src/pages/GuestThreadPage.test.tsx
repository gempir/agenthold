import { stubGlobal } from '@/test-setup';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it } from 'bun:test';
import { GuestThreadPage } from './GuestThreadPage';
import { mockApi } from '@/test-utils';

class FakeGuestSocket {
  static all: FakeGuestSocket[] = [];
  onopen?: () => void;
  onmessage?: (e: { data: string }) => void;
  onclose?: (e: { code: number }) => void;
  constructor(public url: string) {
    FakeGuestSocket.all.push(this);
  }
  close() {}
  emit(o: unknown) {
    this.onmessage?.({ data: JSON.stringify(o) });
  }
}

beforeEach(() => {
  FakeGuestSocket.all = [];
  stubGlobal('WebSocket', FakeGuestSocket);
});

it('renders live read-only guest thread and queues a message request for approval', async () => {
  const { calls } = mockApi({
    'GET /guest/tok-live': {
      sessionId: 's1',
      title: 'Production triage',
      agentName: 'ops-agent',
      running: false,
      expiresAt: '2030-01-02T00:00:00Z',
      events: [
        {
          seq: 1,
          at: '2030-01-01T00:00:00Z',
          type: 'prompt',
          data: {
            text: 'Investigate latency',
            user: { id: 'u1', name: 'Alice' },
          },
        },
      ],
      pendingRequests: [],
    },
    'POST /guest/tok-live/requests': ({ body }: { body: unknown }) => {
      const b = body as { name?: string; text: string };
      return {
        status: 201,
        body: {
          id: 'greq1',
          sessionId: 's1',
          guestLinkId: 'gl1',
          guestName: b.name || 'Guest',
          text: b.text,
          status: 'pending',
          createdAt: '2030-01-01T00:05:00Z',
        },
      };
    },
  });

  render(<GuestThreadPage token="tok-live" />);

  expect(await screen.findByText('Production triage')).toBeInTheDocument();
  expect(screen.getByText('Investigate latency')).toBeInTheDocument();
  expect(FakeGuestSocket.all).toHaveLength(1);
  expect(FakeGuestSocket.all[0].url).toMatch(
    /\/api\/v1\/guest\/tok-live\/stream\?after=1$/,
  );

  // Live agent update streams in over WebSocket.
  act(() => {
    FakeGuestSocket.all[0].onopen?.();
    FakeGuestSocket.all[0].emit({
      seq: 2,
      at: '2030-01-01T00:01:00Z',
      type: 'update',
      data: {
        sessionUpdate: 'agent_message_chunk',
        content: { type: 'text', text: 'Found a slow query in billing.' },
      },
    });
  });
  expect(
    await screen.findByText('Found a slow query in billing.'),
  ).toBeInTheDocument();

  // Guest submits a message request.
  await userEvent.type(screen.getByLabelText('Your name'), 'Sam');
  await userEvent.type(
    screen.getByLabelText('Message to request'),
    'Can you check the index?',
  );
  await userEvent.click(
    screen.getByRole('button', { name: 'Request message' }),
  );

  expect(
    await screen.findByText('Message queued for host approval.'),
  ).toBeInTheDocument();
  expect(screen.getByText('Can you check the index?')).toBeInTheDocument();
  expect(calls.some((c) => c.key === 'POST /guest/tok-live/requests')).toBe(
    true,
  );
});

it('shows unavailable state when guest link is invalid or revoked', async () => {
  mockApi({
    'GET /guest/bad-token': () => ({
      status: 404,
      body: { error: 'not found' },
    }),
  });
  render(<GuestThreadPage token="bad-token" />);
  expect(await screen.findByText('Guest link unavailable')).toBeInTheDocument();
});
