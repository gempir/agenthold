import { expect, it } from 'vitest';
import { describeDetails, humanizeAction } from './audit';

it('humanizes known actions and falls back to the raw one', () => {
  expect(humanizeAction('session.prompt')).toBe('Prompted');
  expect(humanizeAction('terminal.open')).toBe('Opened a terminal');
  expect(humanizeAction('file.write')).toBe('Uploaded a file');
  expect(humanizeAction('grant.set')).toBe('Shared');
  expect(humanizeAction('weird.thing')).toBe('weird.thing');
});

it('describes prompt text truncated and grant details', () => {
  const long = 'x'.repeat(200);
  const d = describeDetails({
    action: 'session.prompt',
    details: { text: long },
  });
  expect(d.length).toBeLessThan(100);
  expect(d.endsWith('…')).toBe(true);
  expect(
    describeDetails({
      action: 'grant.set',
      details: { role: 'operator', subject: 'Bob' },
    }),
  ).toBe('role: operator, to: Bob');
});
