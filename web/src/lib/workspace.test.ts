import { renderHook, act } from '@testing-library/react';
import { expect, it } from 'bun:test';
import {
  clearPanelTabs,
  closePanelTab,
  fileTab,
  isPanelTab,
  openPanelTab,
  parseTab,
  threadTab,
  usePanelTabs,
} from '@/components/agent/panelTabs';
import {
  attachToComposer,
  formatContext,
  removeComposerContext,
  useComposerContext,
  type ReviewComment,
} from '@/lib/composerContext';
import { getDraft, setDraft } from '@/lib/drafts';
import { homeRel, parseFileRef, resolveFileRef } from '@/lib/files';
import { highlightLine, highlightLines, langOfPath } from '@/lib/highlight';
import { parsePatch, rowLine, splitRows } from '@/lib/patch';

it('parses file links agents write', () => {
  expect(parseFileRef('file:///home/agent/web/main.go#L42')).toEqual({
    path: '/home/agent/web/main.go',
    line: 42,
  });
  expect(parseFileRef('/home/agent/x.ts:12:3')).toEqual({
    path: '/home/agent/x.ts',
    line: 12,
  });
  expect(parseFileRef('src/app.tsx#L5-L9')).toEqual({
    path: 'src/app.tsx',
    line: 5,
  });
  expect(parseFileRef('./README.md')).toEqual({ path: './README.md' });
  expect(parseFileRef('main.go:42')).toEqual({ path: 'main.go', line: 42 });
  expect(parseFileRef('src/my%20file.go')).toEqual({ path: 'src/my file.go' });
  for (const href of [
    'https://example.com/a.go',
    'mailto:a@x.io',
    'javascript:alert(1)',
    'javascript:1',
    '#heading',
    'README',
    '',
    undefined,
    'src/',
  ])
    expect(parseFileRef(href)).toBeNull();
});

it('makes paths relative to the home and resolves them against a cwd', () => {
  expect(homeRel('/home/agent/web/a.go')).toBe('web/a.go');
  expect(homeRel('~/web//./a.go')).toBe('web/a.go');
  expect(homeRel('/home/agent')).toBe('');
  expect(homeRel('/etc/hosts')).toBe('/etc/hosts');
  expect(resolveFileRef('src/a.go', 'projects/web')).toBe(
    'projects/web/src/a.go',
  );
  expect(resolveFileRef('./a.go', 'web')).toBe('web/a.go');
  expect(resolveFileRef('/home/agent/x.md', 'web')).toBe('x.md');
  expect(resolveFileRef('a.go')).toBe('a.go');
});

it('parses a unified patch into numbered rows and side-by-side pairs', () => {
  const rows = parsePatch(
    [
      'diff --git a/a.go b/a.go',
      '--- a/a.go',
      '+++ b/a.go',
      '@@ -1,3 +1,3 @@ func main',
      ' package main',
      '-old line',
      '+new line',
      '+extra',
      ' end',
      '\\ No newline at end of file',
      '',
    ].join('\n'),
  );
  expect(rows.map((r) => r.kind)).toEqual([
    'hunk',
    'context',
    'del',
    'add',
    'add',
    'context',
  ]);
  expect(rows[1]).toMatchObject({ oldNo: 1, newNo: 1 });
  expect(rows[2]).toMatchObject({ oldNo: 2, text: 'old line' });
  expect(rows[4]).toMatchObject({ newNo: 3, text: 'extra' });
  expect(rows[5]).toMatchObject({ oldNo: 3, newNo: 4 });
  expect(rowLine(rows[2])).toEqual({ side: 'old', line: 2 });
  expect(rowLine(rows[3])).toEqual({ side: 'new', line: 2 });

  const split = splitRows(rows);
  expect(split).toHaveLength(5);
  expect(split[2]).toMatchObject({
    kind: 'pair',
    leftIndex: 2,
    rightIndex: 3,
  });
  expect(split[3]).toMatchObject({ kind: 'pair', rightIndex: 4 });
  expect(split[3].kind === 'pair' && split[3].left).toBeFalsy();
});

