import { useCallback, useSyncExternalStore } from 'react';

export type FollowUpMode = 'queue' | 'steer';

const KEY = 'agenthold.followUpMode';
const listeners = new Set<() => void>();
let memory: FollowUpMode = 'queue';

function read(): FollowUpMode {
  try {
    const v = localStorage.getItem(KEY);
    return v === 'steer' ? 'steer' : 'queue';
  } catch {
    return memory;
  }
}

export function setFollowUpMode(mode: FollowUpMode) {
  memory = mode;
  try {
    localStorage.setItem(KEY, mode);
  } catch {
    // ignore storage errors
  }
  for (const l of listeners) l();
}

function subscribe(l: () => void) {
  listeners.add(l);
  window.addEventListener('storage', l);
  return () => {
    listeners.delete(l);
    window.removeEventListener('storage', l);
  };
}

/** Per-client preference for Enter while a turn is running: queue (default) or steer. */
export function useFollowUpMode(): [FollowUpMode, (m: FollowUpMode) => void] {
  const mode = useSyncExternalStore(subscribe, read);
  const set = useCallback((m: FollowUpMode) => setFollowUpMode(m), []);
  return [mode, set];
}
