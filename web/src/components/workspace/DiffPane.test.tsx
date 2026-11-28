import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, it, vi } from 'bun:test';
import type { GitDiff, GitRepo } from '@/api/queries';
import { DiffPane } from '@/components/workspace/DiffPane';
import { getDraft } from '@/lib/drafts';
import { mockApi } from '@/test-utils';

const repo = (over: Partial<GitRepo> = {}): GitRepo => ({
  path: 'web',
  project: 'web',
  branch: 'feat/login',
  detached: false,
  dirty: true,
  ahead: 0,
  behind: 0,
  isWorktree: false,
  ...over,
});

const patch = [
  'diff --git a/src/app.ts b/src/app.ts',
  '--- a/src/app.ts',
  '+++ b/src/app.ts',
  '@@ -1,3 +1,3 @@',
  ' import { start } from "./start";',
  '-start(8080);',
  '+start(9090);',
  ' export {};',
  '',
].join('\n');

const diff = (over: Partial<GitDiff> = {}): GitDiff => ({
  mode: 'uncommitted',
  repo: repo(),
  files: [
    {
      path: 'src/app.ts',
      status: 'M',
      additions: 1,
      deletions: 1,
      binary: false,
      truncated: false,
      patch,
    },
    {
      path: 'logo.png',
      status: 'A',
      additions: 0,
      deletions: 0,
      binary: true,
      truncated: false,
    },
  ],
  additions: 1,
  deletions: 1,
  truncated: false,
  ...over,
});

function renderPane(
  props: Partial<Parameters<typeof DiffPane>[0]> = {},
  routes: Record<string, unknown> = {},
) {
  const api = mockApi({
    'GET /agents/a1/git/repos': {
      connected: true,
      repos: [repo(), repo({ path: 'api', project: 'api' })],
    },
    'GET /agents/a1/git/diff?path=web&mode=uncommitted': diff(),
    ...routes,
  });
  const onOpenFile = vi.fn();
  render(
    <QueryClientProvider client={new QueryClient()}>
      <DiffPane
        agentId="a1"
        canUse
        repoPath="web/src"
        composerKey="s1"
        onOpenFile={onOpenFile}
        {...props}
      />
    </QueryClientProvider>,
  );
  return { ...api, onOpenFile };
}

it('lists the changed files with stats, status and their lines', async () => {
  const { onOpenFile } = renderPane({ repoPath: 'web' });
  const files = await screen.findAllByTestId('diff-file');
  expect(files).toHaveLength(2);
  expect(files[0]).toHaveTextContent('src/app.ts');
  expect(within(files[0]).getByLabelText('Modified')).toHaveTextContent('M');
  expect(files[0]).toHaveTextContent('+1 −1');
  expect(files[1]).toHaveTextContent('Binary file');
  expect(screen.getByText('2 files changed')).toBeVisible();
  // Several repositories: a picker, on the one the thread is in.
  expect(screen.getByRole('combobox', { name: 'Repository' })).toHaveValue(
    'web',
  );
  const rows = files[0].querySelectorAll('tr[data-row]');
  expect([...rows].map((r) => r.getAttribute('data-row'))).toEqual([
    'context',
    'del',
    'add',
    'context',
  ]);
  await userEvent.click(
    within(files[0]).getByRole('button', { name: 'Open src/app.ts' }),
  );
  // Opens at the first added line, relative to the home.
  expect(onOpenFile).toHaveBeenCalledWith('web/src/app.ts', 2);
});

it('switches to split view and remembers it', async () => {
  renderPane();
  const [file] = await screen.findAllByTestId('diff-file');
  await userEvent.click(screen.getByRole('button', { name: 'Split view' }));
  expect(localStorage.getItem('agenthold.diff.layout')).toBe('split');
  // The changed line pair sits side by side in one row.
  const pair = within(file).getByText('start(8080);').closest('tr')!;
  expect(pair).toHaveTextContent('start(9090);');
  expect(screen.getByRole('button', { name: 'Unified view' })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
});

it('collapses and expands every file', async () => {
  renderPane();
  await screen.findAllByTestId('diff-file');
  await userEvent.click(screen.getByRole('button', { name: 'Collapse all' }));
  expect(screen.queryByText('start(8080);')).toBeNull();
  expect(
    screen.getByRole('button', { name: 'Expand src/app.ts' }),
  ).toHaveAttribute('aria-expanded', 'false');
  await userEvent.click(screen.getByRole('button', { name: 'Expand all' }));
  expect(screen.getByText('start(8080);')).toBeVisible();
  await userEvent.click(
    screen.getByRole('button', { name: 'Collapse src/app.ts' }),
  );
  expect(screen.queryByText('start(8080);')).toBeNull();
});

it('finds in the diff with Cmd+F, unfolding matching files', async () => {
  renderPane();
  await screen.findAllByTestId('diff-file');
  await userEvent.click(screen.getByRole('button', { name: 'Collapse all' }));
  fireEvent.keyDown(screen.getByTestId('diff-pane'), {
    key: 'f',
    metaKey: true,
  });
  const input = await screen.findByRole('textbox', { name: 'Find in diff' });
  await userEvent.type(input, 'start');
  // The import line and both changed lines match; the file unfolded.
  expect(screen.getByText('1/3')).toBeVisible();
  expect(screen.getByText('start(8080);')).toBeVisible();
  await userEvent.type(input, '{Enter}');
  expect(screen.getByText('2/3')).toBeVisible();
  expect(
    screen.getByText('start(8080);').closest('[data-find-current]'),
  ).not.toBeNull();
  await userEvent.type(input, '{Shift>}{Enter}{Enter}{/Shift}');
  expect(screen.getByText('3/3')).toBeVisible();
  await userEvent.type(input, '{Escape}');
  expect(screen.queryByRole('textbox', { name: 'Find in diff' })).toBeNull();
});

it('commits the uncommitted changes', async () => {
  const { calls } = renderPane(
    {},
    {
      'POST /agents/a1/git/commit': () => ({
        status: 201,
        body: {
          repo: repo({ dirty: false }),
          commit: {
            sha: 'abc1234def',
            shortSha: 'abc1234',
            subject: 'Use port 9090',
            body: '',
            author: 'Alice',
            email: 'a@x.io',
            date: '2030-01-01T00:00:00Z',
            filesChanged: 2,
            additions: 1,
            deletions: 1,
          },
        },
      }),
    },
  );
  await screen.findAllByTestId('diff-file');
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Commit message' }),
    'Use port 9090',
  );
  await userEvent.click(screen.getByRole('button', { name: 'Commit' }));
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'POST /agents/a1/git/commit')?.body,
    ).toEqual({ path: 'web', message: 'Use port 9090', all: true }),
  );
  expect(await screen.findByText('abc1234')).toBeVisible();
});

