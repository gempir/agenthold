import { useState } from 'react';
import { MessageSquarePlus } from 'lucide-react';
import { Button } from '@/components/ui/button';

/** The inline box for a review comment on the selected lines. */
export function CommentBox({
  label,
  onSubmit,
  onCancel,
}: {
  /** What is commented on, e.g. "Lines 4-7". */
  label: string;
  onSubmit: (body: string) => void;
  onCancel: () => void;
}) {
  const [body, setBody] = useState('');
  const submit = () => {
    if (body.trim()) onSubmit(body.trim());
  };
  return (
    <form
      role="group"
      aria-label="Review comment"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      className="my-1.5 grid gap-2 rounded-lg border bg-background p-2 font-sans shadow-sm"
    >
      <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <MessageSquarePlus className="size-3.5" aria-hidden />
        {label}
      </span>
      <textarea
        autoFocus
        aria-label="Comment"
        value={body}
        rows={3}
        placeholder="Leave a comment for the agent…"
        onChange={(e) => setBody(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.preventDefault();
            e.stopPropagation();
            onCancel();
          } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            submit();
          }
        }}
        className="min-h-16 w-full resize-y rounded-md border border-input bg-transparent px-2 py-1.5 text-sm placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      />
      <div className="flex justify-end gap-2">
        <Button type="button" size="sm" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={!body.trim()}>
          Add to composer
        </Button>
      </div>
    </form>
  );
}

/** A selected range of lines: anchor is where it started, end where it ends. */
export type LineSelection = { anchor: number; end: number };

export const selectionBounds = (s: LineSelection) =>
  [Math.min(s.anchor, s.end), Math.max(s.anchor, s.end)] as const;

/** A click on a line number: shift extends the selection, else it starts one. */
export function nextSelection(
  prev: LineSelection | null,
  at: number,
  shift: boolean,
): LineSelection {
  return shift && prev
    ? { anchor: prev.anchor, end: at }
    : { anchor: at, end: at };
}

/** A short confirmation that a comment went to the composer. */
export function AttachedNote({ rich }: { rich: boolean }) {
  return (
    <p role="status" className="text-xs text-emerald-600 dark:text-emerald-400">
      {rich ? 'Added to the composer.' : 'Added to your message draft.'}
    </p>
  );
}
