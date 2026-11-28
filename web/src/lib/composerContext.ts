import { useCallback, useSyncExternalStore } from 'react';

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

const EMPTY: ComposerContextChip[] = [];
const store = new Map<string, ComposerContextChip[]>();
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
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

export function removeComposerContext(sessionId: string, chipId: string) {
  const cur = store.get(sessionId);
  if (!cur) return;
  const next = cur.filter((c) => c.id !== chipId);
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
        const parts = [`[Review comment on ${loc}${side}]`];
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
