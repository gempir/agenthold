import { beforeEach, expect, it } from 'bun:test';
import {
  addComposerContext,
  chipLabel,
  clearComposerContext,
  getComposerContext,
  removeComposerContext,
  serializeComposerContext,
} from './composerContext';

beforeEach(() => {
  clearComposerContext('s1');
  clearComposerContext('s2');
});

it('adds, deduplicates, removes, and clears chips per session', () => {
  const f1 = addComposerContext('s1', { kind: 'file', path: 'src/app.ts' });
  addComposerContext('s1', { kind: 'file', path: 'src/app.ts' });
  expect(getComposerContext('s1')).toHaveLength(1);
  expect(getComposerContext('s2')).toHaveLength(0);

  const q = addComposerContext('s1', {
    kind: 'quote',
    text: 'Consider memoizing this selector',
    comment: 'Please apply this',
    source: 'Assistant',
  });
  expect(getComposerContext('s1')).toHaveLength(2);
  expect(chipLabel(f1)).toBe('app.ts');
  expect(chipLabel(q)).toContain('Consider memoizing');

  removeComposerContext('s1', q.id);
  expect(getComposerContext('s1')).toHaveLength(1);

  clearComposerContext('s1');
  expect(getComposerContext('s1')).toHaveLength(0);
});

it('serializes review comments, files, quotes, and threads cleanly ahead of the prompt', () => {
  const out = serializeComposerContext(
    [
      {
        id: '1',
        kind: 'review_comment',
        path: 'internal/api/sessions.go',
        startLine: 42,
        endLine: 48,
        comment: 'Check error before dereferencing',
        snippet: 'x := *ptr',
      },
      {
        id: '2',
        kind: 'quote',
        text: 'Line A\nLine B',
        source: 'Assistant',
        comment: 'Expand on Line B',
      },
      {
        id: '3',
        kind: 'thread',
        sessionId: 's9',
        title: 'Auth refactor',
      },
    ],
    'Fix these issues',
  );
  expect(out).toContain('[Review comment on internal/api/sessions.go:42-48]');
  expect(out).toContain('Check error before dereferencing');
  expect(out).toContain('[Quoted from Assistant]');
  expect(out).toContain('> Line A\n> Line B');
  expect(out).toContain('Expand on Line B');
  expect(out).toContain('[Referenced thread: "Auth refactor" (session s9)]');
  expect(out.endsWith('Fix these issues')).toBe(true);
  expect(serializeComposerContext([], '  hello  ')).toBe('hello');
});
