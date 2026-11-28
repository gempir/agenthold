import { beforeEach, expect, it } from 'bun:test';
import {
  clearStashes,
  listStashes,
  MAX_STASHES,
  popStash,
  pushStash,
  removeStash,
} from './composerStash';

beforeEach(() => {
  localStorage.clear();
  clearStashes();
});

it('pushes, lists, pops, and removes stashed drafts in LIFO order', () => {
  expect(pushStash('   ')).toBeNull();
  const a = pushStash('first draft', 's1');
  const b = pushStash('second draft', 's1');
  expect(a).not.toBeNull();
  expect(b).not.toBeNull();
  expect(listStashes().map((s) => s.text)).toEqual([
    'second draft',
    'first draft',
  ]);

  expect(popStash()?.text).toBe('second draft');
  expect(listStashes().map((s) => s.text)).toEqual(['first draft']);

  removeStash(a!.id);
  expect(listStashes()).toEqual([]);
  expect(popStash()).toBeNull();
});

it('caps persisted stashes at 100 entries', () => {
  for (let i = 0; i < MAX_STASHES + 15; i++) {
    pushStash(`draft ${i}`);
  }
  const all = listStashes();
  expect(all).toHaveLength(MAX_STASHES);
  expect(all[0].text).toBe(`draft ${MAX_STASHES + 14}`);
  expect(all[all.length - 1].text).toBe('draft 15');
});
