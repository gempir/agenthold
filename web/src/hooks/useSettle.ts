import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import { sessionsQuery, type Session } from '@/api/queries';

/**
 * Settles a thread (done: out of the active list) or unsettles it. The
 * agent's session list is updated at once, so the sidebar and the thread
 * view move it without waiting for the next poll.
 */
export function useSettle() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ session, settle }: { session: Session; settle: boolean }) =>
      unwrap(
        settle
          ? api.POST('/sessions/{sessionId}/settle', {
              params: { path: { sessionId: session.id } },
            })
          : api.POST('/sessions/{sessionId}/unsettle', {
              params: { path: { sessionId: session.id } },
            }),
      ),
    onSuccess: async (updated) => {
      const key = sessionsQuery(updated.agentId).queryKey;
      qc.setQueryData<Session[]>(key, (list) =>
        list?.map((s) => (s.id === updated.id ? updated : s)),
      );
      await qc.invalidateQueries({ queryKey: key });
    },
  });
}
