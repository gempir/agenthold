import { useCallback, useEffect, useSyncExternalStore } from 'react';
import { agentDraftKey, getDraft, setDraft } from '@/lib/drafts';

/**
 * Legacy/external review comment shape emitted by DiffPane and FileViewer.
 * When a rich composer is mounted for the key (via useComposerContext),
 * attachToComposer converts it into a rich `review_comment` chip; otherwise
 * it falls back to appending formatted text to the draft so comments are
 * never lost.
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

export type ComposerContextItem = ReviewComment;

export type ComposerContextChip =
  | {
      id: string;
      kind: 'file';
      path: string;
      startLine?: number;
      endLine?: number;
      comment?: string;
      snippet?: string;
      label?: string;
      size?: number;
    }
  | {
      id: string;
      kind: 'quote';
      text: string;
      comment?: string;
      source?: string;
    }
  | {
      id: string;
      kind: 'thread';
      sessionId: string;
      agentId?: string;
      title: string;
    }
  | {
      id: string;
      kind: 'model';
      harness?: string;
      model?: string;
      label: string;
    }
  | {
      id: string;
      kind: 'skill';
      name: string;
      description?: string;
    }
  | {
      id: string;
      kind: 'review_comment';
      path: string;
      startLine?: number;
      endLine?: number;
      comment: string;
      snippet?: string;
      side?: 'old' | 'new';
      source?: string;
    };

export type NewComposerContextChip =
  | (Omit<Extract<ComposerContextChip, { kind: 'file' }>, 'id'> & {
      id?: string;
    })
  | (Omit<Extract<ComposerContextChip, { kind: 'quote' }>, 'id'> & {
      id?: string;
    })
  | (Omit<Extract<ComposerContextChip, { kind: 'thread' }>, 'id'> & {
      id?: string;
    })
  | (Omit<Extract<ComposerContextChip, { kind: 'model' }>, 'id'> & {
      id?: string;
    })
  | (Omit<Extract<ComposerContextChip, { kind: 'skill' }>, 'id'> & {
      id?: string;
    })
  | (Omit<Extract<ComposerContextChip, { kind: 'review_comment' }>, 'id'> & {
      id?: string;
    });

/** The draft key of the composer a page's context goes to. */
export const composerKey = (agentId: string, sessionId?: string) =>
  sessionId || agentDraftKey(agentId);

export const CONTEXT_EVENT = 'agenthold:composer-context';

const EMPTY: ComposerContextChip[] = [];
const store = new Map<string, ComposerContextChip[]>();
const consumers = new Map<string, number>();
const listeners = new Set<() => void>();

let seq = 0;
export const newContextId = () => `ctx-${Date.now().toString(36)}-${++seq}`;

