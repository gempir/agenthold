import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Sparkles } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  harnessesQuery,
  modelsQuery,
  type Agent,
  type Session,
} from '@/api/queries';
import { ModelPicker } from '@/components/ModelPicker';
import { choiceLabel, toChoice, type Choice } from '@/lib/models';

export function useHarnessName(id: string) {
  const harnesses = useQuery(harnessesQuery);
  return harnesses.data?.find((h) => h.id === id)?.name ?? id;
}

/**
 * The harness and the conversation's model inside the composer: the harness
 * name opens its settings, the model button opens the picker. Without a
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
  const name = useHarnessName(agent.harness);
  const qc = useQueryClient();
  const models = useQuery(modelsQuery(agent.id));
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
  const isOwner = agent.role === 'owner';
  return (
    <div className="flex min-w-0 items-center gap-0.5">
      <Link
        to="/a/$agentId/settings"
        params={{ agentId: agent.id }}
        search={{ tab: 'harness' }}
        title="Harness settings"
        className="flex shrink-0 items-center gap-1.5 rounded-md px-1.5 py-1 hover:bg-accent hover:text-foreground"
      >
        <Sparkles className="size-3.5 shrink-0" aria-hidden />
        <span className="hidden sm:inline">{name}</span>
      </Link>
      <span aria-hidden className="text-muted-foreground/50">
        ·
      </span>
      <ModelPicker
        catalog={models.data}
        value={value}
        defaultLabel={defaultLabel}
        defaultDescription="The model in the agent's settings"
        // Shared users choose among the models the harness reported.
        disabled={
          !canUse ||
          (!switchable && !!session) ||
          (!isOwner && models.data?.source !== 'harness')
        }
        allowCustom={isOwner}
        label="Model for this conversation"
        note={
          switchable
            ? 'A change applies from the next message.'
            : 'This harness picks its model when it starts: change the agent default in the settings.'
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
