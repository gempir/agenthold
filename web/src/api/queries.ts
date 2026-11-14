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
