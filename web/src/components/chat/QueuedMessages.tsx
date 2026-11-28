import { ImageIcon, Pencil, Send, Trash2, Zap } from 'lucide-react';
import type { QueuedMessage } from '@/api/queries';

/**
 * Displays the session's queued follow-up messages right above the composer,
 * with Send now, Edit, and Remove controls.
 */
export function QueuedMessages({
  messages,
  busy,
  onSendNow,
  onEdit,
  onRemove,
}: {
  messages: QueuedMessage[];
  busy?: boolean;
  onSendNow: (messageId: string) => void;
  onEdit: (message: QueuedMessage) => void;
  onRemove: (messageId: string) => void;
}) {
  if (messages.length === 0) return null;
  return (
    <div
      data-testid="queued-messages"
      aria-label="Queued messages"
      className="rounded-xl border bg-card/80 px-3 py-2 text-xs shadow-sm"
    >
      <div className="mb-1.5 flex items-center justify-between gap-2 text-[11px] font-medium text-muted-foreground">
        <span>Queued ({messages.length})</span>
        <span className="hidden text-[10px] opacity-75 sm:inline">
          Option+Up to edit latest
        </span>
      </div>
      <ul className="grid gap-1.5">
        {messages.map((m) => (
          <li
            key={m.id}
            data-testid="queued-message-item"
            className="flex items-start gap-2 rounded-lg border bg-background/70 px-2.5 py-1.5"
          >
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-1.5">
                {m.steer && (
                  <span className="inline-flex shrink-0 items-center gap-0.5 rounded-full bg-amber-500/15 px-1.5 py-0.5 text-[10px] font-medium text-amber-600 dark:text-amber-400">
                    <Zap className="size-2.5" aria-hidden />
                    Steer
                  </span>
                )}
                {m.images && m.images.length > 0 && (
                  <span className="inline-flex shrink-0 items-center gap-0.5 text-[10px] text-muted-foreground">
                    <ImageIcon className="size-3" aria-hidden />
                    {m.images.length}
                  </span>
                )}
              </div>
              <p className="line-clamp-2 text-xs leading-relaxed whitespace-pre-wrap text-foreground">
                {m.text || '(attached image)'}
              </p>
            </div>
            <div className="flex shrink-0 items-center gap-0.5">
              <button
                type="button"
                aria-label="Send now"
                title="Send now (steer)"
                disabled={busy}
                onClick={() => onSendNow(m.id)}
                className="inline-flex h-6 items-center gap-1 rounded-md px-1.5 text-[11px] font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:opacity-50"
              >
                <Send className="size-3" aria-hidden />
                <span>Send now</span>
              </button>
              <button
                type="button"
                aria-label="Edit queued message"
                title="Edit"
                disabled={busy}
                onClick={() => onEdit(m)}
                className="inline-flex size-6 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:opacity-50"
              >
                <Pencil className="size-3" aria-hidden />
              </button>
              <button
                type="button"
                aria-label="Remove queued message"
                title="Remove"
                disabled={busy}
                onClick={() => onRemove(m.id)}
                className="inline-flex size-6 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive disabled:opacity-50"
              >
                <Trash2 className="size-3" aria-hidden />
              </button>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}