function emit() {
  for (const l of listeners) l();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

const lineLabel = (c: ReviewComment) =>
  c.startLine === c.endLine
    ? `line ${c.startLine}`
    : `lines ${c.startLine}-${c.endLine}`;

/** The text a review item stands for in the prompt. */
export function formatContext(c: ComposerContextItem): string {
  const where = `\`${c.path}\` ${lineLabel(c)}${c.side === 'old' ? ' (removed)' : ''}`;
  const lines = [`Review comment on ${where} (${c.source}):`];
  if (c.excerpt.trim()) {
    const fence = c.excerpt.includes('```') ? '````' : '```';
    lines.push(fence, c.excerpt.replace(/\n+$/, ''), fence);
  }
  lines.push(c.body.trim());
  return lines.join('\n');
}

export function getComposerContext(sessionId: string): ComposerContextChip[] {
  return store.get(sessionId) ?? EMPTY;
}

export function addComposerContext(
  sessionId: string,
  chip: NewComposerContextChip,
): ComposerContextChip {
  const full = {
    ...chip,
    id: chip.id ?? crypto.randomUUID(),
  } as ComposerContextChip;
  const cur = store.get(sessionId) ?? EMPTY;
  // Avoid exact duplicate file/thread/skill chips when picked again.
  const deduped = cur.filter((c) => {
    if (full.kind === 'file' && c.kind === 'file') {
      return !(
        c.path === full.path &&
        c.startLine === full.startLine &&
        c.endLine === full.endLine &&
        !c.comment &&
        !full.comment
      );
    }
    if (full.kind === 'thread' && c.kind === 'thread') {
      return c.sessionId !== full.sessionId;
    }
    if (full.kind === 'skill' && c.kind === 'skill') {
      return c.name !== full.name;
    }
    return true;
  });
  store.set(sessionId, [...deduped, full]);
  emit();
  return full;
}

/**
 * Attaches a review comment item to the composer for this key. Returns true
 * when a mounted rich composer accepted it as a chip, or false when it fell
 * back to appending formatted text to the draft.
 */
export function attachToComposer(
  key: string,
  item: ComposerContextItem,
): boolean {
  const rich = (consumers.get(key) ?? 0) > 0;
  if (rich) {
    addComposerContext(key, {
      id: item.id,
      kind: 'review_comment',
      path: item.path,
      startLine: item.startLine,
      endLine: item.endLine,
      side: item.side,
      source: item.source,
      snippet: item.excerpt.trim() || undefined,
      comment: item.body.trim(),
    });
  } else {
    const draft = getDraft(key).replace(/\s+$/, '');
    setDraft(key, (draft ? `${draft}\n\n` : '') + formatContext(item) + '\n');
  }
  globalThis.dispatchEvent?.(
    new CustomEvent(CONTEXT_EVENT, { detail: { key, item, rich } }),
  );
  return rich;
}

export function removeComposerContext(sessionId: string, chipId?: string) {
  const cur = store.get(sessionId);
  if (!cur) return;
  const next = chipId ? cur.filter((c) => c.id !== chipId) : [];
  if (next.length === 0) store.delete(sessionId);
  else store.set(sessionId, next);
  emit();
}

export function clearComposerContext(sessionId: string) {
  if (!store.has(sessionId)) return;
  store.delete(sessionId);
  emit();
}

export function useComposerContext(sessionId: string) {
  useEffect(() => {
    consumers.set(sessionId, (consumers.get(sessionId) ?? 0) + 1);
    return () => {
      const n = (consumers.get(sessionId) ?? 1) - 1;
      if (n > 0) consumers.set(sessionId, n);
      else consumers.delete(sessionId);
    };
  }, [sessionId]);
  const chips = useSyncExternalStore(subscribe, () =>
    getComposerContext(sessionId),
  );
  const add = useCallback(
    (chip: NewComposerContextChip) => addComposerContext(sessionId, chip),
    [sessionId],
  );
  const remove = useCallback(
    (id: string) => removeComposerContext(sessionId, id),
    [sessionId],
  );
  const clear = useCallback(() => clearComposerContext(sessionId), [sessionId]);
  return { chips, add, remove, clear };
}

function lineSuffix(startLine?: number, endLine?: number): string {
  if (!startLine) return '';
  if (endLine && endLine !== startLine) return `:${startLine}-${endLine}`;
  return `:${startLine}`;
}

/** Human-readable short label for a chip pill. */
export function chipLabel(chip: ComposerContextChip): string {
  switch (chip.kind) {
    case 'file': {
      const base =
        chip.label || chip.path.split('/').filter(Boolean).pop() || chip.path;
      return `${base}${lineSuffix(chip.startLine, chip.endLine)}`;
    }
    case 'review_comment': {
      const base = chip.path.split('/').filter(Boolean).pop() || chip.path;
      return `${base}${lineSuffix(chip.startLine, chip.endLine)}`;
    }
    case 'quote': {
      const flat = chip.text.replace(/\s+/g, ' ').trim();
      return flat.length > 36 ? `“${flat.slice(0, 35)}…”` : `“${flat}”`;
    }
    case 'thread':
      return chip.title || 'Thread';
    case 'model':
      return chip.label;
    case 'skill':
      return `$${chip.name}`;
  }
}

/**
 * Formats active context chips cleanly ahead of the prompt text so the agent
 * receives exact paths, line ranges, review comments, quotes, and thread refs.
 */
export function serializeComposerContext(
  chips: ComposerContextChip[],
  prompt: string,
): string {
  const trimmed = prompt.trim();
  if (chips.length === 0) return trimmed;

  const blocks: string[] = [];
  for (const chip of chips) {
    switch (chip.kind) {
      case 'file': {
        const loc = `${chip.path}${lineSuffix(chip.startLine, chip.endLine)}`;
        const parts = [`[File: ${loc}]`];
        if (chip.snippet) parts.push(`\`\`\`\n${chip.snippet}\n\`\`\``);
        if (chip.comment) parts.push(`Comment: ${chip.comment}`);
        blocks.push(parts.join('\n'));
        break;
      }
      case 'review_comment': {
        const loc = `${chip.path}${lineSuffix(chip.startLine, chip.endLine)}`;
        const side = chip.side ? ` (${chip.side})` : '';
        const src = chip.source ? ` (${chip.source})` : '';
        const parts = [`[Review comment on ${loc}${side}${src}]`];
        if (chip.snippet) parts.push(`\`\`\`\n${chip.snippet}\n\`\`\``);
        parts.push(chip.comment);
        blocks.push(parts.join('\n'));
        break;
      }
      case 'quote': {
        const header = chip.source
          ? `[Quoted from ${chip.source}]`
          : '[Quoted text]';
        const quoted = chip.text
          .split('\n')
          .map((line) => `> ${line}`)
          .join('\n');
        const parts = [header, quoted];
        if (chip.comment) parts.push(chip.comment);
        blocks.push(parts.join('\n'));
        break;
      }
      case 'thread': {
        blocks.push(
          `[Referenced thread: "${chip.title}" (session ${chip.sessionId})]`,
        );
        break;
      }
      case 'model': {
        blocks.push(`[Mentioned model: ${chip.label}]`);
        break;
      }
      case 'skill': {
        blocks.push(`$${chip.name}`);
        break;
      }
    }
  }

  const contextSection = blocks.join('\n\n');
  return trimmed ? `${contextSection}\n\n${trimmed}` : contextSection;
}
