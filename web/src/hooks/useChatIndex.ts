import { useQueries, useQuery } from '@tanstack/react-query';
import {
  agentsQuery,
  sessionsQuery,
  type Agent,
  type Session,
} from '@/api/queries';

export interface ChatEntry {
  session: Session;
  agent: Agent;
}

// The sidebar keeps every agent's session list warm; poll it more gently than
// the open agent's own list (5s) does.
const SIDEBAR_POLL_MS = 15_000;

/**
 * Everything the user can chat with: their agents, agents shared with them,
 * and every agent's sessions, newest activity first.
 *
 * The API has no "sessions across agents" endpoint, so this fans out to one
 * sessions request per agent. Each query shares its cache key with the
 * per-page queries, so nothing is fetched twice.
 */
export function useChatIndex() {
  const agentList = useQuery(agentsQuery);
  // Deleting an agent is asynchronous: it stays listed as "destroying" until
  // its sandbox is gone. Offering it would send the landing page (and the
  // sidebar) back into the agent that was just deleted.
  const agents: { agent: Agent }[] = (agentList.data ?? [])
    .filter((a) => a.state !== 'destroying')
    .map((agent) => ({ agent }));
  const sessionQs = useQueries({
    queries: agents.map(({ agent }) => ({
      ...sessionsQuery(agent.id),
      refetchInterval: SIDEBAR_POLL_MS,
    })),
  });
  const chats: ChatEntry[] = [];
  sessionQs.forEach((q, i) => {
    for (const s of q.data ?? []) chats.push({ session: s, ...agents[i] });
  });
  chats.sort((a, b) => b.session.updatedAt.localeCompare(a.session.updatedAt));

  // An agent's last activity: its newest session, else when it was created.
  const lastActive = new Map<string, string>();
  for (const c of chats)
    if (!lastActive.has(c.agent.id))
      lastActive.set(c.agent.id, c.session.updatedAt);
  agents.sort((a, b) =>
    (lastActive.get(b.agent.id) ?? b.agent.createdAt).localeCompare(
      lastActive.get(a.agent.id) ?? a.agent.createdAt,
    ),
  );

  const loading = agentList.isPending || sessionQs.some((q) => q.isPending);
  const error =
    agentList.error ?? sessionQs.find((q) => q.error)?.error ?? null;

  return { agents, chats, loading, error };
}