it('highlights keywords, strings, numbers and block comments', () => {
  expect(langOfPath('web/src/main.tsx')).toBe('js');
  expect(langOfPath('Dockerfile')).toBe('sh');
  expect(langOfPath('notes.txt')).toBeNull();
  const t = highlightLine('const x = "hi" + 42; // done', 'js');
  expect(t.find((x) => x.kind === 'keyword')?.text).toBe('const');
  expect(t.find((x) => x.kind === 'string')?.text).toBe('"hi"');
  expect(t.find((x) => x.kind === 'number')?.text).toBe('42');
  expect(t.at(-1)).toEqual({ kind: 'comment', text: '// done' });
  const lines = highlightLines('a /* start\nmiddle\nend */ b', 'go');
  expect(lines[1]).toEqual([{ kind: 'comment', text: 'middle' }]);
  expect(lines[2][0]).toEqual({ kind: 'comment', text: 'end */' });
  expect(highlightLine('plain', null)).toEqual([
    { kind: 'plain', text: 'plain' },
  ]);
});

const comment = (over: Partial<ReviewComment> = {}): ReviewComment => ({
  kind: 'review',
  id: 'c1',
  path: 'web/a.go',
  startLine: 3,
  endLine: 4,
  side: 'new',
  source: 'uncommitted diff',
  excerpt: '+a\n+b',
  body: 'Rename this.',
  ...over,
});

it('appends review comments to the draft while no rich composer is mounted', () => {
  setDraft('s1', 'Please also');
  const events: unknown[] = [];
  const on = (e: Event) => events.push((e as CustomEvent).detail);
  window.addEventListener('agenthold:composer-context', on);
  expect(attachToComposer('s1', comment())).toBe(false);
  window.removeEventListener('agenthold:composer-context', on);
  expect(getDraft('s1')).toBe(
    'Please also\n\nReview comment on `web/a.go` lines 3-4 (uncommitted diff):\n```\n+a\n+b\n```\nRename this.\n',
  );
  expect(events).toHaveLength(1);
  expect(
    formatContext(comment({ startLine: 7, endLine: 7, side: 'old' })),
  ).toContain('line 7 (removed)');
});

it('hands review comments to a mounted rich composer instead', () => {
  const { result, unmount } = renderHook(() => useComposerContext('s2'));
  expect(result.current.chips).toEqual([]);
  act(() => {
    expect(attachToComposer('s2', comment({ id: 'x' }))).toBe(true);
  });
  expect(result.current.chips.map((c) => c.id)).toEqual(['x']);
  expect(getDraft('s2')).toBe('');
  act(() => removeComposerContext('s2', 'x'));
  expect(result.current.chips).toEqual([]);
  unmount();
  // Unmounted: back to the draft.
  expect(attachToComposer('s2', comment())).toBe(false);
  expect(getDraft('s2')).toContain('Rename this.');
});

it('keeps the open file and thread tabs of an agent', () => {
  clearPanelTabs('t1');
  expect(isPanelTab('diff')).toBe(true);
  expect(isPanelTab('file:a.go:3')).toBe(true);
  expect(isPanelTab('thread:s9')).toBe(true);
  expect(isPanelTab('file:')).toBe(false);
  expect(isPanelTab('nope')).toBe(false);
  expect(parseTab('file:web/a.go:12')).toEqual({
    kind: 'file',
    path: 'web/a.go',
    line: 12,
  });
  expect(parseTab('thread:s2')).toEqual({ kind: 'thread', sessionId: 's2' });

  const { result } = renderHook(() => usePanelTabs('t1', fileTab('web/b.go')));
  // The active tab is shown even before it was opened.
  expect(result.current).toEqual(['file:web/b.go']);
  act(() => {
    openPanelTab('t1', fileTab('web/a.go'));
    openPanelTab('t1', threadTab('s2'));
    // The same file at another line replaces its tab.
    openPanelTab('t1', fileTab('web/a.go', 9));
  });
  expect(result.current).toEqual([
    'file:web/a.go:9',
    'thread:s2',
    'file:web/b.go',
  ]);
  let next = 'overview';
  act(() => {
    next = closePanelTab('t1', 'file:web/a.go:9');
  });
  expect(next).toBe('thread:s2');
  act(() => {
    next = closePanelTab('t1', 'thread:s2', 'files');
  });
  expect(next).toBe('files');
  clearPanelTabs('t1');
});
