import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import {
  agentQuery,
  agentsQuery,
  harnessesQuery,
  modelsQuery,
  sessionsQuery,
  type Agent,
  type PermissionMode,
  type Session,
} from '@/api/queries';
import {
  ModelPicker,
  type DraftModelTarget,
  type HarnessChoice,
} from '@/components/ModelPicker';
import { PermissionModePicker } from '@/components/agent/PermissionModePicker';
import { choiceLabel, toChoice, type Choice } from '@/lib/models';

export function useHarnessName(id: string) {
  const harnesses = useQuery(harnessesQuery);
  return harnesses.data?.find((h) => h.id === id)?.name ?? id;
}

/**
 * Moves the agent's default harness (used from agent settings and draft
 * single-harness switch). Inside an existing thread, HarnessMeta switches only
 * that thread via PATCH /sessions/{sessionId}/harness instead.
 */
export function useSwitchHarness(agent: Agent) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ harness, model }: { harness: string; model: string }) =>
      unwrap(
        api.PATCH('/agents/{agentId}', {
          params: { path: { agentId: agent.id } },
          body: { harness, model, modelOptions: {} },
        }),
      ),
    onSuccess: async (a) => {
      qc.setQueryData(agentQuery(a.id).queryKey, a);
      await Promise.all([
        qc.invalidateQueries({ queryKey: agentsQuery.queryKey }),
        qc.invalidateQueries({ queryKey: modelsQuery(a.id).queryKey }),
        qc.invalidateQueries({ queryKey: sessionsQuery(a.id).queryKey }),
      ]);
    },
  });
}

/**
 * The harness, model, and permission mode controls inside the composer footer:
 * - Inside a thread (`session` set): switches harness, model, and permission
 *   mode for THIS thread ONLY (`PATCH /sessions/{sessionId}/harness`,
 *   `PUT /sessions/{sessionId}/model`, `PATCH /sessions/{sessionId}/settings`).
 * - On a draft (`!session`): holds draft choices (including Shift-click
 *   multi-model selection) and applies them when the thread(s) start.
 */
