import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, it } from 'bun:test';
import { ThreadShareDialog } from './ThreadShareDialog';
import { mockApi } from '@/test-utils';

const alice = {
  id: 'u1',
  email: 'alice@example.com',
  name: 'Alice',
  avatarUrl: '',
};
const bob = {
  id: 'u2',
  email: 'bob@example.com',
  name: 'Bob',
  avatarUrl: '',
};

it('manages thread shares, creates/revokes a 24h guest link, and sends a handoff', async () => {
  let shares: {
    user: { id: string; email: string; name: string; avatarUrl: string };
    role: 'viewer' | 'collaborator';
    createdBy: string;
    createdAt: string;
  }[] = [
    {
      user: bob,
      role: 'viewer',
      createdBy: 'u1',
      createdAt: '2030-01-01T00:00:00Z',
    },
  ];
  let links: {
    id: string;
    sessionId: string;
    createdBy: string;
    expiresAt: string;
    createdAt: string;
    active: boolean;
  }[] = [];

  const { calls } = mockApi({
    'GET /me': { user: alice },
    'GET /sessions/s1/shares': () => ({ body: { owner: alice, shares } }),
    'POST /sessions/s1/shares': ({ body }: { body: unknown }) => {
      const b = body as { email: string; role: 'viewer' | 'collaborator' };
      const added = {
        user: { id: 'u3', email: b.email, name: 'Carol', avatarUrl: '' },
        role: b.role,
        createdBy: 'u1',
        createdAt: '2030-01-01T00:01:00Z',
      };
      shares = [...shares, added];
      return { body: added };
    },
    'DELETE /sessions/s1/shares/u2': () => {
      shares = shares.filter((s) => s.user.id !== 'u2');
      return { status: 204 };
    },
    'GET /sessions/s1/guest-links': () => ({ body: links }),
    'POST /sessions/s1/guest-links': () => {
      const link = {
        id: 'gl1',
        sessionId: 's1',
        createdBy: 'u1',
        expiresAt: '2030-01-02T00:00:00Z',
        createdAt: '2030-01-01T00:00:00Z',
        active: true,
      };
      links = [link];
      return {
        status: 201,
        body: {
          link,
          token: 'tok1234567890abcdef',
          url: '/guest/tok1234567890abcdef',
        },
      };
    },
    'DELETE /sessions/s1/guest-links/gl1': () => {
      links = [];
      return { status: 204 };
    },
    'POST /sessions/s1/handoff': () => ({
      body: {
        to: bob,
        message: 'Alice handed off to @Bob: Ready for your review',
      },
    }),
  });

  render(
    <QueryClientProvider client={new QueryClient()}>
      <ThreadShareDialog
        sessionId="s1"
        sessionTitle="Auth refactor"
        isOwner
        canPrompt
        open
        onOpenChange={() => {}}
      />
    </QueryClientProvider>,
  );

  // Existing share is rendered.
  expect(await screen.findByText('Bob')).toBeInTheDocument();

  // Add Carol as collaborator.
  await userEvent.type(
    screen.getByLabelText('Teammate email'),
    'carol@example.com',
  );
  await userEvent.selectOptions(
    screen.getByLabelText('Thread role'),
    'collaborator',
  );
  await userEvent.click(screen.getByRole('button', { name: 'Share' }));
  expect(await screen.findByText('Carol')).toBeInTheDocument();
  expect(
    calls.some(
      (c) =>
        c.key === 'POST /sessions/s1/shares' &&
        (c.body as { role?: string })?.role === 'collaborator',
    ),
  ).toBe(true);

  // Create guest link and see one-time URL, then revoke it.
  await userEvent.click(
    screen.getByRole('button', { name: 'Create guest link' }),
  );
  const urlInput = await screen.findByLabelText('Guest link URL');
  expect((urlInput as HTMLInputElement).value).toContain(
    '/guest/tok1234567890abcdef',
  );
  await userEvent.click(screen.getByRole('button', { name: 'Revoke' }));
  await waitFor(() =>
    expect(
      calls.some((c) => c.key === 'DELETE /sessions/s1/guest-links/gl1'),
    ).toBe(true),
  );

  // Send @teammate handoff.
  await userEvent.type(screen.getByLabelText('Hand off to'), '@Bob');
  await userEvent.type(
    screen.getByLabelText('Handoff note'),
    'Ready for your review',
  );
  await userEvent.click(screen.getByRole('button', { name: 'Hand off' }));
  expect(
    await screen.findByText('Alice handed off to @Bob: Ready for your review'),
  ).toBeInTheDocument();
});
