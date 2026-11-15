import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import type { Agent } from '@/api/queries';
import { mockApi, renderApp } from '@/test-utils';

afterEach(() => vi.unstubAllGlobals());

const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
};
const agent = (role: Agent['role']): Agent => ({
  id: 'a1',
  name: 'builder',
  owner: me.user,
  image: 'ubuntu:24.04',
  harness: 'claude-code',
  model: '',
  permissionPolicy: 'ask',
  credentialsInSandbox: false,
  login: { source: 'sandbox', connected: false, mine: false },
  cpus: 2,
  memoryMb: 2048,
  idleMinutes: 15,
  state: 'running',
  stateError: '',
  stateChangedAt: '2030-01-01T00:00:00Z',
  needsRebuild: false,
  role,
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
});
const entry = (name: string, isDir: boolean) => ({
  name,
  size: 2048,
  mode: '0644',
  isDir,
  isLink: false,
  modTime: '2030-01-01T00:00:00Z',
});

function setup(role: Agent['role']) {
  return mockApi({
    'GET /me': me,
    'GET /agents/a1': agent(role),
    'GET /agents/a1/files': () => ({
      body: {
        path: '/home/agent',
        entries: [entry('notes.txt', false), entry('src', true)],
      },
    }),
  });
}

it('lists entries and navigates into a directory in the agent panel', async () => {
  const { calls } = setup('shared');
  renderApp('/a/a1?panel=files');
  expect(await screen.findByRole('tab', { name: 'Files' })).toHaveAttribute(
    'aria-selected',
    'true',
  );
  expect(await screen.findByText('notes.txt')).toBeVisible();
  expect(screen.getByText('2.0 KB')).toBeVisible();
  await userEvent.click(screen.getByRole('button', { name: 'src' }));
  await vi.waitFor(() =>
    expect(calls.filter((c) => c.key === 'GET /agents/a1/files').length).toBe(
      2,
    ),
  );
});

it('shows write controls and the terminal to shared users', async () => {
  setup('shared');
  renderApp('/a/a1/settings?tab=files');
  await screen.findByText('notes.txt');
  expect(screen.getByRole('button', { name: 'Upload' })).toBeVisible();
  expect(
    screen.getByRole('button', { name: 'Delete notes.txt' }),
  ).toBeVisible();
  const nav = screen.getByRole('navigation', { name: 'Agent settings' });
  expect(within(nav).getByRole('link', { name: 'Terminal' })).toBeVisible();
  expect(within(nav).queryByRole('link', { name: 'Activity' })).toBeNull();
});
