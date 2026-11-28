import { useContext, useEffect, useMemo, useState } from 'react';
import { QueryClientContext, useQuery } from '@tanstack/react-query';
import {
  Bot,
  FileCode2,
  Folder,
  MessageSquare,
  Sparkles,
  Terminal,
} from 'lucide-react';
import {
  agentSkillsQuery,
  fileSearchQuery,
  harnessesQuery,
  modelsQuery,
  skillsQuery,
} from '@/api/queries';
import { useChatIndex } from '@/hooks/useChatIndex';
import { cn } from '@/lib/utils';

export type TriggerKind = '@' | '$' | '/';

export interface ComposerTrigger {
  kind: TriggerKind;
  query: string;
  /** Inclusive start index of the trigger character in the composer value. */
  start: number;
  /** Exclusive end index (caret position). */
  end: number;
}

/**
 * Detects an active `@`, `$`, or `/` autocomplete trigger immediately before
 * the caret position.
 */
export function detectComposerTrigger(
  text: string,
  caret: number,
): ComposerTrigger | null {
  if (caret <= 0 || caret > text.length) return null;
  const before = text.slice(0, caret);
  // Find the start of the current whitespace-delimited token.
  let start = caret - 1;
  while (start > 0 && !/\s/.test(before[start - 1])) start--;
  const token = before.slice(start);
  if (!token) return null;
  const first = token[0];
  if (first === '@' || first === '$') {
    // Must be at start of string or preceded by whitespace.
    if (start > 0 && !/\s/.test(before[start - 1])) return null;
    const query = token.slice(1);
    return { kind: first, query, start, end: caret };
  }
  if (first === '/') {
    // Slash commands trigger only at the start of the input (or start of line).
    if (start > 0 && before[start - 1] !== '\n') return null;
    const query = token.slice(1);
    // Do not trigger if the query contains another slash (looks like a path).
    if (query.includes('/')) return null;
    return { kind: '/', query, start, end: caret };
  }
  return null;
}

export interface SlashCommandDef {
  name: string;
  usage: string;
  description: string;
  /** Takes an argument after a space instead of executing immediately on pick. */
  takesArg?: boolean;
}

export const SLASH_COMMANDS: SlashCommandDef[] = [
  {
    name: 'side',
    usage: '/side <question>',
    description: 'Start a side chat seeded with this thread’s context',
    takesArg: true,
  },
  {
    name: 'btw',
    usage: '/btw <question>',
    description: 'Ask a quick side question without interrupting the thread',
    takesArg: true,
  },
  {
    name: 'compact',
    usage: '/compact',
    description: 'Ask the agent to summarize and compact context',
  },
  {
    name: 'clear',
    usage: '/clear',
    description: 'Clear the current composer draft and attached context',
  },
  {
    name: 'goal',
    usage: '/goal <criteria>',
    description: 'Run toward a thorough goal with clear completion criteria',
    takesArg: true,
  },
  {
    name: 'model',
    usage: '/model',
    description: 'Open the model and harness picker',
  },
  {
    name: 'plan',
    usage: '/plan',
    description: 'Switch this thread to Plan permission mode',
  },
];

export type ParsedSlashCommand =
  | { command: 'side' | 'btw'; question: string }
  | { command: 'compact'; note: string }
  | { command: 'clear' }
  | { command: 'goal'; criteria: string }
  | { command: 'model' }
  | { command: 'plan' };

/** Parses a submitted slash command, or null if this is a normal prompt. */
export function parseSlashCommand(text: string): ParsedSlashCommand | null {
  const trimmed = text.trim();
  if (!trimmed.startsWith('/')) return null;
  const match = /^\/([a-zA-Z]+)(?:\s+([\s\S]*))?$/.exec(trimmed);
  if (!match) return null;
  const name = match[1].toLowerCase();
  const arg = (match[2] ?? '').trim();
  switch (name) {
    case 'side':
    case 'btw':
      return { command: name, question: arg };
    case 'compact':
      return { command: 'compact', note: arg };
    case 'clear':
      return arg ? null : { command: 'clear' };
    case 'goal':
      return { command: 'goal', criteria: arg };
    case 'model':
      return arg ? null : { command: 'model' };
    case 'plan':
      return arg ? null : { command: 'plan' };
    default:
      return null;
  }
}

export type ComposerMenuItem =
  | {
      id: string;
      kind: 'file';
      group: 'Files';
      path: string;
      name: string;
      isDir: boolean;
    }
  | {
      id: string;
      kind: 'thread';
      group: 'Threads';
      sessionId: string;
      agentId: string;
      title: string;
      detail?: string;
    }
  | {
      id: string;
      kind: 'model';
      group: 'Harnesses & Models';
      harness?: string;
      model?: string;
      label: string;
      detail?: string;
    }
  | {
      id: string;
      kind: 'skill';
      group: 'Skills';
      name: string;
      description: string;
    }
  | {
      id: string;
      kind: 'command';
      group: 'Commands';
      command: SlashCommandDef;
    };

