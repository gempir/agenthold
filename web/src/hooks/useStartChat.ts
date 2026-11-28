import { agentDraftKey, setDraft } from '@/lib/drafts';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import type { PermissionMode, Session } from '@/api/queries';
import type { DraftModelTarget } from '@/components/ModelPicker';
import type { ImageType } from '@/lib/attachments';
import type { Choice } from '@/lib/models';

export interface StartChatInput {
  agentId: string;
  text: string;
  images?: { mimeType: ImageType; data: string }[];
  model?: Choice;
  /** Optional per-thread harness override (defaults to agent's harness). */
  harness?: string;
  /** Multiple models selected via Shift+click: starts one parallel thread per model. */
  models?: DraftModelTarget[];
  /** Per-thread permission mode (default full-access). */
  permissionMode?: PermissionMode;
  /** Start a thread of this project. */
  projectId?: string;
  /** Work directly in the project's main checkouts instead of a branch. */
  direct?: boolean;
  /** Working directory relative to home, e.g. a repository from GitTab. */
  cwd?: string;
  gitBranch?: string;
  createBranch?: boolean;
  createWorktree?: boolean;
  /** When true (Cmd/Ctrl+Enter on draft), create and send without navigating away. */
  background?: boolean;
}

export interface StartChatResult {
  primary: Session;
  sessions: Session[];
  background: boolean;
}

async function launchOne(
  v: StartChatInput,
  target: {
    harness?: string;
    model?: Choice;
    forceWorktree?: boolean;
  },
): Promise<Session> {
  const hasModel =
    !!target.model?.model ||
    Object.keys(target.model?.options ?? {}).length > 0;
  const nonDefaultPerm =
    !!v.permissionMode && v.permissionMode !== 'full-access';

  const s = v.projectId
    ? await unwrap(
        api.POST('/projects/{projectId}/threads', {
          params: { path: { projectId: v.projectId } },
          body: {
            agentId: v.agentId,
            ...(!target.forceWorktree && v.direct && { direct: true }),
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
            ...(target.harness && { harness: target.harness }),
            ...(nonDefaultPerm && { permissionMode: v.permissionMode }),
          },
        }),
      );

  if (v.projectId && (target.harness || nonDefaultPerm)) {
    await unwrap(
      api.PATCH('/sessions/{sessionId}/settings', {
        params: { path: { sessionId: s.id } },
        body: {
          ...(target.harness && { harness: target.harness }),
          ...(hasModel && {
            model: target.model!.model,
            modelOptions: target.model!.options,
          }),
          ...(nonDefaultPerm && { permissionMode: v.permissionMode }),
        },
      }),
    );
  } else if (hasModel) {
    await unwrap(
      api.PUT('/sessions/{sessionId}/model', {
        params: { path: { sessionId: s.id } },
        body: { model: target.model!.model, options: target.model!.options },
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
}

/** Starts one conversation (or parallel threads when multiple models are
 * selected): creates session(s) (or project worktree threads), applies per-thread
 * harness/model/permission settings, sends the first message, and opens the
 * primary thread unless `background` is set. */
export function useStartChat() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return useMutation({
    mutationFn: async (v: StartChatInput): Promise<StartChatResult> => {
      if (v.models && v.models.length > 1) {
        const sessions = await Promise.all(
          v.models.map((m) =>
            launchOne(v, {
              harness: m.harness || undefined,
              model: { model: m.model, options: m.options },
              // Parallel project threads always get separate worktrees.
              forceWorktree: !!v.projectId,
            }),
          ),
        );
        return {
          primary: sessions[0],
          sessions,
          background: !!v.background,
        };
      }

      const s = await launchOne(v, {
        harness: v.harness,
        model: v.model,
      });
      return {
        primary: s,
        sessions: [s],
        background: !!v.background,
      };
    },
    onSuccess: async (res, v) => {
      setDraft(agentDraftKey(res.primary.agentId), '');
      await qc.invalidateQueries({
        queryKey: ['agents', res.primary.agentId, 'sessions'],
      });
      if (v.projectId) {
        await qc.invalidateQueries({ queryKey: ['projects', v.projectId] });
      }
      if (!res.background) {
        await navigate({
          to: '/a/$agentId',
          params: { agentId: res.primary.agentId },
          search: { s: res.primary.id },
        });
      }
    },
  });
}
