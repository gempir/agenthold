import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import { agentsQuery, type Agent } from '@/api/queries';

/** Start/stop/rebuild and delete for one agent, keeping caches in step. */
export function useAgentActions(agent: Agent) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const agentId = agent.id;
  const path = { params: { path: { agentId } } };
  const setAgent = (a: Agent) => qc.setQueryData(['agents', agentId], a);
  const refreshList = () =>
    qc.invalidateQueries({ queryKey: agentsQuery.queryKey });

  const action = useMutation({
    mutationFn: (kind: 'start' | 'stop' | 'rebuild') =>
      unwrap(
        kind === 'start'
          ? api.POST('/agents/{agentId}/start', path)
          : kind === 'stop'
            ? api.POST('/agents/{agentId}/stop', path)
            : api.POST('/agents/{agentId}/rebuild', path),
      ),
    onSuccess: async (a) => {
      setAgent(a);
      await refreshList();
    },
  });

  const remove = useMutation({
    mutationFn: () => unwrap(api.DELETE('/agents/{agentId}', path)),
    onSuccess: async () => {
      qc.removeQueries({ queryKey: ['agents', agentId] });
      await refreshList();
      await navigate({ to: '/' });
    },
  });

  return {
    action,
    remove,
    setAgent,
    busy: action.isPending || remove.isPending,
  };
}
