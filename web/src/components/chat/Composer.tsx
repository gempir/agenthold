import { useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { ArrowUp, Paperclip, Square, X } from 'lucide-react';
import { cn } from '@/lib/utils';
import { IMAGE_TYPES, type Attachment } from '@/lib/attachments';
import { Lightbox } from '@/components/chat/Lightbox';

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
  attachments = [],
  attachError,
  onAttach,
  onRemoveAttachment,
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
  /** Images waiting to be sent. Pasting or the paperclip adds more via onAttach. */
  attachments?: Attachment[];
  attachError?: string | null;
  onAttach?: (files: File[]) => void;
  onRemoveAttachment?: (id: string) => void;
}) {
  const box = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  const [viewing, setViewing] = useState<number | null>(null);
  const canSend =
    (!!value.trim() || attachments.length > 0) &&
    !running &&
    !busy &&
    !disabled;

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
      <textarea
        ref={box}
        aria-label="Message"
        rows={2}
        value={value}
        autoFocus={autoFocus}
        disabled={disabled}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onPaste={(e) => {
          if (!onAttach) return;
          const files = Array.from(e.clipboardData.files);
          if (!files.some((f) => f.type.startsWith('image/'))) return;
          e.preventDefault();
          onAttach(files);
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            if (canSend) onSubmit();
          }
        }}
        className="block max-h-60 min-h-[3.25rem] w-full resize-none bg-transparent px-4 pt-3.5 pb-1 text-[15px] leading-6 placeholder:text-muted-foreground focus:outline-none"
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
      <div className="flex items-center gap-2 px-2.5 pb-2.5">
        {onAttach && (
          <>
            <input
              ref={picker}
              type="file"
              multiple
              hidden
              aria-label="Attach images"
              accept={IMAGE_TYPES.join(',')}
              onChange={(e) => {
                onAttach(Array.from(e.target.files ?? []));
                e.target.value = '';
              }}
            />
            <button
              type="button"
              aria-label="Attach image"
              title="Attach image"
              disabled={disabled}
              onClick={() => picker.current?.click()}
              className="flex size-8 shrink-0 items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:opacity-50"
            >
              <Paperclip className="size-4" />
            </button>
          </>
        )}
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
