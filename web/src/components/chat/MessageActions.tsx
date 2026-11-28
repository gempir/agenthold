import { useEffect, useRef, useState, type RefObject } from 'react';
import { useMutation } from '@tanstack/react-query';
import { MessageSquareQuote, RotateCcw, X } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { ErrorText } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { addComposerContext } from '@/lib/composerContext';

function elementFromNode(node: Node | null): Element | null {
  if (!node) return null;
  return node.nodeType === Node.ELEMENT_NODE
    ? (node as Element)
    : node.parentElement;
}

/**
 * Floating "Cite in composer" action when the user selects text inside an
 * assistant message. Supports 1-click citing or citing with an optional note.
 */
export function QuoteSelectionPopover({
  sessionId,
  containerRef,
  enabled,
}: {
  sessionId: string;
  containerRef: RefObject<HTMLElement | null>;
  enabled: boolean;
}) {
  const [selection, setSelection] = useState<{
    text: string;
    top: number;
    left: number;
  } | null>(null);
  const [addingNote, setAddingNote] = useState(false);
  const [note, setNote] = useState('');
  const popRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!enabled) return;
    const readSelection = () => {
      const sel = window.getSelection();
      if (!sel || sel.isCollapsed) {
        setSelection(null);
        setAddingNote(false);
        return;
      }
      const text = sel.toString().trim();
      if (!text) {
        setSelection(null);
        return;
      }
      const anchorEl = elementFromNode(sel.anchorNode);
      const focusEl = elementFromNode(sel.focusNode);
      const assistantEl =
        anchorEl?.closest('[data-assistant-message="true"]') ??
        focusEl?.closest('[data-assistant-message="true"]');
      if (!assistantEl || !containerRef.current?.contains(assistantEl)) {
        setSelection(null);
        return;
      }
      const containerRect = containerRef.current.getBoundingClientRect();
      let top = 16;
      let left = Math.max(16, containerRect.width / 2 - 90);
      if (sel.rangeCount > 0) {
        const rangeRect = sel.getRangeAt(0).getBoundingClientRect();
        if (rangeRect.width > 0 || rangeRect.height > 0) {
          top = Math.max(8, rangeRect.top - containerRect.top - 42);
          left = Math.max(
            12,
            Math.min(
              containerRect.width - 220,
              rangeRect.left - containerRect.left,
            ),
          );
        }
      }
      setSelection({ text, top, left });
    };

    const onMouseUp = (e: MouseEvent) => {
      if (popRef.current?.contains(e.target as Node)) return;
      readSelection();
    };
    const onSelectionChange = () => {
      const sel = window.getSelection();
      if (!sel || sel.isCollapsed || !sel.toString().trim()) {
        if (!addingNote) setSelection(null);
      }
    };
    document.addEventListener('mouseup', onMouseUp);
    document.addEventListener('selectionchange', onSelectionChange);
    return () => {
      document.removeEventListener('mouseup', onMouseUp);
      document.removeEventListener('selectionchange', onSelectionChange);
    };
  }, [enabled, containerRef, addingNote]);

  if (!enabled || !selection) return null;

  const cite = (comment?: string) => {
    addComposerContext(sessionId, {
      kind: 'quote',
      text: selection.text,
      source: 'Assistant',
      ...(comment?.trim() ? { comment: comment.trim() } : {}),
    });
    window.getSelection()?.removeAllRanges();
    setSelection(null);
    setAddingNote(false);
    setNote('');
  };

  return (
    <div
      ref={popRef}
      role="dialog"
      aria-label="Cite selection"
      style={{ top: selection.top, left: selection.left }}
      className="absolute z-30 flex flex-col gap-1.5 rounded-xl border bg-card p-1.5 shadow-lg shadow-black/25"
    >
      {addingNote ? (
        <form
          className="flex items-center gap-1.5"
          onSubmit={(e) => {
            e.preventDefault();
            cite(note);
          }}
        >
          <input
            autoFocus
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder="Add a note (optional)…"
            aria-label="Citation note"
            className="h-7 w-48 rounded-md border bg-background px-2 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
          />
          <Button type="submit" size="sm" className="h-7 px-2 text-xs">
            Cite
          </Button>
          <button
            type="button"
            aria-label="Cancel citation"
            onClick={() => {
              setAddingNote(false);
              setNote('');
            }}
            className="flex size-6 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <X className="size-3.5" />
          </button>
        </form>
      ) : (
        <div className="flex items-center gap-1">
          <button
            type="button"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => cite()}
            className="inline-flex items-center gap-1.5 rounded-lg px-2 py-1 text-xs font-medium text-foreground transition-colors hover:bg-accent"
          >
            <MessageSquareQuote className="size-3.5" aria-hidden />
            Cite in composer
          </button>
          <button
            type="button"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => setAddingNote(true)}
            className="rounded-lg px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
          >
            + Note
          </button>
        </div>
      )}
    </div>
  );
}

/**
 * Subtle "Edit from here" hover action under a sent user message with a
 * confirmation popover supporting conversation rewind (and optional file revert
 * when in a worktree).
 */
export function RewindAction({
  sessionId,
  seq,
  fallbackText,
  hasWorktree,
  onRewound,
}: {
  sessionId: string;
  seq: number;
  fallbackText: string;
  hasWorktree: boolean;
  onRewound: (promptText: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const rewind = useMutation({
    mutationFn: (revertFiles: boolean) =>
      unwrap(
        api.POST('/sessions/{sessionId}/rewind', {
          params: { path: { sessionId } },
          body: { seq, revertFiles },
        }),
      ),
    onSuccess: (res) => {
      setOpen(false);
      onRewound(res.promptText ?? fallbackText);
    },
  });

  return (
    <div ref={rootRef} className="relative flex flex-col items-end">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] text-muted-foreground opacity-0 transition-opacity group-hover/user-msg:opacity-100 hover:bg-accent hover:text-foreground focus-visible:opacity-100"
      >
        <RotateCcw className="size-3" aria-hidden />
        Edit from here
      </button>
      {open && (
        <div
          role="dialog"
          aria-label="Rewind conversation"
          className="mt-1 grid w-72 gap-2 rounded-xl border bg-card p-3 text-left text-xs shadow-xl shadow-black/25"
        >
          <p className="font-medium text-foreground">Edit from this message?</p>
          <p className="leading-snug text-muted-foreground">
            This removes this message and all later turns, placing your prompt
            back in the composer.
          </p>
          <ErrorText error={rewind.error} />
          <div className="flex flex-wrap items-center gap-1.5">
            <Button
              size="sm"
              className="h-7 px-2.5 text-xs"
              disabled={rewind.isPending}
              onClick={() => rewind.mutate(false)}
            >
              Revert conversation
            </Button>
            {hasWorktree && (
              <Button
                size="sm"
                variant="outline"
                className="h-7 px-2.5 text-xs"
                disabled={rewind.isPending}
                onClick={() => rewind.mutate(true)}
              >
                Revert files too
              </Button>
            )}
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2 text-xs"
              disabled={rewind.isPending}
              onClick={() => setOpen(false)}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
