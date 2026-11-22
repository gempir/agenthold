import { expect, it } from 'bun:test';
import { quotaFull, quotaText } from './roles';

it('describes the agent quota', () => {
  expect(quotaText({ used: 2, max: 2 })).toBe('2 of 2 agents used');
  expect(quotaText({ used: 0, max: 1 })).toBe('0 of 1 agent used');
  expect(quotaText({ used: 5 })).toBeUndefined();
  expect(quotaText(undefined)).toBeUndefined();
  expect(quotaFull({ used: 2, max: 2 })).toBe(true);
  expect(quotaFull({ used: 1, max: 2 })).toBe(false);
  expect(quotaFull({ used: 9 })).toBe(false);
});
