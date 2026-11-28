import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, it } from 'bun:test';
import { SecretRequestCard } from './SecretRequestCard';
import type { SecretRequestItem } from '@/lib/session';
import { mockApi } from '@/test-utils';

const item = (over: Partial<SecretRequestItem> = {}): SecretRequestItem => ({
  kind: 'secret_request',
  seq: 3,
  requestId: 'sreq_1',
  name: 'STRIPE_KEY',
  description: 'Needed to create a test charge',
  hosts: ['api.stripe.com'],
  envVar: 'STRIPE_API_KEY',
  ...over,
});

const renderCard = (it: SecretRequestItem, canUse = true) =>
  render(
    <QueryClientProvider client={new QueryClient()}>
      <SecretRequestCard item={it} sessionId="s1" canUse={canUse} />
    </QueryClientProvider>,
  );

const path = 'POST /sessions/s1/secret-requests/sreq_1';

it('shows the request details with a masked input', () => {
  mockApi({});
  renderCard(item());
  const card = screen.getByRole('group', { name: 'Secret request' });
  expect(card).toHaveTextContent('STRIPE_KEY');
  expect(card).toHaveTextContent('Needed to create a test charge');
  expect(within(card).getByText('api.stripe.com')).toBeVisible();
  expect(card).toHaveTextContent('STRIPE_API_KEY');
  const input = within(card).getByLabelText('Value for STRIPE_KEY');
  expect(input).toHaveAttribute('type', 'password');
  expect(input).toHaveAttribute('autocomplete', 'off');
  expect(
    within(card).getByRole('button', { name: 'Save securely' }),
  ).toBeDisabled();
});

it('saves the secret, then collapses without ever showing the value', async () => {
  const { calls } = mockApi({
    [path]: {
      status: 'submitted',
      secretId: 'sec_1',
      name: 'STRIPE_KEY',
      envVar: 'STRIPE_API_KEY',
    },
  });
  const { container } = renderCard(item());
  const user = userEvent.setup();
  await user.type(screen.getByLabelText('Value for STRIPE_KEY'), 'sk_live_x1');
  await user.click(screen.getByRole('button', { name: 'Save securely' }));
  await waitFor(() =>
    expect(calls.find((c) => c.key === path)?.body).toEqual({
      value: 'sk_live_x1',
    }),
  );
  const status = await screen.findByRole('status');
  expect(status).toHaveTextContent(
    'Secret STRIPE_KEY saved securely as STRIPE_API_KEY',
  );
  expect(screen.queryByRole('group', { name: 'Secret request' })).toBeNull();
  expect(container).not.toHaveTextContent('sk_live_x1');
  expect(container.querySelector('input')).toBeNull();
});

it('declines the request', async () => {
  const { calls } = mockApi({ [path]: { status: 'declined' } });
  renderCard(item());
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: 'Decline' }));
  await waitFor(() =>
    expect(calls.find((c) => c.key === path)?.body).toEqual({ decline: true }),
  );
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Secret request declined',
  );
});

it('clears the input and shows the error when saving fails', async () => {
  mockApi({
    [path]: () => ({
      status: 404,
      body: { error: 'unknown secret request' },
    }),
  });
  renderCard(item());
  const user = userEvent.setup();
  const input = screen.getByLabelText('Value for STRIPE_KEY');
  await user.type(input, 'sk_live_x1');
  await user.click(screen.getByRole('button', { name: 'Save securely' }));
  expect(await screen.findByRole('alert')).toBeVisible();
  expect(input).toHaveValue('');
});

it('renders an already resolved request as a quiet line', () => {
  mockApi({});
  renderCard(item({ resolved: { status: 'submitted', name: 'STRIPE_KEY' } }));
  expect(screen.getByRole('status')).toHaveTextContent(
    'Secret STRIPE_KEY saved securely',
  );
  expect(screen.queryByRole('textbox')).toBeNull();
});

it('read-only viewers see the request but cannot answer it', () => {
  mockApi({});
  renderCard(item(), false);
  const card = screen.getByRole('group', { name: 'Secret request' });
  expect(card).toHaveTextContent('Waiting for the thread owner');
  expect(within(card).queryByRole('button')).toBeNull();
  expect(card.querySelector('input')).toBeNull();
});
