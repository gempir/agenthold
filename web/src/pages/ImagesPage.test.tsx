import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { mockApi, renderApp } from '@/test-utils';

afterEach(() => vi.unstubAllGlobals());

const admin = {
  admin: true,
  user: { id: 'u1', name: 'Alice', email: 'a@example.com', avatarUrl: '' },
};

const baseImage = {
  id: 'base',
  name: 'agenthold Base',
  description: 'Debian trixie with Node.js, Python, and coding harnesses.',
  isDefault: true,
};

const sampleImage = {
  id: 'img_py',
  name: 'Python Data',
  description: 'Python 3.12 with uv and ripgrep',
  aptPackages: ['python3-venv', 'ripgrep'],
  setupScript: 'echo hello',
  env: { UV_LINK_MODE: 'copy' },
  secretIds: [],
  isDefault: false,
  latestRevision: 1,
  readyArches: ['arm64', 'amd64'],
  building: false,
  agentCount: 2,
  latestBuildStatus: 'ready' as const,
  createdAt: '2030-01-01T00:00:00Z',
  updatedAt: '2030-01-01T00:00:00Z',
};

it('shows agenthold Base and custom images with status and arch badges', async () => {
  mockApi({
    'GET /me': admin,
    'GET /images': {
      base: baseImage,
      images: [sampleImage],
    },
  });
  renderApp('/settings/images');
  expect(await screen.findByText('agenthold Base')).toBeVisible();
  expect(screen.getByText('Built-in')).toBeVisible();

  const list = await screen.findByRole('list', { name: 'Custom images list' });
  const item = within(list).getByRole('listitem');
  expect(within(item).getByText('Python Data')).toBeVisible();
  expect(within(item).getByText('rev 1')).toBeVisible();
  expect(within(item).getByText('arm64')).toBeVisible();
  expect(within(item).getByText('amd64')).toBeVisible();
  expect(within(item).getByText('2 machines')).toBeVisible();
  // Cannot delete while machines reference the image.
  expect(
    within(item).getByRole('button', { name: 'Delete Python Data' }),
  ).toBeDisabled();
});

it('lets an admin create a custom image with packages, script, and env vars', async () => {
  const api = mockApi({
    'GET /me': admin,
    'GET /secrets': [],
    'GET /images': { base: baseImage, images: [] },
    'POST /images': () => ({
      status: 201,
      body: { ...sampleImage, id: 'img_new', name: 'Rust Dev', agentCount: 0 },
    }),
  });
  renderApp('/settings/images');
  await userEvent.click(
    await screen.findByRole('button', { name: 'New image' }),
  );
  await userEvent.type(screen.getByLabelText('Name'), 'Rust Dev');
  await userEvent.type(
    screen.getByLabelText(/^APT packages/),
    'build-essential, pkg-config',
  );
  await userEvent.type(
    screen.getByLabelText(/^Setup script/),
    'echo install-rust',
  );
  await userEvent.click(screen.getByRole('button', { name: 'Add variable' }));
  await userEvent.type(screen.getByLabelText('Env key 1'), 'RUST_LOG');
  await userEvent.type(screen.getByLabelText('Env value 1'), 'info');
  await userEvent.click(screen.getByRole('button', { name: 'Save' }));

  await waitFor(() =>
    expect(api.calls.some((c) => c.key === 'POST /images')).toBe(true),
  );
  expect(api.calls.find((c) => c.key === 'POST /images')?.body).toMatchObject({
    name: 'Rust Dev',
    aptPackages: ['build-essential', 'pkg-config'],
    setupScript: 'echo install-rust',
    env: { RUST_LOG: 'info' },
  });
});

it('starts a build and opens the live build log viewer', async () => {
  const buildRow = {
    id: 'bld_1',
    imageId: 'img_py',
    revision: 2,
    arch: 'arm64',
    nodeId: 'local',
    status: 'ready' as const,
    error: '',
    layerSha256: 'abc',
    layerSize: 10 * 1024 * 1024,
    zstdSize: 2 * 1024 * 1024,
    createdAt: '2030-01-01T00:00:00Z',
  };
  mockApi({
    'GET /me': admin,
    'GET /images': { base: baseImage, images: [sampleImage] },
    'POST /images/img_py/builds': () => ({ status: 202, body: [buildRow] }),
    'GET /images/img_py': { ...sampleImage, builds: [buildRow] },
    'GET /images/img_py/builds/bld_1': {
      ...buildRow,
      log: '==> Booting isolated builder microVM\n==> Build finished\n',
    },
  });
  renderApp('/settings/images');
  await userEvent.click(await screen.findByRole('button', { name: 'Build' }));
  const log = await screen.findByLabelText('Build log');
  expect(log).toHaveTextContent('==> Build finished');
  expect(screen.getByText(/10\.0 MiB raw/)).toBeVisible();
});

it('hides admin actions from regular users', async () => {
  mockApi({
    'GET /me': { ...admin, admin: false },
    'GET /images': { base: baseImage, images: [sampleImage] },
  });
  renderApp('/settings/images');
  expect(await screen.findByText('Python Data')).toBeVisible();
  expect(screen.queryByRole('button', { name: 'New image' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Build' })).toBeNull();
});
