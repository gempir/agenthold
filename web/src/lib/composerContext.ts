import { useEffect, useSyncExternalStore } from 'react';
import { agentDraftKey, getDraft, setDraft } from '@/lib/drafts';

/**
 * Context the user attaches to a thread's next message from outside the
 * composer: review comments on diff and file lines, and annotations on
 * pages of the machine's browser.
 *
 * A rich composer (chips) registers itself with useComposerContext(key) and
 * gets the items to show; without one, attachToComposer appends the item as
 * text to the thread's draft so it still reaches the agent.
 */
export type ReviewComment = {
  kind: 'review';
  id: string;
  /** File path relative to the agent's home. */
  path: string;
  startLine: number;
  endLine: number;
  /** Which version a diff comment is on: "old" is the removed side. */
  side?: 'old' | 'new';
  /** Where it was made, e.g. "uncommitted diff", "commit 1a2b3c4", "file". */
  source: string;
  /** The lines commented on, as shown (diff lines with their +/- marker). */
  excerpt: string;
  body: string;
};

/** A box on a page, in viewport CSS pixels. */
export type PageRect = { x: number; y: number; width: number; height: number };

/**
 * A note on a page of the machine's browser: on an element the user picked
 * or a region they drew on its screenshot.
 */
export type BrowserAnnotation = {
  kind: 'browser';
  id: string;
  url: string;
  title?: string;
  viewport: { width: number; height: number };
  element?: { role: string; name: string; selector: string; rect: PageRect };
  region?: PageRect;
  body: string;
};

export type ComposerContextItem = ReviewComment | BrowserAnnotation;

/** The draft key of the composer a page's context goes to. */
export const composerKey = (agentId: string, sessionId?: string) =>
  sessionId || agentDraftKey(agentId);

export const CONTEXT_EVENT = 'agenthold:composer-context';

const pending = new Map<string, ComposerContextItem[]>();
const consumers = new Map<string, number>();
const listeners = new Set<() => void>();
const EMPTY: ComposerContextItem[] = [];
const notify = () => listeners.forEach((l) => l());

let seq = 0;
export const newContextId = () => `ctx-${Date.now().toString(36)}-${++seq}`;

const lineLabel = (c: ReviewComment) =>
  c.startLine === c.endLine
    ? `line ${c.startLine}`
    : `lines ${c.startLine}-${c.endLine}`;

const box = (r: PageRect) =>
  `${Math.round(r.width)}×${Math.round(r.height)} at (${Math.round(r.x)}, ${Math.round(r.y)})`;

function formatAnnotation(c: BrowserAnnotation): string {
  const page = c.title ? `${c.url} ("${c.title}")` : c.url;
  const lines = [
    `Annotation on ${page}, viewport ${c.viewport.width}×${c.viewport.height}:`,
  ];
  if (c.element) {
    const name = c.element.name ? ` "${c.element.name}"` : '';
    lines.push(
      `- element: ${c.element.role}${name}, selector \`${c.element.selector}\`, ${box(c.element.rect)}`,
    );
  }
  if (c.region) lines.push(`- region: ${box(c.region)}`);
  lines.push(c.body.trim());
  return lines.join('\n');
}

/** The text an item stands for in the prompt. */
export function formatContext(c: ComposerContextItem): string {
  if (c.kind === 'browser') return formatAnnotation(c);
  const where = `\`${c.path}\` ${lineLabel(c)}${c.side === 'old' ? ' (removed)' : ''}`;
  const lines = [`Review comment on ${where} (${c.source}):`];
  if (c.excerpt.trim()) {
    const fence = c.excerpt.includes('```') ? '````' : '```';
    lines.push(fence, c.excerpt.replace(/\n+$/, ''), fence);
  }
  lines.push(c.body.trim());
  return lines.join('\n');
}

/**
 * Attaches an item to the composer with this draft key. It returns whether a
 * rich composer took it (false: it went into the draft text).
 */
export function attachToComposer(
  key: string,
  item: ComposerContextItem,
): boolean {
  const rich = (consumers.get(key) ?? 0) > 0;
  if (rich) {
    pending.set(key, [...(pending.get(key) ?? []), item]);
    notify();
  } else {
    const draft = getDraft(key).replace(/\s+$/, '');
    setDraft(key, (draft ? `${draft}\n\n` : '') + formatContext(item) + '\n');
  }
  globalThis.dispatchEvent?.(
    new CustomEvent(CONTEXT_EVENT, { detail: { key, item, rich } }),
  );
  return rich;
}

/** Drops one attached item (id) or all of them (no id). */
export function removeComposerContext(key: string, id?: string) {
  const items = pending.get(key);
  if (!items) return;
  const next = id ? items.filter((i) => i.id !== id) : [];
  if (next.length) pending.set(key, next);
  else pending.delete(key);
  notify();
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
};

/**
 * For a composer that shows attached context as chips: registers it for the
 * key while mounted (so attachments stop going into the draft text) and
 * returns the attached items.
 */
export function useComposerContext(key: string): ComposerContextItem[] {
  useEffect(() => {
    consumers.set(key, (consumers.get(key) ?? 0) + 1);
    return () => {
      const n = (consumers.get(key) ?? 1) - 1;
      if (n > 0) consumers.set(key, n);
      else consumers.delete(key);
    };
  }, [key]);
  return useSyncExternalStore(subscribe, () => pending.get(key) ?? EMPTY);
}
