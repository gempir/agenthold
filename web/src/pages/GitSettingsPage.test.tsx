import { stubGlobal } from '@/test-setup';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it } from 'bun:test';
import type { Agent, GitCredential, GitRepo, GitSettings } from '@/api/queries';
import { mockApi, renderApp } from '@/test-utils';

const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
};

const cred = (over: Partial<GitCredential> = {}): GitCredential => ({
  id: 'c1',
  host: 'github.com',
  username: 'x-access-token',
  label: 'work',
  source: 'token',
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-02T00:00:00Z',
  ...over,
});
const settings = (over: Partial<GitSettings> = {}): GitSettings => ({
  authorName: '',
  authorEmail: '',
  defaultAuthorName: 'Alice',
  defaultAuthorEmail: 'a@x.io',
  credentials: [],
  github: { enabled: false, host: '' },
  ...over,
});

it('shows the default identity as placeholders and saves a custom one', async () => {
  let current = settings();
  const { calls } = mockApi({
    'GET /me': me,
    'GET /me/git': () => ({ body: current }),
    'PUT /me/git': (r: { body: unknown }) => {
      const b = r.body as { authorName: string; authorEmail: string };
      current = settings(b);
      return { body: current };
    },
  });
  renderApp('/settings/git');
  const card = await screen.findByRole('region', { name: 'Author identity' });
  const name = within(card).getByLabelText('Author name');
  const email = within(card).getByLabelText('Author email');
  expect(name).toHaveAttribute('placeholder', 'Alice');
  expect(email).toHaveAttribute('placeholder', 'a@x.io');
  const user = userEvent.setup();
  await user.type(name, 'Ada Lovelace');
  await user.type(email, 'ada@x.io');
  await user.click(within(card).getByRole('button', { name: 'Save' }));
  await waitFor(() =>
    expect(calls.find((c) => c.key === 'PUT /me/git')?.body).toEqual({
      authorName: 'Ada Lovelace',
      authorEmail: 'ada@x.io',
    }),
  );
  expect(await within(card).findByText('Saved.')).toBeVisible();
});

it('adds a GitHub credential and removes a connected host', async () => {
  let creds: GitCredential[] = [
    cred({ id: 'c9', host: 'gitlab.com', username: 'oauth2', label: '' }),
  ];
  stubGlobal('confirm', () => true);
  const { calls } = mockApi({
    'GET /me': me,
    'GET /me/git': () => ({ body: settings({ credentials: creds }) }),
    'POST /me/git/credentials': (r: { body: unknown }) => {
      const b = r.body as { host: string };
      creds = [...creds, cred({ host: b.host })];
      return { body: creds[creds.length - 1] };
    },
    'DELETE /me/git/credentials/c9': () => {
      creds = creds.filter((c) => c.id !== 'c9');
      return { status: 204 };
    },
  });
  renderApp('/settings/git');
  const card = await screen.findByRole('region', { name: 'HTTPS credentials' });
  const list = await within(card).findByRole('list', {
    name: 'Connected hosts',
  });
  expect(within(list).getByText('gitlab.com')).toBeVisible();
  expect(within(list).getByText('oauth2')).toBeVisible();

  const user = userEvent.setup();
  await user.click(within(card).getByRole('button', { name: 'GitHub' }));
  expect(within(card).getByLabelText('Host')).toHaveValue('github.com');
  await user.type(within(card).getByLabelText('Token'), 'ghp_secret');
  await user.type(within(card).getByLabelText('Label (optional)'), 'work');
  await user.click(
    within(card).getByRole('button', { name: 'Save credential' }),
  );
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /me/git/credentials')?.body,
    ).toEqual({ host: 'github.com', token: 'ghp_secret', label: 'work' }),
  );
  expect(await within(list).findByText('github.com')).toBeVisible();
  // The token is not kept around in the form.
  expect(within(card).getByLabelText('Token')).toHaveValue('');

  await user.click(
    within(list).getByRole('button', { name: 'Remove gitlab.com' }),
  );
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'DELETE /me/git/credentials/c9')).toBe(
      true,
    ),
  );
  await waitFor(() =>
    expect(within(list).queryByText('gitlab.com')).toBeNull(),
  );
});

const agent = (over: Partial<Agent> = {}): Agent => ({
  id: 'a1',
  name: 'builder',
  owner: me.user,
  harness: 'claude-code',
  model: '',
  modelOptions: {},
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
  isTemplate: false,
  role: 'owner',
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
  ...over,
});
const repo = (over: Partial<GitRepo> = {}): GitRepo => ({
  path: 'projects/web',
  project: 'web',
  branch: 'main',
  detached: false,
  dirty: false,
  ahead: 0,
  behind: 0,
  isWorktree: false,
  ...over,
});

