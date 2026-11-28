import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, it, vi } from 'bun:test';
import type { BrowserState, Exposure } from '@/api/queries';
import {
  BrowserPane,
  elementAt,
  keyAction,
  spanRect,
  toViewport,
  viewportSize,
} from '@/components/workspace/BrowserPane';
import { formatContext } from '@/lib/composerContext';
import { getDraft } from '@/lib/drafts';
import { mockApi } from '@/test-utils';

const exposure = (port: number, name: string): Exposure => ({
  id: `exp_${port}`,
  agentId: 'a1',
  port,
  name,
  access: 'private',
  wakeOnRequest: false,
  url: `https://${name}.apps.test/`,
  createdBy: 'u1',
  createdAt: '2030-01-01T00:00:00Z',
});

const page = (over: Partial<BrowserState> = {}): BrowserState => ({
  available: true,
  running: true,
  url: 'http://localhost:3000/',
  title: 'Todo',
  width: 1280,
  height: 800,
  consoleErrors: 0,
  screenshot: 'iVBORw0KGgo=',
  elements: [
    {
      role: 'button',
      name: 'Save',
      tag: 'button',
      selector: '#save',
      rect: { x: 100, y: 100, width: 200, height: 40 },
    },
    {
      role: 'form',
      name: '',
      tag: 'form',
      selector: 'form',
      rect: { x: 0, y: 0, width: 1280, height: 400 },
    },
  ],
  ...over,
});

function renderPane(
  routes: Record<string, unknown>,
  props: Partial<Parameters<typeof BrowserPane>[0]> = {},
) {
  const api = mockApi(routes);
  render(
    <QueryClientProvider client={new QueryClient()}>
      <BrowserPane agentId="a1" canUse composerKey="s1" {...props} />
    </QueryClientProvider>,
  );
  return api;
}

const noExposures = {
  'GET /agents/a1/exposures': {
    enabled: true,
    hostSuffix: '.apps.test',
    exposures: [],
  },
};

/** The page viewport shown at 640x400: half the size of the 1280x800 page. */
async function viewport() {
  const v = await screen.findByRole('application', { name: 'Page viewport' });
  v.getBoundingClientRect = () =>
    ({ left: 0, top: 0, width: 640, height: 400 }) as DOMRect;
  return v;
}

it('embeds exposed ports with an address bar and viewport presets', async () => {
  renderPane({
    'GET /agents/a1/exposures': {
      enabled: true,
      hostSuffix: '.apps.test',
      exposures: [exposure(3000, 'web'), exposure(8080, 'api')],
    },
  });
  const frame = await screen.findByTitle('Live preview of web');
  expect(frame).toHaveAttribute('src', 'https://web.apps.test/');
  expect(screen.getByRole('link', { name: 'Open in new tab' })).toHaveAttribute(
    'href',
    'https://web.apps.test/',
  );

  await userEvent.click(screen.getByRole('listitem', { name: /api/ }));
  expect(await screen.findByTitle('Live preview of api')).toHaveAttribute(
    'src',
    'https://api.apps.test/',
  );

  const addr = screen.getByRole('textbox', { name: 'Address' });
  await userEvent.clear(addr);
  await userEvent.type(addr, '/health{Enter}');
  expect(screen.getByTitle('Live preview of api')).toHaveAttribute(
    'src',
    'https://api.apps.test/health',
  );

  expect(screen.getByTestId('app-frame')).toHaveAttribute(
    'data-viewport',
    'fill',
  );
  await userEvent.selectOptions(
    screen.getByRole('combobox', { name: 'Viewport' }),
    'mobile',
  );
  expect(screen.getByTestId('app-frame')).toHaveAttribute(
    'data-viewport',
    '390x844',
  );
  await userEvent.click(
    screen.getByRole('button', { name: 'Rotate viewport' }),
  );
  expect(screen.getByTestId('app-frame')).toHaveAttribute(
    'data-viewport',
    '844x390',
  );
});

