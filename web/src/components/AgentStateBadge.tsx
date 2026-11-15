import { Loader2, Moon } from 'lucide-react';
import { isTransient, type SandboxState } from '@/api/queries';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import { useNow } from '@/lib/time';

const colors: Partial<Record<SandboxState, string>> = {
  running:
    'border-transparent bg-emerald-500/15 text-emerald-700 dark:text-emerald-400',
  sleeping: 'border-transparent bg-muted text-muted-foreground',
  error: 'border-transparent bg-red-500/15 text-red-700 dark:text-red-400',
};

const labels: Partial<Record<SandboxState, string>> = {
  sleeping: 'Asleep',
  starting: 'Waking…',
  rebuilding: 'Resetting system…',
};

export const stateLabel = (s: SandboxState) => labels[s] ?? s;

/** Compact status glyph for list rows: a dot, a moon or a spinner. */
export function AgentStateDot({
  state,
  className,
}: {
  state: SandboxState;
  className?: string;
}) {
  const label = stateLabel(state);
  if (isTransient(state))
    return (
      <Loader2
        role="img"
        aria-label={label}
        className={cn('size-3 shrink-0 animate-spin text-amber-500', className)}
      />
    );
  if (state === 'sleeping')
    return (
      <Moon
        role="img"
        aria-label={label}
        className={cn('size-3 shrink-0 text-muted-foreground', className)}
      />
    );
  return (
    <span
      role="img"
      aria-label={label}
      className={cn(
        'inline-flex size-3 shrink-0 items-center justify-center',
        className,
      )}
    >
      <span
        className={cn(
          'size-2 rounded-full',
          state === 'running'
            ? 'bg-emerald-400'
            : state === 'error'
              ? 'bg-red-400'
              : 'bg-muted-foreground',
        )}
      />
    </span>
  );
}

export function sleepsInText(sleepsAt: string, now: number): string {
  const ms = new Date(sleepsAt).getTime() - now;
  if (!Number.isFinite(ms) || ms < 60_000) return 'sleeps soon';
  return `sleeps in ${Math.floor(ms / 60_000)} min`;
}

export function AgentStateBadge({
  state,
  sleepsAt,
}: {
  state: SandboxState;
  sleepsAt?: string;
}) {
  const transient = isTransient(state);
  const now = useNow(30_000);
  return (
    <>
      <Badge
        data-state={state}
        className={cn(
          'gap-1 rounded-full',
          colors[state] ??
            'border-transparent bg-amber-500/15 text-amber-700 dark:text-amber-400',
        )}
      >
        {transient && <Loader2 className="size-3 animate-spin" aria-hidden />}
        {state === 'sleeping' && <Moon className="size-3" aria-hidden />}
        {labels[state] ?? state}
      </Badge>
      {state === 'running' && sleepsAt && (
        <span className="text-xs text-muted-foreground">
          {sleepsInText(sleepsAt, now)}
        </span>
      )}
    </>
  );
}
