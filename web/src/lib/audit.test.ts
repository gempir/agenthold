import { expect, it } from 'bun:test';
import { describeDetails, humanizeAction } from './audit';

it('humanizes known actions and falls back to the raw one', () => {
  expect(humanizeAction('session.prompt')).toBe('Prompted');
  expect(humanizeAction('terminal.open')).toBe('Opened a terminal');
  expect(humanizeAction('file.write')).toBe('Uploaded a file');
  expect(humanizeAction('share.add')).toBe('Shared');
  expect(humanizeAction('weird.thing')).toBe('weird.thing');
});

it('describes prompt text truncated and share details', () => {
  const long = 'x'.repeat(200);
  const d = describeDetails({
    action: 'session.prompt',
    details: { text: long },
  });
  expect(d.length).toBeLessThan(100);
  expect(d.endsWith('…')).toBe(true);
  expect(
    describeDetails({
      action: 'share.add',
      details: { userId: 'u2', email: 'bob@example.com' },
    }),
  ).toBe('bob@example.com');
  expect(
    describeDetails({ action: 'share.remove', details: { userId: 'u2' } }),
  ).toBe('u2');
});