it('hides the commit bar from viewers', async () => {
  renderPane({ canUse: false, composerKey: undefined });
  await screen.findAllByTestId('diff-file');
  expect(screen.queryByRole('textbox', { name: 'Commit message' })).toBeNull();
  expect(screen.getByRole('button', { name: 'New line 2' })).toBeDisabled();
});

it('comments on a line range and adds it to the draft', async () => {
  renderPane();
  await screen.findAllByTestId('diff-file');
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: 'New line 1' }));
  // A plain click starts over; a shift-click extends the selection.
  await user.click(screen.getByRole('button', { name: 'New line 2' }));
  expect(
    screen.getByRole('group', { name: 'Review comment' }),
  ).toHaveTextContent('Line 2');
  await user.keyboard('{Shift>}');
  await user.click(screen.getByRole('button', { name: 'New line 3' }));
  await user.keyboard('{/Shift}');
  const box = screen.getByRole('group', { name: 'Review comment' });
  expect(box).toHaveTextContent('Lines 2-3');
  await user.type(
    within(box).getByRole('textbox', { name: 'Comment' }),
    'Make the port configurable',
  );
  await user.click(
    within(box).getByRole('button', { name: 'Add to composer' }),
  );
  expect(getDraft('s1')).toBe(
    [
      'Review comment on `web/src/app.ts` lines 2-3 (uncommitted diff):',
      '```',
      '+start(9090);',
      ' export {};',
      '```',
      'Make the port configurable',
      '',
    ].join('\n'),
  );
  expect(screen.getByRole('status')).toHaveTextContent(
    'Added to your message draft.',
  );
  expect(screen.queryByRole('group', { name: 'Review comment' })).toBeNull();
});

it('shows a commit picked from the list of commits', async () => {
  const commit = (sha: string, subject: string) => ({
    sha,
    shortSha: sha.slice(0, 7),
    subject,
    body: '',
    author: 'Alice',
    email: 'a@x.io',
    date: '2030-01-01T00:00:00Z',
    filesChanged: 1,
    additions: 1,
    deletions: 1,
  });
  const { calls } = renderPane(
    { repoPath: 'web' },
    {
      'GET /agents/a1/git/commits?path=web&limit=100': {
        repo: repo(),
        commits: [
          commit('aaaaaaa111', 'Newest'),
          commit('bbbbbbb222', 'Older'),
        ],
      },
      'GET /agents/a1/git/diff?path=web&mode=commit&ref=aaaaaaa111': diff({
        mode: 'commit',
        ref: 'aaaaaaa111',
      }),
      'GET /agents/a1/git/diff?path=web&mode=commit&ref=bbbbbbb222': diff({
        mode: 'commit',
        ref: 'bbbbbbb222',
        files: [],
        additions: 0,
        deletions: 0,
      }),
    },
  );
  await screen.findAllByTestId('diff-file');
  await userEvent.click(screen.getByRole('radio', { name: 'Commits' }));
  const picker = await screen.findByRole('combobox', { name: 'Commit' });
  expect(picker).toHaveValue('aaaaaaa111');
  expect(screen.queryByRole('textbox', { name: 'Commit message' })).toBeNull();
  await userEvent.selectOptions(picker, 'bbbbbbb222');
  expect(
    await screen.findByText('This commit changes no files.'),
  ).toBeVisible();
  expect(calls.map((c) => c.key)).toContain(
    'GET /agents/a1/git/diff?path=web&mode=commit&ref=bbbbbbb222',
  );
});

it('offers to start a sleeping agent', async () => {
  const onStart = vi.fn();
  renderPane(
    { onStart },
    {
      'GET /agents/a1/git/repos': { connected: false, repos: [] },
      'GET /agents/a1/git/diff?path=web%2Fsrc&mode=uncommitted': () => ({
        status: 409,
        body: { error: 'agent is not running' },
      }),
    },
  );
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'agent is not running',
  );
  await userEvent.click(screen.getByRole('button', { name: 'Start' }));
  expect(onStart).toHaveBeenCalled();
});
