import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type DragEvent,
  type ReactNode,
} from 'react';
import {
  Archive,
  ArrowUp,
  Bot,
  FileCode2,
  FileText,
  ListPlus,
  MessageSquare,
  MessageSquareQuote,
  Paperclip,
  Sparkles,
  Square,
  Trash2,
  X,
  Zap,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import {
  LARGE_PASTE_BYTES,
  utf8ByteLength,
  type Attachment,
  type FileAttachment,
} from '@/lib/attachments';
import {
  chipLabel,
  type ComposerContextChip,
  type NewComposerContextChip,
} from '@/lib/composerContext';
import {
  popStash,
  pushStash,
  removeStash,
  useComposerStashes,
} from '@/lib/composerStash';
import { useFollowUpMode } from '@/lib/followUpPref';
import { formatSize } from '@/lib/files';
import {
  ComposerMenu,
  detectComposerTrigger,
  type ComposerMenuItem,
} from '@/components/chat/ComposerMenu';
import { Lightbox } from '@/components/chat/Lightbox';

const MAX_HEIGHT = 240;

export const DEFAULT_COMPOSER_PLACEHOLDER =
  'Ask anything. @ files, $ skills, / commands';

export interface ComposerSubmitOptions {
  mode: 'send' | 'queue' | 'steer';
}

function ChipIcon({ kind }: { kind: ComposerContextChip['kind'] }) {
  switch (kind) {
    case 'file':
    case 'review_comment':
      return <FileCode2 className="size-3 shrink-0" aria-hidden />;
    case 'quote':
      return <MessageSquareQuote className="size-3 shrink-0" aria-hidden />;
    case 'thread':
      return <MessageSquare className="size-3 shrink-0" aria-hidden />;
    case 'model':
      return <Bot className="size-3 shrink-0" aria-hidden />;
    case 'skill':
      return (
        <Sparkles className="size-3 shrink-0 text-amber-500" aria-hidden />
      );
  }
}

/**
 * The large rounded message box pinned under a conversation.
 * - Never blocks sending while running: Enter queues (or steers per preference),
 *   Cmd/Ctrl+Enter triggers the opposite follow-up action.
 * - Supports `@` / `$` / `/` floating autocomplete menus and inline context chips.
 * - Supports general file attachments (picker, drag-and-drop, clipboard paste)
 *   plus automatic >=32 KiB large-paste-to-file conversion (Cmd/Ctrl+Shift+V
 *   pastes inline).
 * - Supports ArrowUp/ArrowDown sent-prompt recall, Option/Alt+Up to edit latest
 *   queued message, Cmd/Ctrl+S prompt stash/restore, and Cmd/Ctrl+Enter
 *   background start on drafts.
 */
export function Composer({
  value,
  onChange,
  onSubmit,
  onBackgroundSubmit,
  onStop,
  running = false,
  busy = false,
  disabled = false,
  placeholder = DEFAULT_COMPOSER_PLACEHOLDER,
  autoFocus,
  meta,
  className,
  agentId,
  cwd,
  sessionId,
  attachments = [],
  fileAttachments = [],
  contextChips = [],
  attachError,
  onAttach,
  onRemoveAttachment,
  onRemoveFileAttachment,
  onLargePaste,
  onInlineLargePaste,
  onAddContextChip,
  onRemoveContextChip,
  onSlashCommand,
  onEditLatestQueued,
  sentHistory = [],
}: {
  value: string;
  onChange: (v: string) => void;
  onSubmit: (opts?: ComposerSubmitOptions) => void;
  /** Called on Cmd/Ctrl+Enter when idle (used by DraftChat for background start). */
  onBackgroundSubmit?: () => void;
  onStop?: () => void;
  running?: boolean;
  /** A send or stop request is in flight. */
  busy?: boolean;
  disabled?: boolean;
  placeholder?: string;
  autoFocus?: boolean;
  /** Shown at the lower left, e.g. the harness, model, and permission mode. */
  meta?: ReactNode;
  className?: string;
  agentId?: string;
  cwd?: string;
  sessionId?: string;
  /** Images waiting to be sent. */
  attachments?: Attachment[];
  /** General non-image files waiting to be uploaded on send. */
  fileAttachments?: FileAttachment[];
  /** Inline context chips (files, diff comments, quotes, threads, models). */
  contextChips?: ComposerContextChip[];
  attachError?: string | null;
  onAttach?: (files: File[]) => void;
  onRemoveAttachment?: (id: string) => void;
  onRemoveFileAttachment?: (id: string) => void;
  onLargePaste?: (text: string) => void;
  onInlineLargePaste?: (file: FileAttachment) => void;
  onAddContextChip?: (chip: NewComposerContextChip) => void;
  onRemoveContextChip?: (id: string) => void;
  /** Immediate execution for zero-arg slash commands picked from `/` menu. */
  onSlashCommand?: (command: string) => void;
  /** Option/Alt+ArrowUp handler to pull the latest queued message into the composer. */
  onEditLatestQueued?: () => void;
  /** Previously sent user prompts (oldest first) for empty-composer ArrowUp recall. */
  sentHistory?: string[];
}) {
  const box = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  const shiftPasteRef = useRef(false);
  const [viewing, setViewing] = useState<number | null>(null);
  const [dragging, setDragging] = useState(false);
  const [followUpMode, setFollowUpMode] = useFollowUpMode();

  // Autocomplete menu state
  const [caret, setCaret] = useState(0);
  const [dismissedKey, setDismissedKey] = useState<string | null>(null);
  const [menuIndex, setMenuIndex] = useState(0);
  const menuItemsRef = useRef<ComposerMenuItem[]>([]);

  // Prompt recall state (null = not recalling, number = index into sentHistory)
  const [recallIndex, setRecallIndex] = useState<number | null>(null);

  // Stash picker popover & transient feedback
  const stashes = useComposerStashes();
  const [stashOpen, setStashOpen] = useState(false);
  const [stashNotice, setStashNotice] = useState<string | null>(null);
  const stashMenuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!stashOpen) return;
    const onDown = (e: MouseEvent) => {
      if (!stashMenuRef.current?.contains(e.target as Node))
        setStashOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setStashOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [stashOpen]);

  useEffect(() => {
    if (!stashNotice) return;
    const t = setTimeout(() => setStashNotice(null), 1800);
    return () => clearTimeout(t);
  }, [stashNotice]);

  const rawTrigger = detectComposerTrigger(value, caret);
  const triggerKey = rawTrigger
    ? `${rawTrigger.kind}:${rawTrigger.start}:${rawTrigger.query}`
    : null;
  const trigger = rawTrigger && dismissedKey !== triggerKey ? rawTrigger : null;

  const hasContent =
    !!value.trim() ||
    attachments.length > 0 ||
    fileAttachments.length > 0 ||
    contextChips.length > 0;
  // NEVER block sending while running!
  const canSend = hasContent && !busy && !disabled;

  const handleItemsResolved = useCallback((items: ComposerMenuItem[]) => {
    menuItemsRef.current = items;
    setMenuIndex((cur) =>
      items.length === 0 ? 0 : Math.min(cur, items.length - 1),
    );
  }, []);

  const syncCaret = () => {
    const el = box.current;
    if (el) setCaret(el.selectionStart ?? el.value.length);
  };

  const replaceTrigger = (replacement: string) => {
    if (!trigger) return;
    const next =
      value.slice(0, trigger.start) + replacement + value.slice(trigger.end);
    const nextCaret = trigger.start + replacement.length;
    onChange(next);
    setCaret(nextCaret);
    setDismissedKey(null);
    setMenuIndex(0);
    requestAnimationFrame(() => {
      const el = box.current;
      if (el) {
        el.focus();
        el.setSelectionRange(nextCaret, nextCaret);
      }
    });
  };

  const selectMenuItem = (item: ComposerMenuItem) => {
    if (!trigger) return;
    switch (item.kind) {
      case 'file':
        if (onAddContextChip) {
          onAddContextChip({ kind: 'file', path: item.path });
          replaceTrigger('');
        } else {
          replaceTrigger(`@${item.path} `);
        }
        return;
      case 'thread':
        if (onAddContextChip) {
          onAddContextChip({
            kind: 'thread',
            sessionId: item.sessionId,
            agentId: item.agentId,
            title: item.title,
          });
          replaceTrigger('');
        } else {
          replaceTrigger(`@${item.title} `);
        }
        return;
      case 'model':
        if (onAddContextChip) {
          onAddContextChip({
            kind: 'model',
            harness: item.harness,
            model: item.model,
            label: item.label,
          });
          replaceTrigger('');
        } else {
          replaceTrigger(`@${item.model || item.harness || item.label} `);
        }
        return;
      case 'skill':
        replaceTrigger(`$${item.name} `);
        return;
      case 'command':
        if (item.command.takesArg) {
          replaceTrigger(`/${item.command.name} `);
        } else if (onSlashCommand) {
          replaceTrigger('');
          onSlashCommand(item.command.name);
        } else {
          replaceTrigger(`/${item.command.name}`);
        }
        return;
    }
  };

  // Grow with the text up to a cap, then scroll.
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${Math.min(el.scrollHeight, MAX_HEIGHT)}px`;
  }, [value]);

  const submitPrimary = () => {
    if (!canSend) return;
    setRecallIndex(null);
    onSubmit({ mode: running ? followUpMode : 'send' });
  };

  const submitOpposite = () => {
    if (!canSend) return;
    setRecallIndex(null);
    if (running) {
      onSubmit({ mode: followUpMode === 'queue' ? 'steer' : 'queue' });
    } else if (onBackgroundSubmit) {
      onBackgroundSubmit();
    } else {
      onSubmit({ mode: 'send' });
    }
  };

  const handleDragOver = (e: DragEvent<HTMLFormElement>) => {
    if (!onAttach || disabled) return;
    if (e.dataTransfer.types.includes('Files')) {
      e.preventDefault();
      if (!dragging) setDragging(true);
    }
  };

  const handleDragLeave = (e: DragEvent<HTMLFormElement>) => {
    if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
      setDragging(false);
    }
  };

  const handleDrop = (e: DragEvent<HTMLFormElement>) => {
    setDragging(false);
    if (!onAttach || disabled) return;
    const dropped = Array.from(e.dataTransfer.files);
    if (dropped.length > 0) {
      e.preventDefault();
      onAttach(dropped);
    }
  };

  const hasChipsRow = contextChips.length > 0 || fileAttachments.length > 0;

  return (
    <form
      className={cn(
        'relative rounded-2xl border bg-card shadow-lg shadow-black/10 transition-colors focus-within:border-foreground/20',
        dragging && 'border-primary/60 ring-2 ring-primary/30',
        className,
      )}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
      onSubmit={(e) => {
        e.preventDefault();
        submitPrimary();
      }}
      onClick={(e) => {
        if (e.target === e.currentTarget) box.current?.focus();
      }}
    >
      {trigger && (
        <ComposerMenu
          trigger={trigger}
          agentId={agentId}
          cwd={cwd}
          sessionId={sessionId}
          activeIndex={menuIndex}
          onHoverIndex={setMenuIndex}
          onSelect={selectMenuItem}
          onItemsResolved={handleItemsResolved}
        />
      )}

      {hasChipsRow && (
        <ul
          aria-label="Attached context"
          className="flex flex-wrap gap-1.5 px-3 pt-3 pr-16"
        >
          {contextChips.map((chip) => (
            <li
              key={chip.id}
              data-testid="composer-context-chip"
              title={
                chip.kind === 'quote'
                  ? chip.comment
                    ? `${chip.text}\n— ${chip.comment}`
                    : chip.text
                  : chip.kind === 'review_comment'
                    ? `${chip.path}: ${chip.comment}`
                    : chip.kind === 'file'
                      ? chip.path
                      : undefined
              }
              className="inline-flex max-w-64 items-center gap-1.5 rounded-lg border bg-muted/60 px-2 py-1 text-xs text-foreground"
            >
              <ChipIcon kind={chip.kind} />
              <span className="min-w-0 truncate">{chipLabel(chip)}</span>
              {chip.kind === 'review_comment' && (
                <span className="max-w-28 truncate text-[11px] text-muted-foreground">
                  · {chip.comment}
                </span>
              )}
              {chip.kind === 'quote' && chip.comment && (
                <span className="max-w-24 truncate text-[11px] text-muted-foreground">
                  · {chip.comment}
                </span>
              )}
              {onRemoveContextChip && (
                <button
                  type="button"
                  aria-label={`Remove ${chipLabel(chip)}`}
                  onClick={() => onRemoveContextChip(chip.id)}
                  className="ml-0.5 flex size-4 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
                >
                  <X className="size-3" />
                </button>
              )}
            </li>
          ))}
          {fileAttachments.map((f) => (
            <li
              key={f.id}
              data-testid="composer-file-chip"
              className="inline-flex max-w-72 items-center gap-1.5 rounded-lg border bg-muted/60 px-2 py-1 text-xs text-foreground"
            >
              <FileText
                className="size-3 shrink-0 text-muted-foreground"
                aria-hidden
              />
              <span className="min-w-0 truncate font-mono text-[11px]">
                {f.name}
              </span>
              <span className="shrink-0 text-[10px] text-muted-foreground">
                {formatSize(f.size)}
              </span>
              {f.fromLargePaste && onInlineLargePaste && (
                <button
                  type="button"
                  onClick={() => onInlineLargePaste(f)}
                  className="shrink-0 rounded px-1 text-[10px] text-muted-foreground hover:bg-accent hover:text-foreground"
                >
                  Inline
                </button>
              )}
              {onRemoveFileAttachment && (
                <button
                  type="button"
                  aria-label={`Remove ${f.name}`}
                  onClick={() => onRemoveFileAttachment(f.id)}
                  className="ml-0.5 flex size-4 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
                >
                  <X className="size-3" />
                </button>
              )}
            </li>
          ))}
        </ul>
      )}

      {attachments.length > 0 && (
        <ul className="flex flex-wrap gap-2 px-3 pt-3">
          {attachments.map((a, i) => (
            <li key={a.id} className="group relative">
              <button
                type="button"
                aria-label={`View ${a.name}`}
                onClick={() => setViewing(i)}
                className="block cursor-zoom-in rounded-lg focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
              >
                <img
                  src={a.previewUrl}
                  alt={a.name}
                  className="size-16 rounded-lg border object-cover"
                />
              </button>
              <button
                type="button"
                aria-label={`Remove ${a.name}`}
                title="Remove"
                onClick={() => onRemoveAttachment?.(a.id)}
                className="absolute -top-1.5 -right-1.5 flex size-5 items-center justify-center rounded-full bg-foreground text-background shadow transition-opacity hover:opacity-90"
              >
                <X className="size-3" />
              </button>
            </li>
          ))}
        </ul>
      )}

      <div className="absolute top-2 right-2 flex items-center gap-0.5">
        {stashes.length > 0 && (
          <div ref={stashMenuRef} className="relative">
            <button
              type="button"
              aria-label={`Stashed prompts (${stashes.length})`}
              title="Stashed prompts (Cmd/Ctrl+S)"
              disabled={disabled}
              onClick={() => setStashOpen((o) => !o)}
              className="flex h-8 items-center gap-1 rounded-full px-2 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:opacity-50"
            >
              <Archive className="size-3.5" aria-hidden />
              <span className="tabular-nums text-[11px]">{stashes.length}</span>
            </button>
            {stashOpen && (
              <div
                role="dialog"
                aria-label="Stashed prompts"
                className="absolute right-0 bottom-full z-40 mb-2 w-72 max-w-[85vw] rounded-xl border bg-card p-1.5 text-xs shadow-xl shadow-black/25"
              >
                <p className="px-2 py-1 text-[11px] font-medium text-muted-foreground">
                  Stashed drafts ({stashes.length})
                </p>
                <ul className="max-h-56 overflow-y-auto">
                  {stashes.map((s) => (
                    <li
                      key={s.id}
                      className="flex items-start gap-1 rounded-lg p-1 hover:bg-accent/60"
                    >
                      <button
                        type="button"
                        onClick={() => {
                          onChange(s.text);
                          removeStash(s.id);
                          setStashOpen(false);
                          box.current?.focus();
                        }}
                        className="min-w-0 flex-1 px-1 py-0.5 text-left"
                      >
                        <span className="line-clamp-2 whitespace-pre-wrap text-foreground">
                          {s.text}
                        </span>
                      </button>
                      <button
                        type="button"
                        aria-label="Delete stash"
                        onClick={() => removeStash(s.id)}
                        className="flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-destructive"
                      >
                        <Trash2 className="size-3" />
                      </button>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}
        {onAttach && (
          <>
            <input
              ref={picker}
              type="file"
              multiple
              hidden
              aria-label="Choose files to attach"
              onChange={(e) => {
                onAttach(Array.from(e.target.files ?? []));
                e.target.value = '';
              }}
            />
            <button
              type="button"
              aria-label="Attach files"
              title="Attach files or images"
              disabled={disabled}
              onClick={() => picker.current?.click()}
              className="flex size-8 items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:opacity-50"
            >
              <Paperclip className="size-4" />
            </button>
          </>
        )}
      </div>

      <textarea
        ref={box}
        aria-label="Message"
        rows={2}
        value={value}
        autoFocus={autoFocus}
        disabled={disabled}
        placeholder={placeholder}
        onChange={(e) => {
          setRecallIndex(null);
          onChange(e.target.value);
          setCaret(e.target.selectionStart ?? e.target.value.length);
        }}
        onClick={syncCaret}
        onKeyUp={syncCaret}
        onSelect={syncCaret}
        onPaste={(e) => {
          const files = Array.from(e.clipboardData?.files ?? []);
          if (files.length > 0 && onAttach) {
            e.preventDefault();
            onAttach(files);
            return;
          }
          const pastedText = e.clipboardData?.getData('text/plain') ?? '';
          const allowInline = shiftPasteRef.current;
          shiftPasteRef.current = false;
          if (
            !allowInline &&
            onLargePaste &&
            pastedText &&
            utf8ByteLength(pastedText) >= LARGE_PASTE_BYTES
          ) {
            e.preventDefault();
            onLargePaste(pastedText);
          }
        }}
        onKeyDown={(e) => {
          if (
            (e.metaKey || e.ctrlKey) &&
            e.shiftKey &&
            e.key.toLowerCase() === 'v'
          ) {
            shiftPasteRef.current = true;
            return;
          }

          // Prompt stash / restore (Cmd/Ctrl+S)
          if (
            (e.metaKey || e.ctrlKey) &&
            !e.altKey &&
            !e.shiftKey &&
            e.key.toLowerCase() === 's'
          ) {
            e.preventDefault();
            if (value.trim()) {
              pushStash(value, sessionId);
              onChange('');
              setRecallIndex(null);
              setStashNotice('Draft stashed');
            } else {
              const latest = popStash();
              if (latest) {
                onChange(latest.text);
                setStashNotice('Stash restored');
              }
            }
            return;
          }

          // Option/Alt+ArrowUp: edit latest queued message
          if (e.altKey && e.key === 'ArrowUp' && onEditLatestQueued) {
            e.preventDefault();
            onEditLatestQueued();
            return;
          }

          // Autocomplete menu navigation
          if (trigger && !e.nativeEvent.isComposing) {
            const items = menuItemsRef.current;
            if (e.key === 'ArrowDown' && items.length > 0) {
              e.preventDefault();
              setMenuIndex((i) => (i + 1) % items.length);
              return;
            }
            if (e.key === 'ArrowUp' && items.length > 0) {
              e.preventDefault();
              setMenuIndex((i) => (i - 1 + items.length) % items.length);
              return;
            }
            if ((e.key === 'Enter' || e.key === 'Tab') && items.length > 0) {
              e.preventDefault();
              const chosen = items[menuIndex] ?? items[0];
              if (chosen) selectMenuItem(chosen);
              return;
            }
            if (e.key === 'Escape') {
              e.preventDefault();
              setDismissedKey(triggerKey);
              return;
            }
          }

          // Prompt recall (ArrowUp/ArrowDown in empty composer or active recall)
          if (
            !e.altKey &&
            !e.metaKey &&
            !e.ctrlKey &&
            !e.shiftKey &&
            sentHistory.length > 0
          ) {
            if (e.key === 'ArrowUp' && (!value || recallIndex !== null)) {
              const nextIdx =
                recallIndex === null
                  ? sentHistory.length - 1
                  : Math.max(0, recallIndex - 1);
              e.preventDefault();
              setRecallIndex(nextIdx);
              onChange(sentHistory[nextIdx]);
              return;
            }
            if (e.key === 'ArrowDown' && recallIndex !== null) {
              e.preventDefault();
              if (recallIndex >= sentHistory.length - 1) {
                setRecallIndex(null);
                onChange('');
              } else {
                const nextIdx = recallIndex + 1;
                setRecallIndex(nextIdx);
                onChange(sentHistory[nextIdx]);
              }
              return;
            }
          }

          // Enter / Cmd+Enter submit
          if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            if (e.metaKey || e.ctrlKey) submitOpposite();
            else submitPrimary();
          }
        }}
        className={cn(
          'block max-h-60 min-h-[3.25rem] w-full resize-none bg-transparent pl-4 pt-3.5 pb-1 text-[15px] leading-6 placeholder:text-muted-foreground focus:outline-none',
          stashes.length > 0 ? 'pr-24' : 'pr-12',
        )}
      />

      <Lightbox
        images={attachments.map((a) => ({ src: a.previewUrl, alt: a.name }))}
        index={
          viewing !== null && viewing < attachments.length ? viewing : null
        }
        onIndexChange={setViewing}
        onClose={() => setViewing(null)}
      />

      {attachError && (
        <p role="alert" className="px-4 pb-1 text-xs text-destructive">
          {attachError}
        </p>
      )}
      {stashNotice && (
        <p role="status" className="px-4 pb-1 text-xs text-muted-foreground">
          {stashNotice}
        </p>
      )}

      <div className="flex items-center gap-2 px-2.5 pb-2.5">
        <div className="flex min-w-0 flex-1 items-center gap-1 text-xs text-muted-foreground">
          {meta}
        </div>
        {running && (
          <button
            type="button"
            aria-label={`Follow-up mode: ${followUpMode === 'steer' ? 'Steer' : 'Queue'}`}
            title={
              followUpMode === 'steer'
                ? 'Enter steers the running turn · Cmd/Ctrl+Enter queues instead. Click to switch.'
                : 'Enter queues for after this turn · Cmd/Ctrl+Enter steers now. Click to switch.'
            }
            onMouseDown={(e) => e.preventDefault()}
            onClick={() =>
              setFollowUpMode(followUpMode === 'queue' ? 'steer' : 'queue')
            }
            className={cn(
              'inline-flex h-7 shrink-0 items-center gap-1 rounded-full border px-2 text-[11px] font-medium transition-colors hover:bg-accent',
              followUpMode === 'steer'
                ? 'border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {followUpMode === 'steer' ? (
              <Zap className="size-3" aria-hidden />
            ) : (
              <ListPlus className="size-3" aria-hidden />
            )}
            <span>{followUpMode === 'steer' ? 'Steer' : 'Queue'}</span>
          </button>
        )}
        {running && onStop && (
          <button
            type="button"
            aria-label="Stop"
            title="Stop"
            disabled={busy || disabled}
            onClick={onStop}
            className="flex size-8 shrink-0 items-center justify-center rounded-full border bg-background text-foreground transition-colors hover:bg-accent disabled:opacity-50"
          >
            <Square className="size-3 fill-current" />
          </button>
        )}
        <button
          type="submit"
          aria-label={
            running ? (followUpMode === 'steer' ? 'Steer' : 'Queue') : 'Send'
          }
          title={
            running
              ? followUpMode === 'steer'
                ? 'Steer now (Enter) · Queue (Cmd/Ctrl+Enter)'
                : 'Queue message (Enter) · Steer now (Cmd/Ctrl+Enter)'
              : onBackgroundSubmit
                ? 'Send (Enter) · Start in background (Cmd/Ctrl+Enter)'
                : 'Send (Enter)'
          }
          disabled={!canSend}
          className="flex size-8 shrink-0 items-center justify-center rounded-full bg-foreground text-background transition-opacity hover:opacity-90 disabled:bg-muted disabled:text-muted-foreground"
        >
          {running && followUpMode === 'steer' ? (
            <Zap className="size-3.5" />
          ) : (
            <ArrowUp className="size-4" />
          )}
        </button>
      </div>
    </form>
  );
}
