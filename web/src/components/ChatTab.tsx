import { lazy, Suspense } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import { sessionsQuery, type SandboxState } from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';

const ChatView = lazy(() => import('@/components/chat/ChatView'));

export function ChatTab({
  agentId,
  operator,
  sessionId,
  agentState,
  onSelect,
}: {
  agentId: string;
  operator: boolean;
  sessionId?: string;
  agentState?: SandboxState;
  onSelect: (id: string) => void;
}) {
  const qc = useQueryClient();
  const sessions = useQuery(sessionsQuery(agentId));
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/agents/{agentId}/sessions', {
          params: { path: { agentId } },
          body: {},
        }),
      ),
    onSuccess: async (s) => {
      await qc.invalidateQueries({
        queryKey: ['agents', agentId, 'sessions'],
      });
      onSelect(s.id);
    },
  });
  const selected = sessions.data?.find((s) => s.id === sessionId);

  return (
    <div className="grid gap-4 md:grid-cols-[14rem_1fr]">
      <aside className="flex flex-col gap-2">
        {operator && (
          <Button
            size="sm"
            disabled={create.isPending}
            onClick={() => create.mutate()}
          >
            New session
          </Button>
        )}
        <ErrorText error={create.error} />
        {sessions.isPending && <Loading />}
        <ErrorText error={sessions.error} />
        {sessions.data?.length === 0 && (
          <p className="text-sm text-muted-foreground">No sessions yet.</p>
        )}
        <ul aria-label="Sessions" className="flex flex-col gap-1">
          {sessions.data?.map((s) => (
            <li key={s.id}>
              <button
                type="button"
                aria-current={s.id === sessionId}
                onClick={() => onSelect(s.id)}
                className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent ${
                  s.id === sessionId ? 'bg-accent font-medium' : ''
                }`}
              >
                <span className="min-w-0 flex-1 truncate">
                  {s.title || new Date(s.createdAt).toLocaleString()}
                </span>
                {s.running && (
                  <span
                    title="running"
                    aria-label="running"
                    className="size-2 shrink-0 animate-pulse rounded-full bg-emerald-500"
                  />
                )}
              </button>
            </li>
          ))}
        </ul>
      </aside>
      <section className="min-w-0">
        {sessionId ? (
          <Suspense fallback={<Loading />}>
            <ChatView
              key={sessionId}
              sessionId={sessionId}
              operator={operator}
              agentState={agentState}
              initialRunning={selected?.running}
            />
          </Suspense>
        ) : (
          <p className="text-sm text-muted-foreground">
            Select a session or start a new one.
          </p>
        )}
      </section>
    </div>
  );
}
