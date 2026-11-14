import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import {
  agentPollInterval,
  agentsPollInterval,
  type Agent,
} from '@/api/queries';
import { AgentStateBadge } from '@/components/AgentStateBadge';
import { mockApi, renderApp } from '@/test-utils';

afterEach(() => vi.unstubAllGlobals());

const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
  personalWorkspaceId: 'w1',
};
const ws = {
  id: 'w1',
  name: 'Acme',
  personal: false,
  role: 'member',
  createdAt: '2030-01-01T00:00:00Z',
};
const agent = (over: Partial<Agent> = {}): Agent => ({
  id: 'a1',
  workspaceId: 'w1',
  name: 'builder',
  owner: me.user,
  image: 'ubuntu:24.04',
  harness: 'claude-code',
  model: '',
  permissionPolicy: 'ask',
  credentialsInSandbox: false,
  cpus: 2,
  memoryMb: 2048,
  idleMinutes: 15,
  state: 'sleeping',
  stateError: '',
  stateChangedAt: '2030-01-01T00:00:00Z',
  needsRebuild: false,
  role: 'operator',
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
  ...over,
});

it('renders agent list with state badges', async () => {
  mockApi({
    'GET /me': me,
    'GET /workspaces': [ws],
    'GET /workspaces/w1': ws,
    'GET /workspaces/w1/members': [],
    'GET /workspaces/w1/agents': [
      agent({ id: 'a1', name: 'one', state: 'running' }),
      agent({ id: 'a2', name: 'two', state: 'error', needsRebuild: true }),
    ],
  });
  renderApp('/w/w1');
  const one = (await screen.findByText('one')).closest('a')!;
  expect(within(one).getByText('running')).toHaveAttribute(
    'data-state',
    'running',
  );
  const two = screen.getByText('two').closest('a')!;
  expect(within(two).getByText('error')).toBeInTheDocument();
  expect(within(two).getByText('rebuild pending')).toBeInTheDocument();
});

it('shows Start for sleeping agent and hides admin controls for operator', async () => {
  mockApi({
    'GET /me': me,
    'GET /workspaces': [ws],
    'GET /agents/a1': agent(),
    'GET /agents/a1/sessions': [],
    'GET /harnesses': [],
  });
  renderApp('/a/a1');
  await userEvent.click(await screen.findByRole('tab', { name: 'overview' }));
  expect(await screen.findByRole('button', { name: 'Start' })).toBeVisible();
  expect(screen.getByRole('button', { name: 'Rebuild' })).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
});

it('polls fast while transient and slowly while running', () => {
  expect(agentPollInterval(agent({ state: 'starting' }))).toBe(1000);
  expect(agentPollInterval(agent({ state: 'destroying' }))).toBe(1000);
  expect(agentPollInterval(agent({ state: 'running' }))).toBe(10_000);
  expect(agentPollInterval(agent({ state: 'sleeping' }))).toBe(false);
  expect(agentPollInterval(undefined)).toBe(false);
  expect(
    agentsPollInterval([
      agent({ state: 'running' }),
      agent({ state: 'creating' }),
    ]),
  ).toBe(1000);
  expect(agentsPollInterval([agent({ state: 'sleeping' })])).toBe(false);
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
    'GET /workspaces': [ws],
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
