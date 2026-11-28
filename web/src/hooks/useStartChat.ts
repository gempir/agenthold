import { agentDraftKey, setDraft } from '@/lib/drafts';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import type { ImageType } from '@/lib/attachments';
import type { Choice } from '@/lib/models';

/** Starts a conversation: creates a session (or project thread), sets its
 * model when one was picked, sends the first message and opens it. The server
 * titles the session from that first message. */
export function useStartChat() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return useMutation({
    mutationFn: async (v: {
      agentId: string;
      text: string;
      images?: { mimeType: ImageType; data: string }[];
      model?: Choice;
      /** Start a thread of this project. */
      projectId?: string;
      /** Work directly in the project's main checkouts instead of a branch. */
      direct?: boolean;
      /** Working directory relative to home, e.g. a repository from GitTab. */
      cwd?: string;
      gitBranch?: string;
      createBranch?: boolean;
      createWorktree?: boolean;
    }) => {
      const s = v.projectId
        ? await unwrap(
            api.POST('/projects/{projectId}/threads', {
              params: { path: { projectId: v.projectId } },
              body: {
                agentId: v.agentId,
                ...(v.direct && { direct: true }),
              },
            }),
          )
        : await unwrap(
            api.POST('/agents/{agentId}/sessions', {
              params: { path: { agentId: v.agentId } },
              body: {
                ...(v.cwd && { cwd: v.cwd }),
                ...(v.gitBranch && { gitBranch: v.gitBranch }),
                ...(v.createBranch && { createBranch: true }),
                ...(v.createWorktree && { createWorktree: true }),
              },
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
          body: { text: v.text, ...(v.images?.length && { images: v.images }) },
        }),
      );
      return s;
    },
    onSuccess: async (s, v) => {
      setDraft(agentDraftKey(s.agentId), '');
      await qc.invalidateQueries({
        queryKey: ['agents', s.agentId, 'sessions'],
      });
      if (v.projectId) {
        await qc.invalidateQueries({ queryKey: ['projects', v.projectId] });
      }
      await navigate({
        to: '/a/$agentId',
        params: { agentId: s.agentId },
        search: { s: s.id },
      });
    },
  });
}