it('drives the sandbox browser: navigate, click scaled to the page, type', async () => {
  const { calls } = renderPane({
    ...noExposures,
    'GET /agents/a1/browser/status': page({ running: false, url: undefined }),
    'POST /agents/a1/browser/navigate': page(),
    'POST /agents/a1/browser/interact': page({ title: 'Todo (saved)' }),
  });
  // Without exposures the sandbox browser opens first.
  const addr = await screen.findByRole('textbox', { name: 'Browser address' });
  await userEvent.type(addr, 'localhost:3000');
  await userEvent.click(screen.getByRole('button', { name: 'Go' }));
  expect(
    await screen.findByRole('img', { name: 'Screenshot of Todo' }),
  ).toHaveAttribute('src', 'data:image/png;base64,iVBORw0KGgo=');
  expect(calls.find((c) => c.key.endsWith('/navigate'))?.body).toEqual({
    url: 'localhost:3000',
    width: 1280,
    height: 800,
  });

  fireEvent.click(await viewport(), { clientX: 100, clientY: 50 });
  await waitFor(() =>
    expect(calls.filter((c) => c.key.endsWith('/interact'))[0]?.body).toEqual({
      action: 'click',
      x: 200,
      y: 100,
    }),
  );
  expect(await screen.findByText('Todo (saved)')).toBeInTheDocument();

  await userEvent.type(
    screen.getByRole('textbox', { name: 'Type into the page' }),
    'milk',
  );
  await userEvent.click(screen.getByRole('button', { name: 'Type' }));
  await userEvent.click(screen.getByRole('button', { name: 'Press Enter' }));
  await waitFor(() =>
    expect(
      calls.filter((c) => c.key.endsWith('/interact')).map((c) => c.body),
    ).toEqual([
      { action: 'click', x: 200, y: 100 },
      { action: 'type', text: 'milk' },
      { action: 'press', key: 'Enter' },
    ]),
  );
});

it('shows the running page and resizes it with a preset', async () => {
  const { calls } = renderPane({
    ...noExposures,
    'GET /agents/a1/browser/status': page({ screenshot: undefined }),
    'GET /agents/a1/browser/screenshot': page({ consoleErrors: 2 }),
  });
  expect(await screen.findByText('2 console errors')).toBeInTheDocument();
  await userEvent.selectOptions(
    screen.getByRole('combobox', { name: 'Viewport' }),
    'tablet',
  );
  await waitFor(() =>
    expect(
      calls.filter((c) => c.key === 'GET /agents/a1/browser/screenshot'),
    ).toHaveLength(2),
  );
});

it('annotates a picked element and sends it to the composer', async () => {
  renderPane({
    ...noExposures,
    'GET /agents/a1/browser/status': page({ screenshot: undefined }),
    'GET /agents/a1/browser/screenshot': page(),
  });
  await screen.findByRole('img', { name: 'Screenshot of Todo' });
  await userEvent.click(screen.getByRole('button', { name: 'Annotate' }));
  const v = await viewport();
  // (60, 60) on screen is (120, 120) on the page: inside the button, which
  // is smaller than the form around it.
  fireEvent.mouseDown(v, { clientX: 60, clientY: 60, button: 0 });
  fireEvent.mouseUp(v, { clientX: 60, clientY: 60, button: 0 });
  const form = await screen.findByRole('group', { name: 'Annotation' });
  expect(form).toHaveTextContent('button “Save” · #save');
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Note' }),
    'Make this button aligned right',
  );
  await userEvent.click(
    screen.getByRole('button', { name: 'Send to composer' }),
  );
  const draft = getDraft('s1');
  expect(draft).toContain(
    'Annotation on http://localhost:3000/ ("Todo"), viewport 1280×800:',
  );
  expect(draft).toContain(
    '- element: button "Save", selector `#save`, 200×40 at (100, 100)',
  );
  expect(draft).toContain('Make this button aligned right');
  expect(screen.getByText(/Sent to the composer/)).toBeInTheDocument();
});

