import { queryOptions } from '@tanstack/react-query';
import { api, unwrap } from './client';
import type { components } from './schema';

export type User = components['schemas']['User'];
export type AuthProvider = components['schemas']['AuthProvider'];

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

export const RUNNING_POLL_MS = 10_000;

/** refetchInterval for a single agent: fast while transient, slow while
 * running (to refresh sleepsAt and notice it falling asleep). */
export const agentPollInterval = (a: { state: SandboxState } | undefined) =>
  a && isTransient(a.state) ? POLL_MS : a ? RUNNING_POLL_MS : false;

/** refetchInterval for an agent list: poll while any agent is transient. */
export const agentsPollInterval = (
  list: { state: SandboxState }[] | undefined,
) =>
  list?.some((a) => isTransient(a.state))
    ? POLL_MS
    : list?.length
      ? RUNNING_POLL_MS
      : false;

export const nodesQuery = queryOptions({
  queryKey: ['nodes'],
  queryFn: () => unwrap(api.GET('/nodes')),
  refetchInterval: RUNNING_POLL_MS,
  retry: false,
});

/** Everything running on this installation. Admins only. React Query's
 * interval polling pauses while the tab is hidden. */
export const OVERVIEW_POLL_MS = 5000;
export const overviewQuery = queryOptions({
  queryKey: ['admin', 'overview'],
  queryFn: () => unwrap(api.GET('/admin/overview')),
  refetchInterval: OVERVIEW_POLL_MS,
  refetchIntervalInBackground: false,
  retry: false,
});

export const sandboxLimitsQuery = queryOptions({
  queryKey: ['sandbox', 'limits'],
  queryFn: () => unwrap(api.GET('/sandbox/limits')),
  retry: false,
});

/** Agents you own and agents shared with you. */
export const agentsQuery = queryOptions({
  queryKey: ['agents'],
  queryFn: () => unwrap(api.GET('/agents')),
  refetchInterval: (q) => agentsPollInterval(q.state.data),
  retry: false,
});

/** Your templates: stopped machines to clone. The agents list leaves them out. */
export const templatesQuery = queryOptions({
  queryKey: ['agents', 'templates'],
  queryFn: () =>
    unwrap(api.GET('/agents', { params: { query: { template: 'true' } } })),
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

/**
 * The models of the agent's harness (what it last reported, or a fallback).
 * With harness, those it would have on that other harness (owners only).
 */
export const modelsQuery = (agentId: string, harness?: string) =>
  queryOptions({
    queryKey: harness
      ? ['agents', agentId, 'models', harness]
      : ['agents', agentId, 'models'],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/models', {
          params: { path: { agentId }, query: harness ? { harness } : {} },
        }),
      ),
    retry: false,
  });

export const harnessesQuery = queryOptions({
  queryKey: ['harnesses'],
  queryFn: () => unwrap(api.GET('/harnesses')),
  retry: false,
});

export type AccessList = components['schemas']['AccessList'];
export type Share = components['schemas']['Share'];
export type AuditEntry = components['schemas']['AuditEntry'];
export type AgentRole = components['schemas']['AgentRole'];

/** Owners configure, share and delete; shared users only use the agent. */
export const isOwner = (a: { role: AgentRole } | undefined) =>
  a?.role === 'owner';

export const accessQuery = (agentId: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'shares'],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/shares', { params: { path: { agentId } } }),
      ),
    retry: false,
  });

export type ProviderLoginInfo = components['schemas']['ProviderLoginInfo'];
export type AgentLogin = components['schemas']['AgentLogin'];

/** Your account-level harness logins (Claude Code, Codex). */
export const providerLoginsQuery = queryOptions({
  queryKey: ['me', 'provider-logins'],
  queryFn: () => unwrap(api.GET('/me/provider-logins')),
  retry: false,
});

export type Exposure = components['schemas']['Exposure'];
export type ExposureAccess = components['schemas']['ExposureAccess'];

/** Ports of the machine served at preview URLs. */
export const exposuresQuery = (agentId: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'exposures'],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/exposures', {
          params: { path: { agentId } },
        }),
      ),
    retry: false,
  });

export type Service = components['schemas']['Service'];
export type ServiceStatus = components['schemas']['ServiceStatus'];

/** Services supervised in a machine (agent or app). */
export const servicesQuery = (agentId: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'services'],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/services', {
          params: { path: { agentId } },
        }),
      ),
    refetchInterval: 5000,
    retry: false,
  });

export type App = components['schemas']['App'];

/** Apps you own and apps shared with you. */
export const appsQuery = queryOptions({
  queryKey: ['apps'],
  queryFn: () => unwrap(api.GET('/apps')),
  refetchInterval: (q) => agentsPollInterval(q.state.data),
  retry: false,
});

export const appQuery = (appId: string) =>
  queryOptions({
    queryKey: ['apps', appId],
    queryFn: () =>
      unwrap(api.GET('/apps/{appId}', { params: { path: { appId } } })),
    refetchInterval: (q) => agentPollInterval(q.state.data),
    retry: false,
  });
