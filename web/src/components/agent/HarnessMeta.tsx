import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Sparkles } from 'lucide-react';
import { harnessesQuery, type Agent } from '@/api/queries';

export function useHarnessName(id: string) {
  const harnesses = useQuery(harnessesQuery);
  return harnesses.data?.find((h) => h.id === id)?.name ?? id;
}

/** "Claude Code · default" inside the composer; opens the harness settings. */
export function HarnessMeta({ agent }: { agent: Agent }) {
  const name = useHarnessName(agent.harness);
  return (
    <Link
      to="/a/$agentId/settings"
      params={{ agentId: agent.id }}
      search={{ tab: 'harness' }}
      title="Harness settings"
      className="flex min-w-0 items-center gap-1.5 rounded-md px-1.5 py-1 hover:bg-accent hover:text-foreground"
    >
      <Sparkles className="size-3.5 shrink-0" aria-hidden />
      <span className="truncate">
        {name}
        <span className="text-muted-foreground/70">
          {' · '}
          {agent.model || 'default model'}
        </span>
      </span>
    </Link>
  );
}