function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return debounced;
}

const BUILTIN_HARNESS_HINTS: {
  id: string;
  harness: string;
  label: string;
  detail: string;
}[] = [
  {
    id: 'claude-code',
    harness: 'claude-code',
    label: 'Claude Code',
    detail: 'claude',
  },
  { id: 'codex', harness: 'codex', label: 'Codex', detail: 'openai' },
  {
    id: 'opencode',
    harness: 'opencode',
    label: 'OpenCode',
    detail: 'multi-provider',
  },
];

function useQueriedAtAndDollarItems({
  trigger,
  agentId,
  cwd,
  sessionId,
}: {
  trigger: ComposerTrigger;
  agentId?: string;
  cwd?: string;
  sessionId?: string;
}): { items: ComposerMenuItem[]; loading: boolean } {
  const debouncedQ = useDebounced(trigger.query, 100);
  const isAt = trigger.kind === '@';
  const isDollar = trigger.kind === '$';

  const files = useQuery({
    ...fileSearchQuery(agentId ?? '', debouncedQ, cwd, 8),
    enabled: isAt && !!agentId,
  });
  const harnesses = useQuery({
    ...harnessesQuery,
    enabled: isAt,
  });
  const models = useQuery({
    ...modelsQuery(agentId ?? ''),
    enabled: isAt && !!agentId,
  });
  const index = useChatIndex();
  const librarySkills = useQuery({
    ...skillsQuery,
    enabled: isDollar,
  });
  const agentSkills = useQuery({
    ...agentSkillsQuery(agentId ?? ''),
    enabled: isDollar && !!agentId,
  });

  const q = trigger.query.trim().toLowerCase();

  const items = useMemo<ComposerMenuItem[]>(() => {
    if (isAt) {
      const out: ComposerMenuItem[] = [];
      // 1. Workspace files
      for (const m of files.data?.results ?? []) {
        out.push({
          id: `file:${m.path}`,
          kind: 'file',
          group: 'Files',
          path: m.path,
          name: m.name,
          isDir: m.isDir,
        });
      }
      // 2. Threads
      const matchingThreads = index.chats
        .filter((c) => c.session.id !== sessionId)
        .filter((c) => {
          if (!q) return true;
          const hay =
            `${c.session.title} ${c.agent.name} ${c.session.git?.branch ?? ''} ${c.session.project?.name ?? ''}`.toLowerCase();
          return hay.includes(q);
        })
        .slice(0, 5);
      for (const c of matchingThreads) {
        out.push({
          id: `thread:${c.session.id}`,
          kind: 'thread',
          group: 'Threads',
          sessionId: c.session.id,
          agentId: c.agent.id,
          title: c.session.title || 'Untitled thread',
          detail: c.session.git?.branch || c.agent.name,
        });
      }
      // 3. Harnesses & Models
      const seenModels = new Set<string>();
      const harnessList = harnesses.data?.length
        ? harnesses.data.map((h) => ({
            id: h.id,
            harness: h.id,
            label: h.name,
            detail: h.id,
          }))
        : BUILTIN_HARNESS_HINTS;
      for (const h of harnessList) {
        const hay = `${h.id} ${h.label} ${h.detail}`.toLowerCase();
        if (q && !hay.includes(q)) continue;
        seenModels.add(`h:${h.id}`);
        out.push({
          id: `harness:${h.id}`,
          kind: 'model',
          group: 'Harnesses & Models',
          harness: h.harness,
          label: h.label,
          detail: `@${h.id}`,
        });
      }
      for (const m of models.data?.models ?? []) {
        const hay = `${m.id} ${m.name} ${m.group ?? ''}`.toLowerCase();
        if (q && !hay.includes(q)) continue;
        if (seenModels.has(`m:${m.id}`)) continue;
        seenModels.add(`m:${m.id}`);
        out.push({
          id: `model:${m.id}`,
          kind: 'model',
          group: 'Harnesses & Models',
          harness: models.data?.harness,
          model: m.id,
          label: m.name,
          detail: m.id,
        });
        if (out.filter((x) => x.kind === 'model').length >= 8) break;
      }
      return out;
    }

    if (isDollar) {
      const seen = new Set<string>();
      const combined = [
        ...(agentSkills.data ?? []),
        ...(librarySkills.data ?? []),
      ];
      const out: ComposerMenuItem[] = [];
      for (const s of combined) {
        if (seen.has(s.name)) continue;
        seen.add(s.name);
        const hay = `${s.name} ${s.description}`.toLowerCase();
        if (q && !hay.includes(q)) continue;
        out.push({
          id: `skill:${s.name}`,
          kind: 'skill',
          group: 'Skills',
          name: s.name,
          description: s.description,
        });
      }
      return out.slice(0, 12);
    }

    return [];
  }, [
    isAt,
    isDollar,
    files.data,
    index.chats,
    sessionId,
    q,
    harnesses.data,
    models.data,
    agentSkills.data,
    librarySkills.data,
  ]);

  const loading =
    (isAt && !!agentId && files.isFetching && items.length === 0) ||
    (isDollar &&
      (librarySkills.isFetching || agentSkills.isFetching) &&
      items.length === 0);

  return { items, loading };
}

