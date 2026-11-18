import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { mockApi, renderApp } from '@/test-utils';

const user = { id: 'u1', name: 'Alice', email: 'a@example.com' };
const app = {
  id: 'ap1',
  name: 'site',
  owner: user,
  cpus: 1,
  memoryMb: 1024,
  idleMinutes: 0,
  state: 'running',
  stateError: '',
  stateChangedAt: '2026-01-01T00:00:00Z',
  needsRebuild: false,
  isTemplate: false,
  role: 'owner',
  url: 'https://site.apps.example.com/',
  createdByAgent: { id: 'ag1', name: 'builder' },
  links: {},
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
};

function mocks(extra: Record<string, unknown> = {}) {
  return mockApi({
    'GET /me': { user, admin: false },
    'GET /agents': [],
    'GET /apps': [app],
    'GET /apps/ap1': app,
    'GET /sandbox/limits': {
      driver: 'fake',
      defaultCpus: 2,
      defaultMemoryMb: 4096,
      maxCpus: 8,
      maxMemoryMb: 16384,
      minMemoryMb: 1024,
    },
    'GET /agents/ap1/services': {
      running: true,
      services: [
        {
          name: 'app',
          command: 'node server.js',
          workdir: 'app/current',
          env: { PORT: '3000' },
          enabled: true,
          status: { state: 'running', restarts: 0, pid: 7 },
          createdAt: '2026-01-01T00:00:00Z',
          updatedAt: '2026-01-01T00:00:00Z',
        },
      ],
    },
    'GET /agents/ap1/exposures': {
      enabled: true,
      hostSuffix: '.apps.example.com',
      exposures: [],
    },
    ...extra,
  });
}

it('shows an app with its manager, service, URL and the apps list', async () => {
  mocks();
  renderApp('/apps/ap1');
  expect(await screen.findByText('Always on')).toBeInTheDocument();
  expect(screen.getByRole('note')).toHaveTextContent(
    'Managed by builder: it can deploy to, run commands in, and read logs of this app, including with secrets attached here.',
  );
  expect(screen.getByRole('link', { name: 'builder' })).toHaveAttribute(
    'href',
    '/a/ag1',
  );
  expect(await screen.findByLabelText('app status')).toHaveTextContent(
    'running',
  );
  expect(screen.getByRole('link', { name: 'Open' })).toHaveAttribute(
    'href',
    app.url,
  );
  const apps = await screen.findByRole('list', { name: 'Apps' });
  expect(apps).toHaveTextContent('site');
  expect(apps).toHaveTextContent('site.apps.example.com');
});

it('sets an idle timeout instead of always on', async () => {
  const { calls } = mocks({
    'PATCH /apps/ap1': (req: { body: unknown }) => ({
      body: { ...app, ...(req.body as object) },
    }),
  });
  renderApp('/apps/ap1?tab=settings');
  const u = userEvent.setup();
  await u.click(await screen.findByRole('checkbox', { name: /Always on/ }));
  await u.clear(screen.getByRole('textbox', { name: /Sleep after idle/ }));
  await u.type(screen.getByRole('textbox', { name: /Sleep after idle/ }), '45');
  await u.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() =>
    expect(calls.find((c) => c.key === 'PATCH /apps/ap1')?.body).toMatchObject({
      idleMinutes: 45,
    }),
  );
});

it('lets the owner take the app over', async () => {
  let current: typeof app & { createdByAgent?: unknown } = app;
  const { calls } = mocks({
    'GET /apps/ap1': () => ({ body: current }),
    'POST /apps/ap1/take-over': () => {
      current = { ...app, createdByAgent: undefined };
      return { body: current };
    },
  });
  vi.stubGlobal('confirm', () => true);
  renderApp('/apps/ap1');
  const u = userEvent.setup();
  await u.click(await screen.findByRole('button', { name: 'Take over' }));
  await waitFor(() =>
    expect(calls.map((c) => c.key)).toContain('POST /apps/ap1/take-over'),
  );
  await waitFor(() =>
    expect(screen.queryByRole('note')).not.toBeInTheDocument(),
  );
});
