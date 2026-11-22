import { stubGlobal } from '@/test-setup';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'bun:test';
import { mockApi, renderApp } from '@/test-utils';

const providers = [
  { id: 'github', name: 'GitHub', type: 'github' },
  { id: 'kc', name: 'Keycloak', type: 'oidc' },
];

it('renders providers with encoded redirect hrefs and the error', async () => {
  mockApi({ 'GET /auth/providers': providers });
  renderApp('/login?redirect=%2Fa%2Fabc%3Fx%3D1&error=access%20denied');
  const gh = await screen.findByRole('link', { name: /GitHub/ });
  expect(gh).toHaveAttribute(
    'href',
    '/api/v1/auth/login/github?redirect=%2Fa%2Fabc%3Fx%3D1',
  );
  expect(screen.getByRole('link', { name: /Keycloak/ })).toHaveAttribute(
    'href',
    '/api/v1/auth/login/kc?redirect=%2Fa%2Fabc%3Fx%3D1',
  );
  expect(screen.getByRole('alert')).toHaveTextContent('access denied');
});

it('defaults redirect to / and rejects external redirects', async () => {
  mockApi({ 'GET /auth/providers': providers });
  renderApp('/login?redirect=https%3A%2F%2Fevil.example');
  expect(await screen.findByRole('link', { name: /GitHub/ })).toHaveAttribute(
    'href',
    '/api/v1/auth/login/github?redirect=%2F',
  );
});

it('says when sign-in is not set up', async () => {
  mockApi({ 'GET /auth/providers': [] });
  renderApp('/login');
  expect(
    await screen.findByText(/Sign-in is not set up on this server/),
  ).toBeInTheDocument();
});

it('signs in the local admin with username and password', async () => {
  const assign = vi.fn();
  stubGlobal('location', { ...globalThis.location, assign });
  const { calls } = mockApi({
    'GET /auth/providers': [
      { id: 'local', name: 'Username and password', type: 'local' },
      ...providers,
    ],
    'POST /auth/local': (req: { body: unknown }) =>
      (req.body as { password: string }).password === 'right'
        ? { status: 204 }
        : { status: 401, body: { error: 'wrong username or password' } },
  });
  renderApp('/login?redirect=%2Fa%2Fx');
  const user = userEvent.setup();
  await user.type(
    await screen.findByRole('textbox', { name: 'Username' }),
    'admin',
  );
  await user.type(screen.getByLabelText('Password'), 'wrong');
  await user.click(screen.getByRole('button', { name: 'Sign in' }));
  expect(
    await screen.findByText('wrong username or password'),
  ).toBeInTheDocument();
  expect(assign).not.toHaveBeenCalled();

  await user.clear(screen.getByLabelText('Password'));
  await user.type(screen.getByLabelText('Password'), 'right');
  await user.click(screen.getByRole('button', { name: 'Sign in' }));
  await waitFor(() => expect(assign).toHaveBeenCalledWith('/a/x'));
  expect(
    calls.filter((c) => c.key === 'POST /auth/local').at(-1)?.body,
  ).toEqual({ username: 'admin', password: 'right' });
  // OIDC/GitHub buttons are still offered next to the form.
  expect(screen.getByRole('link', { name: /GitHub/ })).toBeInTheDocument();
});