function filterSlashCommands(query: string): ComposerMenuItem[] {
  const q = query.trim().toLowerCase();
  const matching = SLASH_COMMANDS.filter(
    (c) =>
      !q ||
      c.name.toLowerCase().includes(q) ||
      c.description.toLowerCase().includes(q),
  );
  if (q) {
    const score = (c: SlashCommandDef) => {
      const n = c.name.toLowerCase();
      if (n.startsWith(q)) return 0;
      if (n.includes(q)) return 1;
      return 2;
    };
    matching.sort((a, b) => score(a) - score(b));
  }
  return matching.map((command) => ({
    id: `cmd:${command.name}`,
    kind: 'command',
    group: 'Commands',
    command,
  }));
}

/**
 * Floating autocomplete menu above the composer for `@` (files, threads,
 * harnesses/models), `$` (skills), and `/` (commands).
 */
export function ComposerMenu({
  trigger,
  agentId,
  cwd,
  sessionId,
  activeIndex,
  onHoverIndex,
  onSelect,
  onItemsResolved,
}: {
  trigger: ComposerTrigger;
  agentId?: string;
  cwd?: string;
  sessionId?: string;
  activeIndex: number;
  onHoverIndex: (i: number) => void;
  onSelect: (item: ComposerMenuItem) => void;
  onItemsResolved: (items: ComposerMenuItem[]) => void;
}) {
  const hasQueryClient = !!useContext(QueryClientContext);

  if (trigger.kind === '/') {
    const items = filterSlashCommands(trigger.query);
    return (
      <ComposerMenuList
        trigger={trigger}
        items={items}
        loading={false}
        activeIndex={activeIndex}
        onHoverIndex={onHoverIndex}
        onSelect={onSelect}
        onItemsResolved={onItemsResolved}
      />
    );
  }

  if (!hasQueryClient) {
    const items: ComposerMenuItem[] =
      trigger.kind === '@'
        ? BUILTIN_HARNESS_HINTS.filter(
            (h) =>
              !trigger.query.trim() ||
              `${h.id} ${h.label}`
                .toLowerCase()
                .includes(trigger.query.trim().toLowerCase()),
          ).map((h) => ({
            id: `harness:${h.id}`,
            kind: 'model',
            group: 'Harnesses & Models',
            harness: h.harness,
            label: h.label,
            detail: `@${h.id}`,
          }))
        : [];
    return (
      <ComposerMenuList
        trigger={trigger}
        items={items}
        loading={false}
        activeIndex={activeIndex}
        onHoverIndex={onHoverIndex}
        onSelect={onSelect}
        onItemsResolved={onItemsResolved}
      />
    );
  }

  return (
    <QueriedComposerMenu
      trigger={trigger}
      agentId={agentId}
      cwd={cwd}
      sessionId={sessionId}
      activeIndex={activeIndex}
      onHoverIndex={onHoverIndex}
      onSelect={onSelect}
      onItemsResolved={onItemsResolved}
    />
  );
}

function QueriedComposerMenu({
  trigger,
  agentId,
  cwd,
  sessionId,
  activeIndex,
  onHoverIndex,
  onSelect,
  onItemsResolved,
}: {
  trigger: ComposerTrigger;
  agentId?: string;
  cwd?: string;
  sessionId?: string;
  activeIndex: number;
  onHoverIndex: (i: number) => void;
  onSelect: (item: ComposerMenuItem) => void;
  onItemsResolved: (items: ComposerMenuItem[]) => void;
}) {
  const { items, loading } = useQueriedAtAndDollarItems({
    trigger,
    agentId,
    cwd,
    sessionId,
  });
  return (
    <ComposerMenuList
      trigger={trigger}
      items={items}
      loading={loading}
      activeIndex={activeIndex}
      onHoverIndex={onHoverIndex}
      onSelect={onSelect}
      onItemsResolved={onItemsResolved}
    />
  );
}

