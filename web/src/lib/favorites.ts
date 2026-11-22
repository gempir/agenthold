import { useCallback, useSyncExternalStore } from 'react';

/** A starred model of a harness, with its name for when the list lacks it. */
export interface FavoriteModel {
  harness: string;
  model: string;
  name: string;
}

/*
 * The starred models live in this browser (agenthold has no per-user
 * preferences on the server); every picker on the page shares them, and
 * other tabs follow through the storage event. Without storage they last
 * for this page.
 */
const KEY = 'agenthold.favoriteModels';
const listeners = new Set<() => void>();
let memory = '[]';
let cache: { raw: string; list: FavoriteModel[] } = { raw: '[]', list: [] };

function readRaw(): string {
  try {
    return localStorage.getItem(KEY) ?? '[]';
  } catch {
    return memory;
  }
}

function parse(raw: string): FavoriteModel[] {
  try {
    const v: unknown = JSON.parse(raw);
    if (!Array.isArray(v)) return [];
    return v.filter(
      (f): f is FavoriteModel =>
        !!f &&
        typeof f.harness === 'string' &&
        typeof f.model === 'string' &&
        !!f.model &&
        typeof f.name === 'string',
    );
  } catch {
    return [];
  }
}

function snapshot(): FavoriteModel[] {
  const raw = readRaw();
  if (raw !== cache.raw) cache = { raw, list: parse(raw) };
  return cache.list;
}

function write(list: FavoriteModel[]) {
  memory = JSON.stringify(list);
  try {
    localStorage.setItem(KEY, memory);
  } catch {
    // storage unavailable: the stars last for this page only
  }
  listeners.forEach((l) => l());
}

function subscribe(l: () => void) {
  listeners.add(l);
  const onStorage = (e: StorageEvent) => {
    if (e.key === KEY || e.key === null) l();
  };
  window.addEventListener('storage', onStorage);
  return () => {
    listeners.delete(l);
    window.removeEventListener('storage', onStorage);
  };
}

export const isFavorite = (
  list: FavoriteModel[],
  harness: string,
  model: string,
) => list.some((f) => f.harness === harness && f.model === model);

/** The starred models, in the order they were starred, and a toggle. */
export function useFavoriteModels() {
  const list = useSyncExternalStore(subscribe, snapshot, snapshot);
  const toggle = useCallback((f: FavoriteModel) => {
    const cur = snapshot();
    write(
      isFavorite(cur, f.harness, f.model)
        ? cur.filter((x) => !(x.harness === f.harness && x.model === f.model))
        : [...cur, f],
    );
  }, []);
  return { favorites: list, toggle };
}
