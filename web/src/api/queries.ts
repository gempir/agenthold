import { queryOptions } from '@tanstack/react-query';
import { api, unwrap } from './client';
import type { components } from './schema';

export type Workspace = components['schemas']['Workspace'];
export type Member = components['schemas']['Member'];
export type Invite = components['schemas']['Invite'];
export type WorkspaceRole = components['schemas']['WorkspaceRole'];
export type User = components['schemas']['User'];

export const meQuery = queryOptions({
  queryKey: ['me'],
  queryFn: () => unwrap(api.GET('/me')),
  retry: false,
});

export const providersQuery = queryOptions({
  queryKey: ['providers'],
  queryFn: () => unwrap(api.GET('/auth/providers')),
  retry: false,
});

export const workspacesQuery = queryOptions({
  queryKey: ['workspaces'],
  queryFn: () => unwrap(api.GET('/workspaces')),
  retry: false,
});

export const workspaceQuery = (id: string) =>
  queryOptions({
    queryKey: ['workspaces', id],
    queryFn: () =>
      unwrap(
        api.GET('/workspaces/{workspaceId}', {
          params: { path: { workspaceId: id } },
        }),
      ),
    retry: false,
  });

export const membersQuery = (id: string) =>
  queryOptions({
    queryKey: ['workspaces', id, 'members'],
    queryFn: () =>
      unwrap(
        api.GET('/workspaces/{workspaceId}/members', {
          params: { path: { workspaceId: id } },
        }),
      ),
    retry: false,
  });

export const invitesQuery = (id: string, enabled: boolean) =>
  queryOptions({
    queryKey: ['workspaces', id, 'invites'],
    queryFn: () =>
      unwrap(
        api.GET('/workspaces/{workspaceId}/invites', {
          params: { path: { workspaceId: id } },
        }),
      ),
    enabled,
    retry: false,
  });

export const invitePreviewQuery = (token: string) =>
  queryOptions({
    queryKey: ['invite', token],
    queryFn: () =>
      unwrap(api.GET('/invites/{token}', { params: { path: { token } } })),
    retry: false,
  });

export const canManage = (role: WorkspaceRole) =>
  role === 'owner' || role === 'admin';

export type Agent = components['schemas']['Agent'];
export type SandboxState = components['schemas']['SandboxState'];

const TRANSIENT: SandboxState[] = [
  'creating',
  'starting',
  'stopping',
  'rebuilding',
  'destroying',
];
export const isTransient = (s: SandboxState) => TRANSIENT.includes(s);

export const POLL_MS = 1000;

/** refetchInterval for a single agent: poll only while transient. */
export const agentPollInterval = (a: Agent | undefined) =>
  a && isTransient(a.state) ? POLL_MS : false;

/** refetchInterval for an agent list: poll while any agent is transient. */
export const agentsPollInterval = (list: Agent[] | undefined) =>
  list?.some((a) => isTransient(a.state)) ? POLL_MS : false;

export const sandboxLimitsQuery = queryOptions({
  queryKey: ['sandbox', 'limits'],
  queryFn: () => unwrap(api.GET('/sandbox/limits')),
  retry: false,
});

export const agentsQuery = (workspaceId: string) =>
  queryOptions({
    queryKey: ['workspaces', workspaceId, 'agents'],
    queryFn: () =>
      unwrap(
        api.GET('/workspaces/{workspaceId}/agents', {
          params: { path: { workspaceId } },
        }),
      ),
    refetchInterval: (q) => agentsPollInterval(q.state.data),
    retry: false,
  });

export const agentQuery = (agentId: string) =>
  queryOptions({
    queryKey: ['agents', agentId],
    queryFn: () =>
      unwrap(api.GET('/agents/{agentId}', { params: { path: { agentId } } })),
    refetchInterval: (q) => agentPollInterval(q.state.data),
    retry: false,
  });

export type Session = components['schemas']['Session'];
export type HarnessType = components['schemas']['HarnessType'];

export const sessionsQuery = (agentId: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'sessions'],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/sessions', {
          params: { path: { agentId } },
        }),
      ),
    refetchInterval: 5000,
    retry: false,
  });

export const harnessesQuery = queryOptions({
  queryKey: ['harnesses'],
  queryFn: () => unwrap(api.GET('/harnesses')),
  retry: false,
});
