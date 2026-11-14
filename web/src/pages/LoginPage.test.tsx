import { screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { mockApi, renderApp } from '@/test-utils';

afterEach(() => vi.unstubAllGlobals());

const providers = [
  { id: 'github', name: 'GitHub', type: 'github' },
  { id: 'kc', name: 'Keycloak', type: 'oidc' },
];

it('renders providers with encoded redirect hrefs and the error', async () => {
  mockApi({ 'GET /auth/providers': providers });
  renderApp('/login?redirect=%2Finvite%2Fabc%3Fx%3D1&error=access%20denied');
  const gh = await screen.findByRole('link', { name: /GitHub/ });
  expect(gh).toHaveAttribute(
    'href',
    '/api/v1/auth/login/github?redirect=%2Finvite%2Fabc%3Fx%3D1',
  );
  expect(screen.getByRole('link', { name: /Keycloak/ })).toHaveAttribute(
    'href',
    '/api/v1/auth/login/kc?redirect=%2Finvite%2Fabc%3Fx%3D1',
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

it('says when no providers are configured', async () => {
  mockApi({ 'GET /auth/providers': [] });
  renderApp('/login');
  expect(
    await screen.findByText(/No sign-in providers are configured/),
  ).toBeInTheDocument();
});
