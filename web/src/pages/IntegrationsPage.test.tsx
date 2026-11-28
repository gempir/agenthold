import { stubGlobal } from '@/test-setup';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it } from 'bun:test';
import type { OrgSkill, OrgTool } from '@/api/queries';
import { mockApi, renderApp } from '@/test-utils';

const user = { id: 'u1', email: 'a@x.io', name: 'Alice', avatarUrl: '' };
const me = { user };
const adminMe = { user, admin: true };
const t = '2030-01-01T00:00:00Z';

const tool = (over: Partial<OrgTool>): OrgTool => ({
  id: 'otl_x',
  slug: 'x',
  name: 'X',
  description: '',
  kind: 'mcp_http',
  authMode: 'none',
  config: {},
  hasSecret: false,
  secretHosts: [],
  enabledByDefault: true,
  userEnabled: true,
  connected: false,
  allowedRoles: [],
  createdAt: t,
  updatedAt: t,
  ...over,
});

const sentry = tool({
  id: 'otl_sentry',
  slug: 'sentry',
  name: 'Sentry',
  description: 'Error tracking',
  authMode: 'org_shared',
  hasSecret: true,
  secretHosts: ['sentry.io'],
  config: { url: 'https://mcp.sentry.dev/mcp' },
});
const linear = tool({
  id: 'otl_lin',
  slug: 'linear',
  name: 'Linear',
  description: 'Issues',
  kind: 'executor_app',
  authMode: 'user_oauth',
  config: { executorCatalogId: 'linear' },
});
const skill: OrgSkill = {
  id: 'osk_1',
  name: 'release-notes',
  description: 'Write release notes',
  content: 'Do it well.',
  enabledByDefault: true,
  createdAt: t,
  updatedAt: t,
};

const shell = { 'GET /agents': [], 'GET /apps': [] };

it('lists org tools with a personal toggle and one-click Connect for users', async () => {
  let tools = [sentry, linear];
  const { calls } = mockApi({
    ...shell,
    'GET /me': me,
    'GET /org/tools': () => ({ body: tools }),
    'GET /org/skills': [skill],
    'PUT /me/org-tools/otl_sentry': (r: { body: unknown }) => {
      const enabled = (r.body as { enabled: boolean }).enabled;
      tools = tools.map((x) =>
        x.id === 'otl_sentry' ? { ...x, userEnabled: enabled } : x,
      );
      return { body: tools[0] };
    },
    'POST /me/org-tools/otl_lin/connect': {
      toolId: 'otl_lin',
      slug: 'linear',
      connected: false,
      session: {
        connectionId: 'con_1',
        token: 'tok_1',
        providerId: 'prv_lin',
        providerName: 'Linear',
        methods: [
          {
            name: 'apiKey',
            kind: 'secrets',
            fields: [{ name: 'key', title: 'API key', required: true }],
          },
        ],
      },
    },
    'POST /me/executor/connections/submit': () => {
      tools = tools.map((x) =>
        x.id === 'otl_lin' ? { ...x, connected: true } : x,
      );
      return { body: { id: 'acc_1' } };
    },
  });
  renderApp('/settings/integrations');

  const list = await screen.findByRole('list', {
    name: 'Organization tool list',
  });
  const sentryRow = within(list).getByRole('listitem', { name: 'Sentry' });
  expect(sentryRow).toHaveTextContent('Provided by organization');
  expect(sentryRow).toHaveTextContent('Error tracking');
  const toggle = within(sentryRow).getByRole('switch', { name: 'Use Sentry' });
  expect(toggle).toHaveAttribute('aria-checked', 'true');

  const u = userEvent.setup();
  await u.click(toggle);
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'PUT /me/org-tools/otl_sentry')?.body,
    ).toEqual({ enabled: false }),
  );
  await waitFor(() => expect(toggle).toHaveAttribute('aria-checked', 'false'));

  const linearRow = within(list).getByRole('listitem', { name: 'Linear' });
  expect(linearRow).not.toHaveTextContent('Provided by organization');
  await u.click(
    within(linearRow).getByRole('button', { name: 'Connect Linear' }),
  );
  await u.type(await within(linearRow).findByLabelText(/API key/), 'lin_1');
  await u.click(
    within(linearRow).getByRole('button', { name: 'Save connection' }),
  );
  expect(await within(linearRow).findByText('Connected')).toBeVisible();

  // Org skills are listed read-only; no admin controls for users.
  const skills = screen.getByRole('region', { name: 'Organization skills' });
  expect(within(skills).getByText('release-notes')).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Add tool' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Add skill' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull();
});

