import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it } from 'bun:test';
import type {
  Agent,
  PullRequestDetail,
  PullRequestSummary,
} from '@/api/queries';
import { mockApi, renderApp } from '@/test-utils';

const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
};

const summary = (over: Partial<PullRequestSummary>): PullRequestSummary => ({
  host: 'github.com',
  repo: 'o/web',
  number: 1,
  url: 'https://github.com/o/web/pull/1',
  title: 'Add login',
  state: 'open',
  draft: false,
  author: 'alice',
  headRef: 'login',
  headSha: 'abc123',
  baseRef: 'main',
  comments: 2,
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-02T00:00:00Z',
  ...over,
});

const detail: PullRequestDetail = {
  pullRequest: summary({}),
  body: 'Adds the **login** page.',
  conflicted: false,
  additions: 4,
  deletions: 1,
  changedFiles: 1,
  checks: [
    { name: 'build', status: 'completed', conclusion: 'success' },
    { name: 'test', status: 'completed', conclusion: 'failure' },
  ],
  checksState: 'failure',
  comments: [
    {
      id: 7,
      kind: 'review',
      author: 'bob',
      body: 'Please add tests',
      state: 'CHANGES_REQUESTED',
      createdAt: '2030-01-02T00:00:00Z',
    },
  ],
  files: [
    {
      path: 'src/login.ts',
      status: 'A',
      additions: 4,
      deletions: 1,
      patch: '@@ -1,1 +1,2 @@\n-old line\n+new line\n+second line',
    },
  ],
  viewer: 'alice',
};

const agent: Agent = {
  id: 'a1',
  name: 'builder',
  owner: me.user,
  harness: 'claude-code',
  model: '',
  modelOptions: {},
  permissionPolicy: 'ask',
  executorEnabled: true,
  executorApprovalMode: 'model',
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
};

function routes(extra: Record<string, unknown> = {}) {
  return mockApi({
    'GET /me': me,
    'GET /agents': [agent],
    'GET /apps': [],
    'GET /projects': [],
    'GET /pull-requests?state=open&filter=involved': {
      hosts: ['github.com'],
      errors: [],
      pullRequests: [
        summary({}),
        summary({
          number: 2,
          title: 'Fix typo',
          author: 'carol',
          headSha: 'def',
        }),
      ],
    },
    'GET /pull-requests?state=merged&filter=involved': {
      hosts: ['github.com'],
      errors: [],
      pullRequests: [summary({ number: 3, title: 'Shipped', state: 'merged' })],
    },
    'GET /pull-requests/o/web/1?host=github.com': detail,
    ...extra,
  });
}

it('lists, filters and searches pull requests', async () => {
  routes();
  renderApp('/pulls');
  const list = await screen.findByRole('list', { name: 'Pull requests' });
  expect(await within(list).findByText('Add login')).toBeVisible();
  expect(within(list).getByText('Fix typo')).toBeVisible();

  await userEvent.type(
    screen.getByRole('textbox', { name: 'Search pull requests' }),
    'carol',
  );
  expect(within(list).queryByText('Add login')).toBeNull();
  expect(within(list).getByText('Fix typo')).toBeVisible();
  await userEvent.clear(
    screen.getByRole('textbox', { name: 'Search pull requests' }),
  );

  await userEvent.click(screen.getByRole('tab', { name: 'Merged' }));
  expect(await within(list).findByText('Shipped')).toBeVisible();
  expect(within(list).queryByText('Add login')).toBeNull();
});

it('tells the user to connect an account without credentials', async () => {
  mockApi({
    'GET /me': me,
    'GET /agents': [],
    'GET /apps': [],
    'GET /projects': [],
    'GET /pull-requests?state=open&filter=involved': {
      hosts: [],
      errors: [],
      pullRequests: [],
    },
  });
  renderApp('/pulls');
  expect(
    await screen.findByText('Connect a git account to see your pull requests.'),
  ).toBeVisible();
  expect(screen.getByRole('link', { name: 'Settings → Git' })).toHaveAttribute(
    'href',
    '/settings/git',
  );
});

