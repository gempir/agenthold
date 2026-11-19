import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import type { Choice } from '@/lib/models';

/** Starts a conversation: creates a session, sets its model when one was
 * picked, sends the first message and opens it. The server titles the
 * session from that first message. */
export function useStartChat() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return useMutation({
    mutationFn: async (v: {
      agentId: string;
      text: string;
      model?: Choice;
    }) => {
      const s = await unwrap(
        api.POST('/agents/{agentId}/sessions', {
          params: { path: { agentId: v.agentId } },
          body: {},
        }),
      );
      if (v.model?.model || Object.keys(v.model?.options ?? {}).length) {
        await unwrap(
          api.PUT('/sessions/{sessionId}/model', {
            params: { path: { sessionId: s.id } },
            body: { model: v.model!.model, options: v.model!.options },
          }),
        );
      }
      await unwrap(
        api.POST('/sessions/{sessionId}/prompt', {
          params: { path: { sessionId: s.id } },
          body: { text: v.text },
        }),
      );
      return s;
    },
    onSuccess: async (s) => {
      await qc.invalidateQueries({
        queryKey: ['agents', s.agentId, 'sessions'],
      });
      await navigate({
        to: '/a/$agentId',
        params: { agentId: s.agentId },
        search: { s: s.id },
      });
    },
  });
}