it('lets admins add, edit and delete org tools', async () => {
  stubGlobal('confirm', () => true);
  let tools = [sentry];
  const { calls } = mockApi({
    ...shell,
    'GET /me': adminMe,
    'GET /org/tools': () => ({ body: tools }),
    'GET /org/skills': [],
    'POST /org/tools': (r: { body: unknown }) => {
      const created = tool({ ...(r.body as Partial<OrgTool>), id: 'otl_new' });
      tools = [...tools, created];
      return { status: 201, body: created };
    },
    'PUT /org/tools/otl_sentry': (r: { body: unknown }) => ({
      body: { ...sentry, ...(r.body as object) },
    }),
    'DELETE /org/tools/otl_sentry': () => {
      tools = tools.filter((x) => x.id !== 'otl_sentry');
      return { status: 204 };
    },
  });
  renderApp('/settings/integrations');
  const u = userEvent.setup();

  await u.click(await screen.findByRole('button', { name: 'Add tool' }));
  const form = screen.getByRole('form', { name: 'New organization tool' });
  await u.type(within(form).getByLabelText('Name'), 'GitHub');
  await u.type(within(form).getByLabelText('Slug'), 'github');
  await u.type(within(form).getByLabelText('Description'), 'Code hosting');
  await u.selectOptions(within(form).getByLabelText('Authentication'), [
    'org_shared',
  ]);
  await u.type(
    within(form).getByLabelText('Server URL'),
    'https://api.githubcopilot.com/mcp',
  );
  await u.type(within(form).getByLabelText('Shared secret'), 'ghp_org');
  await u.type(within(form).getByLabelText('Secret hosts'), 'api.github.com');
  await u.type(within(form).getByLabelText(/Allowed roles/), 'user, admin');
  await u.click(within(form).getByRole('button', { name: 'Add tool' }));
  await waitFor(() =>
    expect(calls.find((c) => c.key === 'POST /org/tools')?.body).toEqual({
      slug: 'github',
      name: 'GitHub',
      description: 'Code hosting',
      kind: 'mcp_http',
      authMode: 'org_shared',
      config: { url: 'https://api.githubcopilot.com/mcp', headers: {} },
      enabledByDefault: true,
      allowedRoles: ['user', 'admin'],
      secretHosts: ['api.github.com'],
      secret: 'ghp_org',
    }),
  );
  expect(await screen.findByRole('listitem', { name: 'GitHub' })).toBeVisible();
  // The secret is never echoed back in the UI.
  expect(document.body).not.toHaveTextContent('ghp_org');

  // Edit keeps the saved secret unless a new one is typed.
  const sentryRow = screen.getByRole('listitem', { name: 'Sentry' });
  await u.click(within(sentryRow).getByRole('button', { name: 'Edit' }));
  const edit = screen.getByRole('form', { name: 'Edit Sentry' });
  expect(within(edit).getByLabelText('Shared secret (saved)')).toHaveValue('');
  const nameInput = within(edit).getByLabelText('Name');
  await u.clear(nameInput);
  await u.type(nameInput, 'Sentry EU');
  await u.click(within(edit).getByRole('button', { name: 'Save tool' }));
  await waitFor(() => {
    const body = calls.find((c) => c.key === 'PUT /org/tools/otl_sentry')
      ?.body as Record<string, unknown>;
    expect(body.name).toBe('Sentry EU');
    expect(body.secretHosts).toEqual(['sentry.io']);
    expect('secret' in body).toBe(false);
  });

  await u.click(
    within(await screen.findByRole('listitem', { name: 'Sentry' })).getByRole(
      'button',
      { name: 'Delete' },
    ),
  );
  await waitFor(() =>
    expect(screen.queryByRole('listitem', { name: 'Sentry' })).toBeNull(),
  );
});

