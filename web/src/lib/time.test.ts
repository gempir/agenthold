import { expect, it } from 'bun:test';
import { elapsed, relTime } from './time';

const now = new Date('2030-01-10T12:00:00Z').getTime();
const ago = (s: number) => new Date(now - s * 1000).toISOString();

it('formats compact relative times', () => {
  expect(relTime(ago(5), now)).toBe('now');
  expect(relTime(ago(7 * 60), now)).toBe('7m');
  expect(relTime(ago(3 * 3600), now)).toBe('3h');
  expect(relTime(ago(2 * 86400), now)).toBe('2d');
  expect(relTime(ago(15 * 86400), now)).toBe('2w');
  expect(relTime('nope', now)).toBe('');
  // Clock skew never yields negative times.
  expect(relTime(ago(-30), now)).toBe('now');
});

it('formats elapsed durations', () => {
  expect(elapsed(18_000)).toBe('18s');
  expect(elapsed(125_000)).toBe('2m 05s');
  expect(elapsed(3_720_000)).toBe('1h 02m');
});
