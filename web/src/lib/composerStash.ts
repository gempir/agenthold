import { useSyncExternalStore } from 'react';

const KEY = 'agenthold.composerStashes';
export const MAX_STASHES = 100;

export interface ComposerStashEntry {
  id: string;
  text: string;
  createdAt: string;
  sessionId?: string;
}

const listeners = new Set<() => void>();
let cacheRaw: string | null = null;
let cacheParsed: ComposerStashEntry[] = [];
let memory: ComposerStashEntry[] = [];

function emit() {
  for (const l of listeners) l();
}

function read(): ComposerStashEntry[] {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw === cacheRaw) return cacheParsed;
    cacheRaw = raw;
    if (!raw) {
      cacheParsed = [];
      return cacheParsed;
    }
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) {
      cacheParsed = [];
      return cacheParsed;
    }
    cacheParsed = parsed.filter(
      (x): x is ComposerStashEntry =>
        !!x &&
        typeof x === 'object' &&
        typeof (x as ComposerStashEntry).id === 'string' &&
        typeof (x as ComposerStashEntry).text === 'string' &&
        typeof (x as ComposerStashEntry).createdAt === 'string',
    );
    return cacheParsed;
  } catch {
    return memory;
  }
}

function write(entries: ComposerStashEntry[]) {
  const trimmed = entries.slice(0, MAX_STASHES);
  memory = trimmed;
  try {
    const raw = JSON.stringify(trimmed);
    if (trimmed.length === 0) {
      localStorage.removeItem(KEY);
      cacheRaw = null;
      cacheParsed = [];
    } else {
      localStorage.setItem(KEY, raw);
      cacheRaw = raw;
      cacheParsed = trimmed;
    }
  } catch {
    cacheParsed = trimmed;
  }
  emit();
}

function subscribe(l: () => void) {
  listeners.add(l);
  window.addEventListener('storage', l);
  return () => {
    listeners.delete(l);
    window.removeEventListener('storage', l);
  };
}

export function listStashes(): ComposerStashEntry[] {
  return read();
}

export function pushStash(
  text: string,
  sessionId?: string,
): ComposerStashEntry | null {
  if (!text.trim()) return null;
  const entry: ComposerStashEntry = {
    id: crypto.randomUUID(),
    text,
    createdAt: new Date().toISOString(),
    ...(sessionId ? { sessionId } : {}),
  };
  write([entry, ...read()]);
  return entry;
}

export function popStash(): ComposerStashEntry | null {
  const cur = read();
  if (cur.length === 0) return null;
  const [latest, ...rest] = cur;
  write(rest);
  return latest;
}

export function removeStash(id: string) {
  write(read().filter((e) => e.id !== id));
}

export function clearStashes() {
  write([]);
}

export function useComposerStashes(): ComposerStashEntry[] {
  return useSyncExternalStore(subscribe, read);
}
