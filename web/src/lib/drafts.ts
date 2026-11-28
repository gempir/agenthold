import { useCallback, useSyncExternalStore } from 'react';

const PREFIX = 'agenthold.draft.';
const listeners = new Set<() => void>();
const memory = new Map<string, string>();

/** Draft key of the unsent first message of a new thread with an agent. */
export const agentDraftKey = (agentId: string) => `agent:${agentId}`;
/** Draft key of the home page's new thread box. */
export const NEW_DRAFT_KEY = 'new';

function read(key: string): string {
  try {
    return localStorage.getItem(PREFIX + key) ?? '';
  } catch {
    return memory.get(key) ?? '';
  }
}

export function setDraft(key: string, text: string) {
  try {
    if (text) localStorage.setItem(PREFIX + key, text);
    else localStorage.removeItem(PREFIX + key);
  } catch {
    if (text) memory.set(key, text);
    else memory.delete(key);
  }
  listeners.forEach((l) => l());
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  window.addEventListener('storage', l);
  return () => {
    listeners.delete(l);
    window.removeEventListener('storage', l);
  };
};

/** The persisted unsent text of one composer; survives switching threads and reloads. */
export function useDraft(key: string): [string, (v: string) => void] {
  const text = useSyncExternalStore(subscribe, () => read(key));
  const set = useCallback((v: string) => setDraft(key, v), [key]);
  return [text, set];
}

/** Whether the thread with this draft key has unsent text. */
export function useHasDraft(key: string): boolean {
  return useSyncExternalStore(subscribe, () => read(key).trim() !== '');
}
