import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it } from 'bun:test';
import { mockApi, renderApp } from '@/test-utils';

it('shows debounced message snippets and closes with Escape', async () => {
  mockApi({
    'GET /me': {
      user: { id: 'u1', name: 'Alice', email: 'a@x.io' },
      role: 'user',
    },
    'GET /agents': [],
    'GET /apps': [],
    'GET /projects': [],
    'GET /sessions/search': [
      {
        session: { id: 's1', agentId: 'a1', title: 'Found thread' },
        matchField: 'message',
        snippet: 'The hidden message needle',
      },
    ],
  });
  renderApp('/projects');
  await screen.findByRole('heading', { name: 'Projects' });
  await userEvent.keyboard('{Control>}k{/Control}');
  const dialog = await screen.findByRole('dialog', { name: 'Command palette' });
  await userEvent.type(within(dialog).getByRole('combobox'), 'needle');
  expect(
    await within(dialog).findByText('The hidden message needle'),
  ).toBeVisible();
  await userEvent.keyboard('{Escape}');
  expect(screen.queryByRole('dialog', { name: 'Command palette' })).toBeNull();
});
