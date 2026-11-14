import { useEffect, useState } from 'react';
import { Loader2, Moon } from 'lucide-react';
import { isTransient, type SandboxState } from '@/api/queries';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';

const colors: Partial<Record<SandboxState, string>> = {
  running: 'border-transparent bg-green-600 text-white',
  sleeping: 'border-transparent bg-slate-500 text-white',
  error: 'border-transparent bg-red-600 text-white',
};

const labels: Partial<Record<SandboxState, string>> = {
  sleeping: 'Asleep',
  starting: 'Waking…',
};

/** Re-renders every `ms` so relative times stay fresh. */
function useNow(ms: number) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
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
          'gap-1',
          colors[state] ?? 'border-transparent bg-amber-500 text-white',
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
