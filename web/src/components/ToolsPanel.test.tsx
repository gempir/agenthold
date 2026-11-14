import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, it } from 'vitest';
import { mockApi } from '@/test-utils';
import { AgentToolsPanel, parsePairs } from './ToolsPanel';

const t = '2026-01-01T00:00:00Z';
const server = (id: string, name: string, scope: 'agent' | 'workspace') => ({
  id,
  name,
  scope,
  workspaceId: 'ws1',
  transport: 'http',
  url: `https://${name}.example.com/mcp`,
  command: '',
  args: [],
  env: {},
  headers: {},
  secretRefs: [],
  createdAt: t,
  updatedAt: t,
});
const skill = (id: string, name: string) => ({
  id,
  name,
  description: `about ${name}`,
  content: 'x',
  workspaceId: 'ws1',
  createdAt: t,
  updatedAt: t,
});

function renderPanel(admin: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <AgentToolsPanel agentId="ag1" workspaceId="ws1" admin={admin} />
    </QueryClientProvider>,
  );
}

it('parses key/value lines', () => {
  expect(parsePairs('Authorization: Bearer x\n\nbad\nX-A:1', ':')).toEqual({
    Authorization: 'Bearer x',
    'X-A': '1',
  });
  expect(parsePairs('A=b=c', '=')).toEqual({ A: 'b=c' });
});

it('lists what the agent uses and attaches a skill', async () => {
  const { calls } = mockApi({
    'GET /agents/ag1/mcp-servers': [
      server('m1', 'docs', 'workspace'),
      server('m2', 'local', 'agent'),
    ],
    'GET /agents/ag1/skills': [skill('s1', 'release-notes')],
    'GET /workspaces/ws1/mcp-servers': [
      server('m1', 'docs', 'workspace'),
      server('m3', 'search', 'workspace'),
    ],
    'GET /workspaces/ws1/skills': [
      skill('s1', 'release-notes'),
      skill('s2', 'deploy'),
    ],
    'PUT /agents/ag1/skills/s2': { status: 204 },
  });
  renderPanel(true);
  const list = await screen.findByRole('list', { name: 'Agent MCP servers' });
  await waitFor(() => expect(list).toHaveTextContent('docs'));
  expect(list).toHaveTextContent('this agent');
  expect(
    await screen.findByRole('list', { name: 'Agent skills' }),
  ).toHaveTextContent('release-notes');

  const user = userEvent.setup();
  await waitFor(() =>
    expect(screen.getByRole('option', { name: 'deploy' })).toBeInTheDocument(),
  );
  expect(
    screen.queryByRole('option', { name: 'release-notes' }),
  ).not.toBeInTheDocument(); // already attached
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Skill' }),
    's2',
  );
  await user.click(screen.getAllByRole('button', { name: 'Attach' })[1]);
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'PUT /agents/ag1/skills/s2')).toBe(true),
  );
});

it('is read-only for non-admins', async () => {
  mockApi({
    'GET /agents/ag1/mcp-servers': [server('m1', 'docs', 'workspace')],
    'GET /agents/ag1/skills': [],
  });
  renderPanel(false);
  await screen.findByRole('list', { name: 'Agent MCP servers' });
  expect(
    screen.queryByRole('button', { name: /Attach|Detach|Add a server/ }),
  ).not.toBeInTheDocument();
});