it('lets admins manage org skills', async () => {
  stubGlobal('confirm', () => true);
  let skills: OrgSkill[] = [skill];
  const { calls } = mockApi({
    ...shell,
    'GET /me': adminMe,
    'GET /org/tools': [],
    'GET /org/skills': () => ({ body: skills }),
    'POST /org/skills': (r: { body: unknown }) => {
      const created = { ...skill, ...(r.body as object), id: 'osk_2' };
      skills = [...skills, created];
      return { status: 201, body: created };
    },
    'DELETE /org/skills/osk_1': () => {
      skills = skills.filter((s) => s.id !== 'osk_1');
      return { status: 204 };
    },
  });
  renderApp('/settings/integrations');
  const u = userEvent.setup();
  await u.click(await screen.findByRole('button', { name: 'Add skill' }));
  const form = screen.getByRole('form', { name: 'New organization skill' });
  await u.type(within(form).getByLabelText('Skill name'), 'triage');
  await u.type(
    within(form).getByLabelText('Skill description'),
    'Triage incoming bugs',
  );
  await u.type(within(form).getByLabelText(/Instructions/), 'Label them.');
  await u.click(within(form).getByLabelText(/Enabled for everyone/));
  await u.click(within(form).getByRole('button', { name: 'Add skill' }));
  await waitFor(() =>
    expect(calls.find((c) => c.key === 'POST /org/skills')?.body).toEqual({
      name: 'triage',
      description: 'Triage incoming bugs',
      content: 'Label them.',
      enabledByDefault: false,
    }),
  );
  const list = screen.getByRole('list', { name: 'Organization skill list' });
  expect(await within(list).findByText('triage')).toBeVisible();
  await u.click(
    within(
      within(list).getByRole('listitem', { name: 'release-notes' }),
    ).getByRole('button', { name: 'Delete' }),
  );
  await waitFor(() =>
    expect(within(list).queryByText('release-notes')).toBeNull(),
  );
});

it('keeps legacy settings URLs as Integrations tabs', async () => {
  mockApi({
    ...shell,
    'GET /me': me,
    'GET /mcp-servers': [],
    'GET /skills': [],
    'GET /me/git': {
      authorName: '',
      authorEmail: '',
      credentials: [],
      github: { enabled: false },
    },
  });
  const { router } = renderApp('/settings/tools');
  expect(
    await screen.findByRole('heading', { name: 'Integrations' }),
  ).toBeVisible();
  const tabs = screen.getByRole('navigation', {
    name: 'Integrations sections',
  });
  expect(within(tabs).getByRole('link', { name: 'Advanced' })).toHaveAttribute(
    'aria-current',
    'page',
  );
  // Settings nav has one Integrations entry, active for the legacy path.
  const nav = screen.getByRole('navigation', { name: 'Account settings' });
  expect(
    within(nav).getByRole('link', { name: 'Integrations' }),
  ).toHaveAttribute('aria-current', 'page');
  for (const gone of ['Accounts', 'Tools', 'Git'])
    expect(within(nav).queryByRole('link', { name: gone })).toBeNull();

  await userEvent.click(
    within(tabs).getByRole('link', { name: 'Git & GitHub' }),
  );
  await waitFor(() =>
    expect(router.state.location.pathname).toBe('/settings/git'),
  );
  expect(
    await screen.findByRole('region', { name: 'Author identity' }),
  ).toBeVisible();
});
