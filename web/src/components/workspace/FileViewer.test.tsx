import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, it, vi } from 'bun:test';
import type { TextFile } from '@/api/queries';
import { FileViewer, parseDelimited } from '@/components/workspace/FileViewer';
import { getDraft } from '@/lib/drafts';
import { mockApi } from '@/test-utils';

const file = (path: string, content: string, over: Partial<TextFile> = {}) => ({
  path,
  size: content.length,
  modTime: '2030-01-01T00:00:00Z',
  mimeType: 'text/plain',
  isBinary: false,
  truncated: false,
  content,
  ...over,
});

const enc = encodeURIComponent;

function renderViewer(
  path: string,
  routes: Record<string, unknown>,
  props: Partial<Parameters<typeof FileViewer>[0]> = {},
) {
  const api = mockApi(routes);
  const onOpenFile = vi.fn();
  render(
    <QueryClientProvider client={new QueryClient()}>
      <FileViewer
        agentId="a1"
        path={path}
        canUse
        composerKey="s1"
        onOpenFile={onOpenFile}
        {...props}
      />
    </QueryClientProvider>,
  );
  return { ...api, onOpenFile };
}

const GO = 'package main\n\nfunc main() {\n\tprintln("hi")\n}\n';

it('shows code with line numbers, highlighting and the linked line', async () => {
  renderViewer(
    'web/main.go',
    {
      [`GET /agents/a1/files/read?path=${enc('web/main.go')}`]: file(
        'web/main.go',
        GO,
      ),
    },
    { line: 3 },
  );
  const current = await screen.findByRole('row', { current: true });
  expect(current).toHaveTextContent('3func main() {');
  expect(within(current).getByText('func')).toHaveClass('text-violet-700');
  expect(screen.getAllByRole('button', { name: /^Line \d+$/ })).toHaveLength(5);
  expect(screen.getByRole('link', { name: 'Download' })).toHaveAttribute(
    'href',
    `/api/v1/agents/a1/files/content?path=${enc('/home/agent/web/main.go')}`,
  );
});

it('edits and saves with Cmd+S, showing unsaved changes', async () => {
  const { calls } = renderViewer('web/main.go', {
    [`GET /agents/a1/files/read?path=${enc('web/main.go')}`]: file(
      'web/main.go',
      GO,
    ),
    'PUT /agents/a1/files/write': file('web/main.go', '', { size: 60 }),
  });
  await userEvent.click(await screen.findByRole('button', { name: 'Edit' }));
  const editor = screen.getByRole('textbox', { name: 'Edit main.go' });
  expect(editor).toHaveValue(GO);
  expect(screen.queryByRole('img', { name: 'Unsaved changes' })).toBeNull();
  await userEvent.type(editor, '// end');
  expect(screen.getByRole('img', { name: 'Unsaved changes' })).toBeVisible();
  await userEvent.keyboard('{Meta>}s{/Meta}');
  await waitFor(() =>
    expect(
      calls.find((c) => c.key === 'PUT /agents/a1/files/write')?.body,
    ).toEqual({
      path: 'web/main.go',
      content: `${GO}// end`,
    }),
  );
  expect(await screen.findByText('Saved')).toBeVisible();
  expect(screen.queryByRole('img', { name: 'Unsaved changes' })).toBeNull();
  await userEvent.click(screen.getByRole('button', { name: 'Done' }));
  expect(await screen.findByText('// end')).toBeVisible();
});

it('is read-only for viewers and truncated files', async () => {
  renderViewer(
    'big.log',
    {
      'GET /agents/a1/files/read?path=big.log': file('big.log', 'a\nb', {
        truncated: true,
        size: 9_000_000,
      }),
    },
    { canUse: true },
  );
  expect(
    await screen.findByText(/Showing the start of a 8\.6 MB file/),
  ).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull();
});

it('renders Markdown with a raw toggle and opens linked files', async () => {
  const { onOpenFile } = renderViewer('docs/guide.md', {
    [`GET /agents/a1/files/read?path=${enc('docs/guide.md')}`]: file(
      'docs/guide.md',
      '# Guide\n\nSee [setup](setup.md:4).\n',
    ),
  });
  expect(await screen.findByRole('heading', { name: 'Guide' })).toBeVisible();
  await userEvent.click(screen.getByRole('link', { name: 'setup' }));
  // Relative to the document's directory.
  expect(onOpenFile).toHaveBeenCalledWith('docs/setup.md', 4);
  await userEvent.click(screen.getByRole('radio', { name: 'raw' }));
  expect(screen.queryByRole('heading', { name: 'Guide' })).toBeNull();
  expect(screen.getByText('# Guide')).toBeVisible();
});

it('renders CSV as a table and HTML in a sandboxed frame', async () => {
  renderViewer('data.csv', {
    'GET /agents/a1/files/read?path=data.csv': file(
      'data.csv',
      'name,note\nada,"likes, commas"\n',
    ),
  });
  expect(
    await screen.findByRole('columnheader', { name: 'note' }),
  ).toBeVisible();
  expect(screen.getByRole('cell', { name: 'likes, commas' })).toBeVisible();
  expect(parseDelimited('a\tb\n1\t"x\ny"', '\t')).toEqual([
    ['a', 'b'],
    ['1', 'x\ny'],
  ]);
});

it('previews HTML without scripts', async () => {
  renderViewer('site/index.html', {
    [`GET /agents/a1/files/read?path=${enc('site/index.html')}`]: file(
      'site/index.html',
      '<h1>Hi</h1>',
    ),
  });
  const frame = await screen.findByTitle('index.html preview');
  expect(frame).toHaveAttribute('sandbox', '');
  expect(frame).toHaveAttribute('srcdoc', '<h1>Hi</h1>');
});

it('shows images and PDFs from the file content', () => {
  const { calls } = renderViewer('shots/a.png', {});
  expect(screen.getByRole('img', { name: 'a.png' })).toHaveAttribute(
    'src',
    `/api/v1/agents/a1/files/content?path=${enc('/home/agent/shots/a.png')}`,
  );
  expect(calls).toHaveLength(0);
});

it('comments on lines for the composer', async () => {
  renderViewer('web/main.go', {
    [`GET /agents/a1/files/read?path=${enc('web/main.go')}`]: file(
      'web/main.go',
      GO,
    ),
  });
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: 'Line 3' }));
  await user.keyboard('{Shift>}');
  await user.click(screen.getByRole('button', { name: 'Line 5' }));
  await user.keyboard('{/Shift}');
  const box = screen.getByRole('group', { name: 'Review comment' });
  expect(box).toHaveTextContent('Lines 3-5');
  await user.type(within(box).getByRole('textbox'), 'Log instead');
  await user.click(
    within(box).getByRole('button', { name: 'Add to composer' }),
  );
  expect(getDraft('s1')).toBe(
    'Review comment on `web/main.go` lines 3-5 (file):\n```\nfunc main() {\n\tprintln("hi")\n}\n```\nLog instead\n',
  );
});