it('the Git tab lists repositories and worktrees with their state', async () => {
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents/a1': agent(),
    'GET /agents/a1/git/repos': {
      connected: true,
      repos: [
        repo({ dirty: true, ahead: 2, behind: 1 }),
        repo({
          path: 'projects/web-feat',
          project: 'web',
          branch: 'feat/x',
          isWorktree: true,
        }),
      ],
    },
    'POST /agents/a1/git/pull': repo(),
  });
  renderApp('/a/a1/settings?tab=git');
  const main = await screen.findByRole('main');
  const first = await within(main).findByRole('article', {
    name: 'projects/web',
  });
  expect(within(first).getByText('main')).toBeVisible();
  expect(within(first).getByText('dirty')).toBeVisible();
  expect(within(first).getByText('↑2')).toBeVisible();
  expect(within(first).getByText('↓1')).toBeVisible();
  expect(within(first).queryByText('worktree')).toBeNull();
  expect(
    within(first).getByRole('link', { name: 'New thread' }),
  ).toHaveAttribute('href', '/a/a1?cwd=projects%2Fweb');
  const second = within(main).getByRole('article', {
    name: 'projects/web-feat',
  });
  expect(within(second).getByText('worktree')).toBeVisible();
  expect(within(second).getByText('feat/x')).toBeVisible();

  await userEvent.click(within(first).getByRole('button', { name: 'Pull' }));
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /agents/a1/git/pull')?.body,
    ).toEqual({ path: 'projects/web' }),
  );
});

it('the Git tab offers Start while the agent sleeps', async () => {
  mockApi({
    'GET /me': me,
    'GET /agents/a1': agent({ state: 'sleeping' }),
    'GET /agents/a1/git/repos': { connected: false, repos: [] },
  });
  renderApp('/a/a1/settings?tab=git');
  expect(await screen.findByRole('button', { name: 'Start' })).toBeVisible();
  expect(screen.queryByRole('form', { name: 'Clone repository' })).toBeNull();
});

it('the Git tab clones a repository and links to the credentials', async () => {
  let repos: GitRepo[] = [];
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents/a1': agent(),
    'GET /agents/a1/git/repos': () => ({ body: { connected: true, repos } }),
    'POST /agents/a1/git/clone': () => {
      repos = [repo({ path: 'projects/lib', project: 'lib' })];
      return { status: 201, body: repos[0] };
    },
  });
  renderApp('/a/a1/settings?tab=git');
  const form = await screen.findByRole('form', { name: 'Clone repository' });
  expect(
    within(form).getByRole('link', {
      name: 'connect GitHub or add an HTTPS token',
    }),
  ).toHaveAttribute('href', '/settings/git');
  const user = userEvent.setup();
  await user.type(
    within(form).getByLabelText('Repository URL'),
    'https://github.com/o/lib.git',
  );
  await user.type(
    within(form).getByLabelText('Folder (optional)'),
    'projects/lib',
  );
  await user.click(within(form).getByRole('button', { name: 'Clone' }));
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /agents/a1/git/clone')?.body,
    ).toEqual({ url: 'https://github.com/o/lib.git', path: 'projects/lib' }),
  );
  expect(
    await screen.findByRole('article', { name: 'projects/lib' }),
  ).toBeVisible();
});

it('the Git tab switches branches and creates a worktree', async () => {
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents/a1': agent(),
    'GET /agents/a1/git/repos': { connected: true, repos: [repo()] },
    'GET /agents/a1/git/status?path=projects%2Fweb': {
      repo: repo(),
      branches: ['main', 'dev'],
    },
    'POST /agents/a1/git/checkout': repo({ branch: 'dev' }),
    'POST /agents/a1/git/worktrees': {
      status: 201,
      body: repo({
        path: 'projects/web-feat',
        branch: 'feat/x',
        isWorktree: true,
      }),
    },
  });
  renderApp('/a/a1/settings?tab=git');
  const card = await screen.findByRole('article', { name: 'projects/web' });
  const user = userEvent.setup();

  await user.click(within(card).getByRole('button', { name: 'Branches' }));
  await user.click(
    await within(card).findByRole('button', { name: 'Switch to dev' }),
  );
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /agents/a1/git/checkout')?.body,
    ).toEqual({ path: 'projects/web', branch: 'dev' }),
  );
  await user.click(within(card).getByRole('button', { name: 'Branches' }));

  await user.click(within(card).getByRole('button', { name: 'New worktree' }));
  const form = within(card).getByRole('form', { name: 'New worktree' });
  await user.type(within(form).getByLabelText('Worktree branch'), 'feat/x');
  await user.type(within(form).getByLabelText('Base ref (optional)'), 'main');
  await user.click(
    within(form).getByRole('button', { name: 'Create worktree' }),
  );
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /agents/a1/git/worktrees')?.body,
    ).toEqual({
      path: 'projects/web',
      branch: 'feat/x',
      create: true,
      fromRef: 'main',
    }),
  );
});

