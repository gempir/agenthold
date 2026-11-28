import {
  CircleCheck,
  CircleDashed,
  CircleX,
  GitMerge,
  GitPullRequest,
  GitPullRequestClosed,
  GitPullRequestDraft,
} from 'lucide-react';
import type { PullRequestChecksState, PullRequestState } from '@/api/queries';
import { cn } from '@/lib/utils';

const STATE: Record<
  PullRequestState,
  { label: string; cls: string; Icon: typeof GitPullRequest }
> = {
  open: {
    label: 'Open',
    cls: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400',
    Icon: GitPullRequest,
  },
  draft: {
    label: 'Draft',
    cls: 'border-border bg-muted text-muted-foreground',
    Icon: GitPullRequestDraft,
  },
  merged: {
    label: 'Merged',
    cls: 'border-violet-500/30 bg-violet-500/10 text-violet-700 dark:text-violet-400',
    Icon: GitMerge,
  },
  closed: {
    label: 'Closed',
    cls: 'border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-400',
    Icon: GitPullRequestClosed,
  },
};

/** "Open", "Draft", "Merged" or "Closed", colored like the host does. */
export function PullRequestStateBadge({
  state,
  className,
}: {
  state: PullRequestState;
  className?: string;
}) {
  const s = STATE[state] ?? STATE.open;
  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center gap-1 rounded-full border px-1.5 py-0.5 text-[11px] font-medium',
        s.cls,
        className,
      )}
    >
      <s.Icon className="size-3" aria-hidden />
      {s.label}
    </span>
  );
}

const CHECKS: Record<
  Exclude<PullRequestChecksState, 'none'>,
  { label: string; cls: string; Icon: typeof CircleCheck }
> = {
  success: {
    label: 'Checks passed',
    cls: 'text-emerald-600 dark:text-emerald-400',
    Icon: CircleCheck,
  },
  failure: {
    label: 'Checks failed',
    cls: 'text-red-600 dark:text-red-400',
    Icon: CircleX,
  },
  pending: {
    label: 'Checks running',
    cls: 'text-amber-600 dark:text-amber-400',
    Icon: CircleDashed,
  },
};

/** The CI verdict of a pull request's head, as an icon (nothing for none). */
export function ChecksIndicator({
  state,
  className,
}: {
  state: PullRequestChecksState;
  className?: string;
}) {
  if (state === 'none') return null;
  const c = CHECKS[state];
  if (!c) return null;
  return (
    <span
      role="img"
      aria-label={c.label}
      title={c.label}
      className={cn('inline-flex shrink-0', c.cls, className)}
    >
      <c.Icon className="size-3.5" aria-hidden />
    </span>
  );
}
