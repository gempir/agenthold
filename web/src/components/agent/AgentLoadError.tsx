import { Link } from '@tanstack/react-router';
import { ApiError, errorMessage } from '@/api/client';
import { Button } from '@/components/ui/button';

/** Shown in place of an agent's chat or settings when it cannot load. */
export function AgentLoadError({ error }: { error: unknown }) {
  const missing = error instanceof ApiError && error.status === 404;
  return (
    <div
      role="alert"
      className="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center"
    >
      <p className="text-base font-medium">
        {missing ? 'Agent not found' : 'Could not load this agent'}
      </p>
      <p className="max-w-sm text-sm text-muted-foreground">
        {missing
          ? 'It may have been deleted, or you do not have access to it.'
          : errorMessage(error)}
      </p>
      <Button asChild variant="outline" size="sm" className="mt-2">
        <Link to="/new">Start a new chat</Link>
      </Button>
    </div>
  );
}