it('shows a pull request, comments on it and merges its reviewed head', async () => {
  const { calls } = routes({
    'POST /pull-requests/o/web/1/comments?host=github.com': () => ({
      status: 204,
    }),
    'POST /pull-requests/o/web/1/merge?host=github.com': {
      merged: true,
      sha: 'm1',
    },
  });
  const { router } = renderApp('/pulls');
  const list = await screen.findByRole('list', { name: 'Pull requests' });
  await userEvent.click(await within(list).findByText('Add login'));
  await waitFor(() =>
    expect(router.state.location.search).toEqual({ pr: 'github.com/o/web#1' }),
  );
  const view = await screen.findByTestId('pull-request');
  expect(
    await within(view).findByRole('heading', { name: /Add login/ }),
  ).toBeVisible();
  expect(
    await within(view).findByText(/login/, { selector: 'strong' }),
  ).toBeVisible();

  await userEvent.click(within(view).getByRole('tab', { name: /Checks/ }));
  const checks = within(view).getByRole('list', { name: 'Checks' });
  expect(within(checks).getByText('test').closest('li')).toHaveTextContent(
    'failure',
  );

  await userEvent.click(within(view).getByRole('tab', { name: /Files/ }));
  const file = within(view).getByTestId('pr-file');
  expect(file).toHaveTextContent('src/login.ts');
  expect(file).toHaveTextContent('+new line');
  expect(file).toHaveTextContent('-old line');

  await userEvent.click(within(view).getByRole('tab', { name: /Comments/ }));
  expect(within(view).getByText('Please add tests')).toBeVisible();
  await userEvent.type(
    within(view).getByRole('textbox', { name: 'New comment' }),
    'Added them',
  );
  await userEvent.click(within(view).getByRole('button', { name: 'Comment' }));
  await waitFor(() =>
    expect(
      calls.find(
        (c) => c.key === 'POST /pull-requests/o/web/1/comments?host=github.com',
      )?.body,
    ).toEqual({ body: 'Added them', event: 'comment' }),
  );

  await userEvent.selectOptions(
    within(view).getByRole('combobox', { name: 'Merge method' }),
    'rebase',
  );
  await userEvent.click(within(view).getByRole('button', { name: 'Merge' }));
  await waitFor(() =>
    expect(
      calls.find(
        (c) => c.key === 'POST /pull-requests/o/web/1/merge?host=github.com',
      )?.body,
    ).toEqual({ method: 'rebase', headSha: 'abc123' }),
  );
  expect(await within(view).findByText('Merged.')).toBeVisible();
});

it('starts a thread on the pull request with Work on PR', async () => {
  const { calls } = routes({
    'POST /pull-requests/o/web/1/work?host=github.com': () => ({
      status: 201,
      body: {
        session: {
          id: 's9',
          agentId: 'a1',
          title: 'PR #1: Add login',
          running: false,
          createdBy: me.user,
          createdAt: '2030-01-01T00:00:00Z',
          updatedAt: '2030-01-01T00:00:00Z',
        },
        pullRequest: {},
        worktree: '.worktrees/web/pr-1',
      },
    }),
  });
  const { router } = renderApp('/pulls?pr=github.com%2Fo%2Fweb%231');
  const view = await screen.findByTestId('pull-request');
  await userEvent.click(
    await within(view).findByRole('button', { name: 'Work on PR' }),
  );
  await waitFor(() =>
    expect(
      calls.find(
        (c) => c.key === 'POST /pull-requests/o/web/1/work?host=github.com',
      )?.body,
    ).toEqual({ agentId: 'a1' }),
  );
  await waitFor(() => expect(router.state.location.pathname).toBe('/a/a1'));
  expect(router.state.location.search).toMatchObject({ s: 's9' });
});
