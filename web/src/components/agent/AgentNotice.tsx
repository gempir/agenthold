import { CircleAlert, Loader2, Moon } from 'lucide-react';
import { isTransient, type Agent } from '@/api/queries';
import { ErrorText } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { useAgentActions } from '@/hooks/useAgentActions';

/** One slim line above the composer when the agent is not simply running. */
export function AgentNotice({
  agent: a,
  canUse,
}: {
  agent: Agent;
  canUse: boolean;
}) {
  const { action, busy } = useAgentActions(a);
  const row =
    'mb-2 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border bg-card/60 px-3 py-2 text-sm';
  if (a.state === 'sleeping')
    return (
      <div role="status" className={row}>
        <Moon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <p className="min-w-0 flex-1 text-muted-foreground">
          This agent is asleep. Its files and conversations are kept.
          {canUse
            ? ' Sending a message wakes it.'
            : ' Someone who can use it can wake it.'}
        </p>
        {canUse && (
          <Button
            size="sm"
            variant="outline"
            className="h-7"
            disabled={busy}
            onClick={() => action.mutate('start')}
          >
            Wake now
          </Button>
        )}
        <ErrorText error={action.error} />
      </div>
    );
  if (a.state === 'error')
    return (
      <div role="alert" className={`${row} border-destructive/30`}>
        <CircleAlert className="size-4 shrink-0 text-destructive" aria-hidden />
        <p className="min-w-0 flex-1 text-destructive">
          {a.stateError || 'The sandbox is in an error state.'}
        </p>
        {canUse && (
          <Button
            size="sm"
            variant="outline"
            className="h-7"
            disabled={busy}
            onClick={() => action.mutate('start')}
          >
            Start
          </Button>
        )}
        <ErrorText error={action.error} />
      </div>
    );
  if (isTransient(a.state))
    return (
      <div role="status" className={row}>
        <Loader2
          className="size-4 shrink-0 animate-spin text-amber-500"
          aria-hidden
        />
        <p className="min-w-0 flex-1 text-muted-foreground">
          {a.state === 'creating'
            ? 'Setting up the sandbox…'
            : a.state === 'starting'
              ? 'Waking the agent…'
              : `Sandbox is ${a.state}…`}
        </p>
      </div>
    );
  return null;
}
