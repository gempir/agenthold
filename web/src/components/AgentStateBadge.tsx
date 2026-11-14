import { Loader2 } from 'lucide-react';
import { isTransient, type SandboxState } from '@/api/queries';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';

const colors: Partial<Record<SandboxState, string>> = {
  running: 'border-transparent bg-green-600 text-white',
  sleeping: 'border-transparent bg-gray-400 text-white',
  error: 'border-transparent bg-red-600 text-white',
};

export function AgentStateBadge({ state }: { state: SandboxState }) {
  const transient = isTransient(state);
  return (
    <Badge
      data-state={state}
      className={cn(
        'gap-1',
        colors[state] ?? 'border-transparent bg-amber-500 text-white',
      )}
    >
      {transient && <Loader2 className="size-3 animate-spin" aria-hidden />}
      {state}
    </Badge>
  );
}