it('hides Connect GitHub when the server has no GitHub app', async () => {
  mockApi({ 'GET /me': me, 'GET /me/git': settings() });
  renderApp('/settings/git');
  await screen.findByRole('region', { name: 'Author identity' });
  expect(screen.queryByRole('region', { name: 'GitHub' })).toBeNull();
});

it('connects GitHub with the device flow and disconnects it', async () => {
  let creds: GitCredential[] = [];
  let polls = 0;
  let approved = false;
  stubGlobal('confirm', () => true);
  const { calls } = mockApi({
    'GET /me': me,
    'GET /me/git': () => ({
      body: settings({
        credentials: creds,
        github: { enabled: true, host: 'github.com' },
      }),
    }),
    'POST /me/git/github/device': () => ({
      status: 201,
      body: {
        id: 'ghf_1',
        userCode: 'WXYZ-0001',
        verificationUri: 'https://github.com/login/device',
        expiresAt: '2030-01-01T00:15:00Z',
        interval: 0,
      },
    }),
    'POST /me/git/github/device/ghf_1': () => {
      polls++;
      if (!approved) return { body: { status: 'pending' } };
      creds = [
        cred({
          id: 'gh1',
          source: 'github',
          account: 'ada-gh',
          label: '@ada-gh',
          scopes: 'repo',
        }),
      ];
      return { body: { status: 'connected', credential: creds[0] } };
    },
    'DELETE /me/git/credentials/gh1': () => {
      creds = [];
      return { status: 204 };
    },
  });
  renderApp('/settings/git');
  const card = await screen.findByRole('region', { name: 'GitHub' });
  const user = userEvent.setup();
  await user.click(
    within(card).getByRole('button', { name: 'Connect GitHub' }),
  );
  expect(await within(card).findByText('WXYZ-0001')).toBeVisible();
  expect(
    within(card).getByRole('link', { name: 'Open GitHub' }),
  ).toHaveAttribute('href', 'https://github.com/login/device');
  await waitFor(() => expect(polls).toBeGreaterThan(0));
  approved = true;
  expect(await within(card).findByText('ada-gh')).toBeVisible();
  expect(within(card).queryByText('WXYZ-0001')).toBeNull();
  expect(
    within(card).getByRole('button', { name: 'Reconnect GitHub' }),
  ).toBeVisible();

  await user.click(within(card).getByRole('button', { name: 'Disconnect' }));
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'DELETE /me/git/credentials/gh1')).toBe(
      true,
    ),
  );
  expect(
    await within(card).findByRole('button', { name: 'Connect GitHub' }),
  ).toBeVisible();
});

it('the Git tab pushes a branch and removes a worktree', async () => {
  stubGlobal('confirm', () => true);
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents/a1': agent(),
    'GET /agents/a1/git/repos': {
      connected: true,
      repos: [
        repo({ remoteUrl: 'https://github.com/o/web.git', ahead: 1 }),
        repo({
          path: '.worktrees/web/feat-x',
          branch: 'feat/x',
          isWorktree: true,
          dirty: true,
          mainPath: 'projects/web',
          remoteUrl: 'https://github.com/o/web.git',
        }),
      ],
    },
    'POST /agents/a1/git/push': repo(),
    'POST /agents/a1/git/worktrees/remove': repo(),
  });
  renderApp('/a/a1/settings?tab=git');
  const main = await screen.findByRole('article', { name: 'projects/web' });
  expect(
    within(main).queryByRole('button', { name: /Remove worktree/ }),
  ).toBeNull();
  const user = userEvent.setup();
  await user.click(within(main).getByRole('button', { name: 'Push' }));
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /agents/a1/git/push')?.body,
    ).toEqual({ path: 'projects/web' }),
  );
  const wt = screen.getByRole('article', { name: '.worktrees/web/feat-x' });
  await user.click(
    within(wt).getByRole('button', {
      name: 'Remove worktree .worktrees/web/feat-x',
    }),
  );
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /agents/a1/git/worktrees/remove')?.body,
    ).toEqual({ path: '.worktrees/web/feat-x', force: true }),
  );
});
