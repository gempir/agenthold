import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it } from 'bun:test';
import type { Project, ProjectStatus, Session } from '@/api/queries';
import { mockApi, renderApp } from '@/test-utils';

const me = {
  user: { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' },
};

const project: Project = {
  id: 'p1',
  name: 'Shop',
  slug: 'shop',
  description: '',
  instructions: '',
  sources: [{ id: 's1', name: 'web', kind: 'repo', url: 'https://x/web.git' }],
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
};

const session = (id: string, title: string): Session => ({
  id,
  agentId: 'a1',
  title,
  running: false,
  createdBy: me.user,
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
  project: { id: 'p1', name: 'Shop', direct: false, branch: `agenthold/${id}` },
});

it('creates a project with a repository', async () => {
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents': [],
    'GET /apps': [],
    'GET /projects': [],
    'POST /projects': () => ({ status: 201, body: project }),
    'GET /projects/p1': project,
    'GET /projects/p1/status': { threads: [], overlaps: [] },
  });
  const { router } = renderApp('/projects?new=true');
  await waitFor(() => expect(router.state.location.pathname).toBe('/projects'));
  const user = userEvent.setup();
  await user.type(await screen.findByRole('textbox', { name: 'Name' }), 'Shop');
  await user.type(
    screen.getByRole('textbox', { name: 'Git URL' }),
    'https://x/web.git',
  );
  await user.click(screen.getByRole('button', { name: 'Create project' }));
  await waitFor(() =>
    expect(calls.find((c) => c.key === 'POST /projects')?.body).toEqual({
      name: 'Shop',
      description: '',
      instructions: '',
      sources: [{ kind: 'repo', url: 'https://x/web.git' }],
    }),
  );
});

it('warns about files two threads change and merges a thread', async () => {
  const status: ProjectStatus = {
    threads: [
      {
        session: session('one', 'Header'),
        agentId: 'a1',
        agentName: 'dev',
        connected: true,
        sources: [
          {
            source: 'web',
            base: 'main',
            commits: 2,
            dirty: false,
            remote: true,
            files: [{ path: 'index.html', status: 'M' }],
          },
        ],
      },
      {
        session: session('two', 'Footer'),
        agentId: 'a1',
        agentName: 'dev',
        connected: true,
        sources: [
          {
            source: 'web',
            base: 'main',
            commits: 0,
            dirty: true,
            remote: true,
            files: [{ path: 'index.html', status: 'M' }],
          },
        ],
      },
    ],
    overlaps: [
      { source: 'web', path: 'index.html', sessionIds: ['one', 'two'] },
    ],
  };
  const { calls } = mockApi({
    'GET /me': me,
    'GET /agents': [],
    'GET /apps': [],
    'GET /projects': [project],
    'GET /projects/p1': project,
    'GET /projects/p1/status': status,
    'POST /sessions/one/project/merge': [
      { source: 'web', done: true, commits: 2 },
    ],
  });
  renderApp('/projects/p1');
  const alert = await screen.findByRole('alert');
  expect(alert).toHaveTextContent('Threads change the same files');
  expect(alert).toHaveTextContent('web/index.html: Header, Footer');

  const rows = within(screen.getByRole('list', { name: 'Project threads' }))
    .getAllByRole('listitem')
    .filter(
      (li) =>
        li.parentElement?.getAttribute('aria-label') === 'Project threads',
    );
  // Uncommitted changes cannot be merged.
  expect(within(rows[1]).getByRole('button', { name: 'Merge' })).toBeDisabled();
  const user = userEvent.setup();
  await user.click(within(rows[0]).getByRole('button', { name: 'Merge' }));
  expect(await within(rows[0]).findByText('web: 2 commits done')).toBeVisible();
  expect(calls.some((c) => c.key === 'POST /sessions/one/project/merge')).toBe(
    true,
  );
});
