import { useSyncExternalStore } from 'react';

/** The workspace tabs every panel has. */
export type BuiltinTab =
  'overview' | 'diff' | 'git' | 'files' | 'preview' | 'terminal' | 'activity';

/**
 * A right-panel tab: a built-in one, a workspace file ("file:<path>" with an
 * optional ":<line>", the path relative to the home) or another thread
 * ("thread:<sessionId>") shown beside the open one.
 */
export type PanelTab = BuiltinTab | `file:${string}` | `thread:${string}`;
export type DynamicTab = Exclude<PanelTab, BuiltinTab>;

const BUILTIN: readonly string[] = [
  'overview',
  'diff',
  'git',
  'files',
  'preview',
  'terminal',
  'activity',
];

export const isBuiltinTab = (v: unknown): v is BuiltinTab =>
  typeof v === 'string' && BUILTIN.includes(v);

export const isPanelTab = (v: unknown): v is PanelTab =>
  isBuiltinTab(v) ||
  (typeof v === 'string' && /^(file|thread):./.test(v) && v.length < 2048);

/** Tabs only those who may use the agent get. */
export const panelTabNeedsUse = (t: PanelTab) =>
  t === 'terminal' || t === 'activity';

export const fileTab = (path: string, line?: number): DynamicTab =>
  `file:${path}${line ? `:${line}` : ''}`;
export const threadTab = (sessionId: string): DynamicTab =>
  `thread:${sessionId}`;

export type ParsedTab =
  | { kind: 'builtin'; id: BuiltinTab }
  | { kind: 'file'; path: string; line?: number }
  | { kind: 'thread'; sessionId: string };

export function parseTab(t: PanelTab): ParsedTab {
  if (isBuiltinTab(t)) return { kind: 'builtin', id: t };
  if (t.startsWith('thread:')) return { kind: 'thread', sessionId: t.slice(7) };
  const rest = t.slice(5);
  const m = rest.match(/^(.*):(\d+)$/);
  return m
    ? { kind: 'file', path: m[1], line: Number(m[2]) }
    : { kind: 'file', path: rest };
}

/** Tabs that show the same thing: one file (at any line) or one thread. */
const sameTarget = (a: PanelTab, b: PanelTab) => {
  const x = parseTab(a);
  const y = parseTab(b);
  if (x.kind === 'file' && y.kind === 'file') return x.path === y.path;
  return a === b;
};

// The open dynamic tabs, per agent, kept across reloads. Parsed lists are
// cached by their stored text so the hook gets a stable value.
const KEY = (agentId: string) => `agenthold.panelTabs.${agentId}`;
const memory = new Map<string, string>();
const parsed = new Map<string, DynamicTab[]>();
const listeners = new Set<() => void>();
const EMPTY: DynamicTab[] = [];

function raw(agentId: string): string {
  try {
    return localStorage.getItem(KEY(agentId)) ?? '';
  } catch {
    return memory.get(agentId) ?? '';
  }
}

function load(agentId: string): DynamicTab[] {
  const text = raw(agentId);
  if (!text) return EMPTY;
  const hit = parsed.get(text);
  if (hit) return hit;
  let list: DynamicTab[] = EMPTY;
  try {
    const v: unknown = JSON.parse(text);
    if (Array.isArray(v))
      list = v.filter(
        (t): t is DynamicTab => isPanelTab(t) && !isBuiltinTab(t),
      );
  } catch {
    // a bad value: start empty
  }
  if (parsed.size > 50) parsed.clear();
  parsed.set(text, list);
  return list;
}

function save(agentId: string, list: DynamicTab[]) {
  const text = list.length ? JSON.stringify(list) : '';
  try {
    if (text) localStorage.setItem(KEY(agentId), text);
    else localStorage.removeItem(KEY(agentId));
  } catch {
    if (text) memory.set(agentId, text);
    else memory.delete(agentId);
  }
  listeners.forEach((l) => l());
}

const replaceAt = <T>(list: T[], i: number, v: T) =>
  list.map((x, k) => (k === i ? v : x));

/** Adds a tab (replacing one of the same file) and returns it. */
export function openPanelTab(agentId: string, tab: DynamicTab): DynamicTab {
  const list = load(agentId);
  const i = list.findIndex((t) => sameTarget(t, tab));
  if (i >= 0) {
    if (list[i] !== tab) save(agentId, replaceAt(list, i, tab));
  } else {
    // A handful is plenty; drop the oldest.
    save(agentId, [...list, tab].slice(-12));
  }
  return tab;
}

/**
 * Closes a tab and returns the tab to show if it was the active one: its
 * neighbour, or the given fallback.
 */
export function closePanelTab(
  agentId: string,
  tab: PanelTab,
  fallback: PanelTab = 'overview',
): PanelTab {
  const list = load(agentId);
  const i = list.findIndex((t) => sameTarget(t, tab));
  if (i < 0) return fallback;
  const next = list.filter((_, k) => k !== i);
  save(agentId, next);
  return next[i] ?? next[i - 1] ?? fallback;
}

/** Forgets every open tab of an agent (tests, and closing all). */
export function clearPanelTabs(agentId: string) {
  save(agentId, EMPTY);
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
};

/**
 * The open dynamic tabs of an agent's panel. The active tab is always among
 * them (a link straight to "panel=file:…" opens it).
 */
export function usePanelTabs(agentId: string, active?: PanelTab) {
  const list = useSyncExternalStore(subscribe, () => load(agentId));
  if (!active || isBuiltinTab(active) || list.includes(active)) return list;
  const i = list.findIndex((t) => sameTarget(t, active));
  return i >= 0 ? replaceAt(list, i, active) : [...list, active];
}

type ClickLike = {
  button: number;
  metaKey: boolean;
  ctrlKey: boolean;
  preventDefault: () => void;
};

/**
 * Click handlers for a link to a thread: a plain click opens it in the panel
 * beside this one, Cmd/Ctrl-click or a middle click in the main view. Without
 * handlers the link works as a plain link.
 */
export function threadLinkHandlers(
  sessionId: string,
  onPanel?: (id: string) => void,
  onMain?: (id: string) => void,
) {
  if (!onPanel && !onMain) return {};
  return {
    onClick: (e: ClickLike) => {
      if (e.button !== 0) return;
      if (e.metaKey || e.ctrlKey) {
        if (!onMain) return;
        e.preventDefault();
        onMain(sessionId);
      } else if (onPanel) {
        e.preventDefault();
        onPanel(sessionId);
      }
    },
    onAuxClick: (e: ClickLike) => {
      if (e.button !== 1 || !onMain) return;
      e.preventDefault();
      onMain(sessionId);
    },
  };
}
