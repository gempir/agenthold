import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Sparkles } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  agentQuery,
  agentsQuery,
  harnessesQuery,
  modelsQuery,
  sessionsQuery,
  type Agent,
  type Session,
} from '@/api/queries';
import { ModelPicker, type HarnessChoice } from '@/components/ModelPicker';
import { choiceLabel, toChoice, type Choice } from '@/lib/models';

export function useHarnessName(id: string) {
  const harnesses = useQuery(harnessesQuery);
  return harnesses.data?.find((h) => h.id === id)?.name ?? id;
}

/**
 * Moves the agent to another harness (the harness is the agent's, not a
 * conversation's): the model starts over on the new harness's default or the
 * model picked, the conversations' own models are reset by the server, and
 * the caches that describe the old harness are refreshed.
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
 * The harness and the conversation's model inside the composer: one picker
 * opens on the model, the harness name in front of it. The owner finds the
 * other harnesses with their models in the same list and can move the agent
 * there; others pick among the models the harness reported. Without a
 * session (a draft) the choice is held by the caller and applied when the
 * session is created.
 */
export function HarnessMeta({
  agent,
  session,
  draft,
  onDraft,
  canUse = true,
}: {
  agent: Agent;
  session?: Session;
  draft?: Choice;
  onDraft?: (c: Choice) => void;
  canUse?: boolean;
}) {
  const qc = useQueryClient();
  const harnesses = useQuery(harnessesQuery);
  const name =
    harnesses.data?.find((h) => h.id === agent.harness)?.name ?? agent.harness;
  const models = useQuery(modelsQuery(agent.id));
  const isOwner = agent.role === 'owner';
  const otherHarnesses = isOwner
    ? (harnesses.data ?? []).filter((h) => h.id !== agent.harness)
    : [];
  const otherModels = useQueries({
    queries: otherHarnesses.map((h) => modelsQuery(agent.id, h.id)),
  });
  const sessions = useQuery(sessionsQuery(agent.id));
  const switchHarness = useSwitchHarness(agent);
  const agentDefault = toChoice({
    model: agent.model,
    options: agent.modelOptions,
  });
  const set = useMutation({
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
  const value = session
    ? set.isPending && set.variables
      ? set.variables
      : toChoice(session.model)
    : (draft ?? { model: '', options: {} });
  const defaultLabel = `Agent default (${choiceLabel(models.data, agentDefault, 'harness default')})`;
  const switchable = models.data?.switchable ?? true;
  const others: HarnessChoice[] = otherHarnesses.map((h, i) => ({
    id: h.id,
    name: h.name,
    catalog: otherModels[i]?.data,
  }));
  return (
    <div className="flex min-w-0 items-center gap-0.5">
      <ModelPicker
        catalog={models.data}
        value={value}
        defaultLabel={defaultLabel}
        defaultDescription="The model in the agent's settings"
        // Shared users choose among the models the harness reported.
        disabled={
          !canUse ||
          (!switchable && !!session && others.length === 0) ||
          (!isOwner && models.data?.source !== 'harness')
        }
        allowCustom={isOwner}
        label="Model for this conversation"
        leading={
          <>
            <Sparkles className="size-3.5 shrink-0" aria-hidden />
            <span className="hidden sm:inline">{name}</span>
            <span aria-hidden className="text-muted-foreground/50">
              ·
            </span>
          </>
        }
        note={
          switchable
            ? 'A change applies from the next message.'
            : 'This harness picks its model when it starts: change the agent default in the settings.'
        }
        harnessSwitch={
          others.length > 0
            ? {
                agentName: agent.name,
                others,
                busy: sessions.data?.some((s) => s.running),
                loginLink: (
                  <Link to="/settings/providers" className="underline">
                    Provider logins.
                  </Link>
                ),
                onSwitch: async (harness, model) => {
                  await switchHarness.mutateAsync({ harness, model });
                  onDraft?.({ model: '', options: {} });
                },
              }
            : undefined
        }
        footer={
          isOwner && (
            <Link
              to="/a/$agentId/settings"
              params={{ agentId: agent.id }}
              search={{ tab: 'harness' }}
              className="underline"
            >
              Harness settings
            </Link>
          )
        }
        onChange={(c) => (session ? set.mutate(c) : onDraft?.(c))}
      />
      {set.error && (
        <span role="alert" className="truncate text-destructive">
          {set.error instanceof Error
            ? set.error.message
            : 'Could not set the model'}
        </span>
      )}
    </div>
  );
}
