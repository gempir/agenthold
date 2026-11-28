import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it } from 'bun:test';
import { mockApi, renderApp } from '@/test-utils';

const alice = {
  id: 'u1',
  name: 'Alice',
  email: 'alice@example.com',
  avatarUrl: '',
};

const baseSummary = {
  range: '7d',
  scope: 'me',
  totals: {
    totalTokens: 1_500_000,
    inputTokens: 1_000_000,
    outputTokens: 300_000,
    cacheReadTokens: 200_000,
    cacheWriteTokens: 0,
    totalCostUsd: 7.56,
    turnCount: 12,
  },
  series: [
    {
      bucket: '2026-11-18',
      totalTokens: 500_000,
      inputTokens: 300_000,
      outputTokens: 100_000,
      cacheReadTokens: 100_000,
      cacheWriteTokens: 0,
      costUsd: 2.5,
      turnCount: 4,
    },
    {
      bucket: '2026-11-19',
      totalTokens: 1_000_000,
      inputTokens: 700_000,
      outputTokens: 200_000,
      cacheReadTokens: 100_000,
      cacheWriteTokens: 0,
      costUsd: 5.06,
      turnCount: 8,
    },
  ],
  byModel: [
    {
      harness: 'claude-code',
      model: 'claude-sonnet-4-6',
      totalTokens: 1_500_000,
      inputTokens: 1_000_000,
      outputTokens: 300_000,
      cacheReadTokens: 200_000,
      cacheWriteTokens: 0,
      costUsd: 7.56,
      turnCount: 12,
      priced: true,
    },
  ],
  byUser: [],
};

const orgSummary = {
  ...baseSummary,
  scope: 'org',
  totals: {
    ...baseSummary.totals,
    totalCostUsd: 12.34,
    turnCount: 20,
  },
  byUser: [
    {
      userId: 'u1',
      userName: 'Alice',
      userEmail: 'alice@example.com',
      totalTokens: 1_500_000,
      inputTokens: 1_000_000,
      outputTokens: 300_000,
      costUsd: 7.56,
      turnCount: 12,
    },
    {
      userId: 'u2',
      userName: 'Bob',
      userEmail: 'bob@example.com',
      totalTokens: 800_000,
      inputTokens: 600_000,
      outputTokens: 200_000,
      costUsd: 4.78,
      turnCount: 8,
    },
  ],
};

it('renders personal usage, switches ranges and toggles Cost/Tokens for a regular user', async () => {
  const { calls } = mockApi({
    'GET /me': { admin: false, user: alice },
    'GET /agents': [],
    'GET /apps': [],
    'GET /usage/summary?range=7d&scope=me': baseSummary,
    'GET /usage/summary?range=30d&scope=me': { ...baseSummary, range: '30d' },
  });

  renderApp('/usage');

  expect(await screen.findByTestId('usage-total-cost')).toHaveTextContent(
    '$7.56',
  );
  expect(screen.getByTestId('usage-total-tokens')).toHaveTextContent('1.50M');
  expect(screen.getByText('claude-sonnet-4-6')).toBeVisible();
  // Non-admin does not see Organization scope or Model prices button.
  expect(screen.queryByRole('button', { name: 'Organization' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Model prices' })).toBeNull();

  // Switch metric from Cost to Tokens.
  await userEvent.click(screen.getByRole('button', { name: 'Tokens' }));
  expect(screen.getByText('Token volume over time')).toBeVisible();

  // Switch time range to 30d.
  await userEvent.click(screen.getByRole('button', { name: '30d' }));
  await waitFor(() =>
    expect(
      calls.some((c) => c.key === 'GET /usage/summary?range=30d&scope=me'),
    ).toBe(true),
  );
});

it('shows org scope breakdown and custom pricing editor for admins', async () => {
  let prices = [
    {
      model: 'claude-sonnet-4-6',
      inputPerM: 3,
      outputPerM: 15,
      cacheReadPerM: 0.3,
      custom: false,
    },
  ];
  const { calls } = mockApi({
    'GET /me': { admin: true, user: alice },
    'GET /agents': [],
    'GET /apps': [],
    'GET /usage/summary?range=7d&scope=me': baseSummary,
    'GET /usage/summary?range=7d&scope=org': orgSummary,
    'GET /usage/prices': () => ({ body: prices }),
    'PUT /usage/prices': ({ body }: { body: unknown }) => {
      const b = body as {
        model: string;
        inputPerM: number;
        outputPerM: number;
        cacheReadPerM?: number;
      };
      const updated = {
        model: b.model,
        inputPerM: b.inputPerM,
        outputPerM: b.outputPerM,
        cacheReadPerM: b.cacheReadPerM ?? 0,
        custom: true,
      };
      prices = [...prices.filter((p) => p.model !== b.model), updated];
      return { body: updated };
    },
  });

  renderApp('/usage');
  await screen.findByTestId('usage-total-cost');

  // Switch to Organization scope and verify Users breakdown table.
  await userEvent.click(screen.getByRole('button', { name: 'Organization' }));
  const usersSection = await screen.findByRole('region', {
    name: 'Usage by user',
  });
  expect(within(usersSection).getByText('Bob')).toBeVisible();
  expect(within(usersSection).getByText('$4.78')).toBeVisible();

  // Open custom pricing editor and save a custom price.
  await userEvent.click(screen.getByRole('button', { name: 'Model prices' }));
  const pricingSection = await screen.findByRole('region', {
    name: 'Custom model pricing',
  });
  await userEvent.type(
    within(pricingSection).getByLabelText('Model ID'),
    'custom-model-1',
  );
  await userEvent.click(
    within(pricingSection).getByRole('button', { name: 'Save price' }),
  );
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'PUT /usage/prices')).toBe(true),
  );
  expect(
    await within(pricingSection).findByText('custom-model-1'),
  ).toBeVisible();
});
