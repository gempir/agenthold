import { useState } from 'react';

/**
 * Threads whose turn finished while this view was open and that the user
 * has not opened since: the sidebar marks them "Done". openId is the thread
 * on screen, which never counts as unseen.
 */
export function useJustFinished(
  running: { id: string; running: boolean }[],
  openId?: string,
): Set<string> {
  const now = running
    .filter((s) => s.running)
    .map((s) => s.id)
    .sort()
    .join(' ');
  const [prev, setPrev] = useState(now);
  const [done, setDone] = useState<ReadonlySet<string>>(() => new Set());
  // Derived from the previous render, not an effect: see "Storing
  // information from previous renders" in the React docs.
  if (prev !== now) {
    const still = new Set(now.split(' '));
    const finished = prev.split(' ').filter((id) => id && !still.has(id));
    setPrev(now);
    if (finished.length) setDone((d) => new Set([...d, ...finished]));
  }
  if (openId && done.has(openId)) {
    const next = new Set(done);
    next.delete(openId);
    setDone(next);
  }
  return done as Set<string>;
}
