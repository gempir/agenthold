import { useLayoutEffect, useRef, type ReactNode } from 'react';
import { ArrowUp, Square } from 'lucide-react';
import { cn } from '@/lib/utils';

const MAX_HEIGHT = 240;

/**
 * The large rounded message box pinned under a conversation. Enter sends,
 * Shift+Enter adds a line. While the agent works the send button becomes a
 * stop button and the text can still be drafted.
 */
export function Composer({
  value,
  onChange,
  onSubmit,
  onStop,
  running = false,
  busy = false,
  disabled = false,
  placeholder,
  autoFocus,
  meta,
  className,
}: {
  value: string;
  onChange: (v: string) => void;
  onSubmit: () => void;
  onStop?: () => void;
  running?: boolean;
  /** A send or stop request is in flight. */
  busy?: boolean;
  disabled?: boolean;
  placeholder: string;
  autoFocus?: boolean;
  /** Shown at the lower left, e.g. the harness and model. */
  meta?: ReactNode;
  className?: string;
}) {
  const box = useRef<HTMLTextAreaElement>(null);
  const canSend = !!value.trim() && !running && !busy && !disabled;

  // Grow with the text up to a cap, then scroll.
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${Math.min(el.scrollHeight, MAX_HEIGHT)}px`;
  }, [value]);

  return (
    <form
      className={cn(
        'rounded-2xl border bg-card shadow-lg shadow-black/10 transition-colors focus-within:border-foreground/20',
        className,
      )}
      onSubmit={(e) => {
        e.preventDefault();
        if (canSend) onSubmit();
      }}
      onClick={(e) => {
        if (e.target === e.currentTarget) box.current?.focus();
      }}
    >
      <textarea
        ref={box}
        aria-label="Message"
        rows={2}
        value={value}
        autoFocus={autoFocus}
        disabled={disabled}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            if (canSend) onSubmit();
          }
        }}
        className="block max-h-60 min-h-[3.25rem] w-full resize-none bg-transparent px-4 pt-3.5 pb-1 text-[15px] leading-6 placeholder:text-muted-foreground focus:outline-none"
      />
      <div className="flex items-center gap-2 px-2.5 pb-2.5">
        <div className="flex min-w-0 flex-1 items-center gap-1 text-xs text-muted-foreground">
          {meta}
        </div>
        {running && onStop ? (
          <button
            type="button"
            aria-label="Stop"
            title="Stop"
            disabled={busy || disabled}
            onClick={onStop}
            className="flex size-8 shrink-0 items-center justify-center rounded-full bg-foreground text-background transition-opacity hover:opacity-90 disabled:opacity-50"
          >
            <Square className="size-3 fill-current" />
          </button>
        ) : (
          <button
            type="submit"
            aria-label="Send"
            title="Send (Enter)"
            disabled={!canSend}
            className="flex size-8 shrink-0 items-center justify-center rounded-full bg-foreground text-background transition-opacity hover:opacity-90 disabled:bg-muted disabled:text-muted-foreground"
          >
            <ArrowUp className="size-4" />
          </button>
        )}
      </div>
    </form>
  );
}