it('annotates a dragged region', async () => {
  renderPane(
    {
      ...noExposures,
      'GET /agents/a1/browser/status': page({ screenshot: undefined }),
      'GET /agents/a1/browser/screenshot': page(),
    },
    { composerKey: 's2' },
  );
  await screen.findByRole('img', { name: 'Screenshot of Todo' });
  await userEvent.click(screen.getByRole('button', { name: 'Annotate' }));
  const v = await viewport();
  fireEvent.mouseDown(v, { clientX: 300, clientY: 250, button: 0 });
  fireEvent.mouseMove(v, { clientX: 400, clientY: 300 });
  fireEvent.mouseUp(v, { clientX: 400, clientY: 300, button: 0 });
  expect(
    await screen.findByRole('group', { name: 'Annotation' }),
  ).toHaveTextContent('Region 200×100');
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Note' }),
    'Too much space',
  );
  await userEvent.keyboard('{Meta>}{Enter}{/Meta}');
  expect(getDraft('s2')).toContain('- region: 200×100 at (600, 500)');
});

it('explains a machine without Chromium and offers to start a stopped one', async () => {
  renderPane({
    ...noExposures,
    'GET /agents/a1/browser/status': page({ available: false, running: false }),
  });
  expect(
    await screen.findByText(/This machine has no headless Chromium/),
  ).toBeInTheDocument();
});

it('offers to start a stopped machine', async () => {
  const onStart = vi.fn();
  renderPane(
    {
      ...noExposures,
      'GET /agents/a1/browser/status': () => ({
        status: 409,
        body: { error: 'the machine is not running; start it first' },
      }),
    },
    { onStart },
  );
  await userEvent.click(
    await screen.findByRole('button', { name: 'Start the agent' }),
  );
  expect(onStart).toHaveBeenCalled();
});

it('shows only the live app to those who cannot use the agent', async () => {
  renderPane(noExposures, { canUse: false, composerKey: undefined });
  expect(
    await screen.findByText(/No ports are exposed yet/),
  ).toBeInTheDocument();
  expect(screen.queryByRole('tab', { name: /Sandbox browser/ })).toBeNull();
});

it('maps screen points, elements, keys and viewports', () => {
  expect(
    toViewport(
      150,
      125,
      { left: 50, top: 25, width: 640, height: 400 },
      { width: 1280, height: 800 },
    ),
  ).toEqual({ x: 200, y: 200 });
  expect(
    toViewport(
      9999,
      -5,
      { left: 0, top: 0, width: 640, height: 400 },
      { width: 1280, height: 800 },
    ),
  ).toEqual({ x: 1280, y: 0 });
  const els = page().elements;
  expect(elementAt(els, 150, 120)?.selector).toBe('#save');
  expect(elementAt(els, 900, 300)?.selector).toBe('form');
  expect(elementAt(els, 900, 700)).toBeUndefined();
  expect(spanRect({ x: 10, y: 50 }, { x: 4, y: 20 })).toEqual({
    x: 4,
    y: 20,
    width: 6,
    height: 30,
  });
  const k = (key: string, mods: Partial<Record<string, boolean>> = {}) =>
    keyAction({ key, ctrlKey: false, metaKey: false, altKey: false, ...mods });
  expect(k('a')).toEqual({ action: 'type', text: 'a' });
  expect(k('Enter')).toEqual({ action: 'press', key: 'Enter' });
  expect(k('a', { ctrlKey: true })).toEqual({
    action: 'press',
    key: 'Control+a',
  });
  expect(k('Shift')).toBeNull();
  expect(viewportSize('tablet', true)).toEqual({ width: 1024, height: 768 });
  expect(viewportSize('fill')).toBeNull();
  expect(
    formatContext({
      kind: 'browser',
      id: 'x',
      url: 'http://localhost:3000/',
      viewport: { width: 390, height: 844 },
      region: { x: 1.4, y: 2, width: 30, height: 40 },
      body: ' Fix ',
    }),
  ).toBe(
    'Annotation on http://localhost:3000/, viewport 390×844:\n- region: 30×40 at (1, 2)\nFix',
  );
});