export function HarnessMeta({
  agent,
  session,
  draft,
  onDraft,
  multiModels,
  onMultiModels,
  permissionMode,
  onPermissionMode,
  canUse = true,
}: {
  agent: Agent;
  session?: Session;
  draft?: Choice;
  onDraft?: (c: Choice) => void;
  multiModels?: DraftModelTarget[];
  onMultiModels?: (models: DraftModelTarget[]) => void;
  permissionMode?: PermissionMode;
  onPermissionMode?: (mode: PermissionMode) => void;
  canUse?: boolean;
}) {
  const qc = useQueryClient();
  const harnesses = useQuery(harnessesQuery);
  const effectiveHarness = session?.harness || agent.harness;
  const pinnedHarness = !!session?.harness && session.harness !== agent.harness;
  const currentHarness = harnesses.data?.find((h) => h.id === effectiveHarness);
  const name = currentHarness?.name ?? effectiveHarness;
  const models = useQuery(
    modelsQuery(agent.id, pinnedHarness ? effectiveHarness : undefined),
  );
  const isOwner = agent.role === 'owner';
  // Inside a thread, any user who can prompt can switch THIS thread's harness.
  // On a draft, only owners see other harnesses (switching changes agent default).
  const otherHarnesses =
    session && canUse
      ? (harnesses.data ?? []).filter((h) => h.id !== effectiveHarness)
      : isOwner
        ? (harnesses.data ?? []).filter((h) => h.id !== effectiveHarness)
        : [];
  const otherModels = useQueries({
    queries: otherHarnesses.map((h) => ({
      ...modelsQuery(agent.id, h.id),
      enabled: isOwner,
    })),
  });
  const sessions = useQuery(sessionsQuery(agent.id));
  const switchAgentHarness = useSwitchHarness(agent);
  const switchThreadHarness = useMutation({
    mutationFn: ({ harness, model }: { harness: string; model: string }) =>
      unwrap(
        api.PATCH('/sessions/{sessionId}/harness', {
          params: { path: { sessionId: session!.id } },
          body: {
            harness,
            ...(model ? { model } : {}),
            modelOptions: {},
          },
        }),
      ),
    onSuccess: async (_s, vars) => {
      await Promise.all([
        qc.invalidateQueries({ queryKey: sessionsQuery(agent.id).queryKey }),
        qc.invalidateQueries({
          queryKey: modelsQuery(agent.id, vars.harness).queryKey,
        }),
      ]);
    },
  });
  const agentDefault = pinnedHarness
    ? { model: '', options: {} }
    : toChoice({
        model: agent.model,
        options: agent.modelOptions,
      });
  const setModel = useMutation({
    mutationFn: (c: Choice) =>
      unwrap(
        api.PUT('/sessions/{sessionId}/model', {
          params: { path: { sessionId: session!.id } },
          body: { model: c.model, options: c.options },
        }),
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ['agents', agent.id, 'sessions'] }),
  });
  const setPerm = useMutation({
    mutationFn: (mode: PermissionMode) =>
      unwrap(
        api.PATCH('/sessions/{sessionId}/settings', {
          params: { path: { sessionId: session!.id } },
          body: { permissionMode: mode },
        }),
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ['agents', agent.id, 'sessions'] }),
  });
  const value = session
    ? setModel.isPending && setModel.variables
      ? setModel.variables
      : toChoice(session.model)
    : (draft ?? { model: '', options: {} });
  const effectivePermission: PermissionMode = session
    ? (setPerm.isPending && setPerm.variables) ||
      session.permissionMode ||
      'full-access'
    : (permissionMode ?? 'full-access');
  const defaultLabel = choiceLabel(models.data, agentDefault, 'Default model');
  const switchable = models.data?.switchable ?? true;
  const others: HarnessChoice[] = otherHarnesses.map((h, i) => ({
    id: h.id,
    name: h.name,
    iconUrl: h.iconUrl,
    catalog: otherModels[i]?.data,
  }));
  const err = setModel.error ?? setPerm.error;
  return (
    <div className="flex min-w-0 items-center gap-0.5">
      <ModelPicker
        catalog={models.data}
        value={value}
        defaultLabel={defaultLabel}
        defaultDescription={
          pinnedHarness
            ? `${name}'s default model`
            : "The model in the agent's settings"
        }
        // Shared users choose among the models the harness reported.
        disabled={
          !canUse ||
          (!switchable && !!session && others.length === 0) ||
          (!isOwner && !session && models.data?.source !== 'harness')
        }
        allowCustom={isOwner}
        label="Model for this conversation"
        harness={{
          id: effectiveHarness,
          name,
          iconUrl: currentHarness?.iconUrl,
        }}
        multiSelect={!session && !!onMultiModels}
        selectedModels={!session ? multiModels : undefined}
        onSelectedModelsChange={!session ? onMultiModels : undefined}
        harnessSwitch={
          others.length > 0
            ? {
                agentName: agent.name,
                scope: session ? 'thread' : 'agent',
                others,
                busy: session
                  ? session.running
                  : sessions.data?.some((s) => s.running),
                loginLink: (
                  <Link to="/settings/providers" className="underline">
                    Provider logins.
                  </Link>
                ),
                onSwitch: async (harness, model) => {
                  if (session) {
                    await switchThreadHarness.mutateAsync({ harness, model });
                  } else {
                    await switchAgentHarness.mutateAsync({ harness, model });
                    onDraft?.({ model: '', options: {} });
                    onMultiModels?.([]);
                  }
                },
              }
            : undefined
        }
        onChange={(c) => (session ? setModel.mutate(c) : onDraft?.(c))}
      />
      {(session || onPermissionMode) && (
        <PermissionModePicker
          value={effectivePermission}
          disabled={!canUse || setPerm.isPending}
          onChange={(mode) =>
            session ? setPerm.mutate(mode) : onPermissionMode?.(mode)
          }
        />
      )}
      {err && (
        <span role="alert" className="truncate text-destructive">
          {err instanceof Error ? err.message : 'Could not update settings'}
        </span>
      )}
    </div>
  );
}