function ComposerMenuList({
  trigger,
  items,
  loading,
  activeIndex,
  onHoverIndex,
  onSelect,
  onItemsResolved,
}: {
  trigger: ComposerTrigger;
  items: ComposerMenuItem[];
  loading: boolean;
  activeIndex: number;
  onHoverIndex: (i: number) => void;
  onSelect: (item: ComposerMenuItem) => void;
  onItemsResolved: (items: ComposerMenuItem[]) => void;
}) {
  useEffect(() => {
    onItemsResolved(items);
  }, [items, onItemsResolved]);

  const groups: {
    name: string;
    rows: { item: ComposerMenuItem; idx: number }[];
  }[] = [];
  items.forEach((item, idx) => {
    const last = groups[groups.length - 1];
    if (last && last.name === item.group) last.rows.push({ item, idx });
    else groups.push({ name: item.group, rows: [{ item, idx }] });
  });

  const emptyText =
    trigger.kind === '@'
      ? 'No matching files, threads, or models'
      : trigger.kind === '$'
        ? 'No matching skills'
        : 'No matching commands';

  return (
    <div
      role="listbox"
      aria-label={
        trigger.kind === '@'
          ? 'Context suggestions'
          : trigger.kind === '$'
            ? 'Skill suggestions'
            : 'Slash commands'
      }
      className="absolute right-0 bottom-full left-0 z-30 mb-2 max-h-72 overflow-y-auto rounded-xl border bg-card p-1.5 text-sm shadow-xl shadow-black/25"
    >
      {loading ? (
        <p className="px-2.5 py-2 text-xs text-muted-foreground">Searching…</p>
      ) : items.length === 0 ? (
        <p className="px-2.5 py-2 text-xs text-muted-foreground">{emptyText}</p>
      ) : (
        groups.map((g) => (
          <div key={g.name} className="mb-1 last:mb-0">
            <p className="px-2.5 pt-1.5 pb-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
              {g.name}
            </p>
            {g.rows.map(({ item, idx }) => {
              const selected = idx === activeIndex;
              return (
                <button
                  key={item.id}
                  type="button"
                  role="option"
                  aria-selected={selected}
                  onMouseMove={() => idx !== activeIndex && onHoverIndex(idx)}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => onSelect(item)}
                  className={cn(
                    'flex w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-left text-sm transition-colors',
                    selected
                      ? 'bg-accent text-foreground'
                      : 'text-foreground/90 hover:bg-accent/60',
                  )}
                >
                  {item.kind === 'file' ? (
                    <>
                      {item.isDir ? (
                        <Folder
                          className="size-3.5 shrink-0 text-muted-foreground"
                          aria-hidden
                        />
                      ) : (
                        <FileCode2
                          className="size-3.5 shrink-0 text-muted-foreground"
                          aria-hidden
                        />
                      )}
                      <span className="min-w-0 flex-1 truncate font-mono text-xs">
                        {item.path}
                      </span>
                    </>
                  ) : item.kind === 'thread' ? (
                    <>
                      <MessageSquare
                        className="size-3.5 shrink-0 text-muted-foreground"
                        aria-hidden
                      />
                      <span className="min-w-0 flex-1 truncate">
                        {item.title}
                      </span>
                      {item.detail && (
                        <span className="max-w-32 shrink-0 truncate font-mono text-[11px] text-muted-foreground">
                          {item.detail}
                        </span>
                      )}
                    </>
                  ) : item.kind === 'model' ? (
                    <>
                      <Bot
                        className="size-3.5 shrink-0 text-muted-foreground"
                        aria-hidden
                      />
                      <span className="min-w-0 flex-1 truncate">
                        {item.label}
                      </span>
                      {item.detail && (
                        <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
                          {item.detail}
                        </span>
                      )}
                    </>
                  ) : item.kind === 'skill' ? (
                    <>
                      <Sparkles
                        className="size-3.5 shrink-0 text-amber-500"
                        aria-hidden
                      />
                      <span className="shrink-0 font-mono text-xs font-medium">
                        ${item.name}
                      </span>
                      {item.description && (
                        <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
                          {item.description}
                        </span>
                      )}
                    </>
                  ) : (
                    <>
                      <Terminal
                        className="size-3.5 shrink-0 text-muted-foreground"
                        aria-hidden
                      />
                      <span className="shrink-0 font-mono text-xs font-medium">
                        {item.command.usage}
                      </span>
                      <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
                        {item.command.description}
                      </span>
                    </>
                  )}
                </button>
              );
            })}
          </div>
        ))
      )}
    </div>
  );
}
