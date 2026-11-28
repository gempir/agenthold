import { queryOptions } from '@tanstack/react-query';
import { api, unwrap } from './client';
import type { components } from './schema';

export type User = components['schemas']['User'];
export type AuthProvider = components['schemas']['AuthProvider'];
export type Version = components['schemas']['Version'];

export const versionQuery = queryOptions({
  queryKey: ['version'],
  queryFn: () => unwrap(api.GET('/version')),
  staleTime: Infinity,
  retry: false,
});

export const meQuery = queryOptions({
  queryKey: ['me'],
  queryFn: () => unwrap(api.GET('/me')),
  retry: false,
});

export type UserSettings = components['schemas']['UserSettings'];

export const mySettingsQuery = queryOptions({
  queryKey: ['me', 'settings'],
  queryFn: () => unwrap(api.GET('/me/settings')),
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
export type SessionGit = components['schemas']['SessionGit'];
export type PermissionMode = components['schemas']['PermissionMode'];
export type QueuedMessage = components['schemas']['QueuedMessage'];
export type SessionShareRole = components['schemas']['SessionShareRole'];
export type SessionAccessRole = components['schemas']['SessionAccessRole'];
export type SessionShare = components['schemas']['SessionShare'];
export type SessionAccessList = components['schemas']['SessionAccessList'];
export type SharedThread = components['schemas']['SharedThread'];
export type GuestLink = components['schemas']['GuestLink'];
export type GuestLinkCreated = components['schemas']['GuestLinkCreated'];
export type GuestRequest = components['schemas']['GuestRequest'];
export type GuestThreadView = components['schemas']['GuestThreadView'];
export type SessionReviewComment =
  components['schemas']['SessionReviewComment'];
export type ReviewCommentSide = components['schemas']['ReviewCommentSide'];
export type SessionHandoffResult =
  components['schemas']['SessionHandoffResult'];
export type Skill = components['schemas']['Skill'];
export type FileSearchResult = components['schemas']['FileSearchResult'];
export type FileMatch = components['schemas']['FileMatch'];
export type HarnessType = components['schemas']['HarnessType'];
export type CustomHarness = components['schemas']['CustomHarness'];
export type CustomHarnessInput = components['schemas']['CustomHarnessInput'];
export type CustomHarnessPatch = components['schemas']['CustomHarnessPatch'];
export type AcpRegistryAgent = components['schemas']['AcpRegistryAgent'];
export type AcpRegistrySearchResponse =
  components['schemas']['AcpRegistrySearchResponse'];
export type HarnessAuthFlow = components['schemas']['HarnessAuthFlow'];

export const sessionQueueQuery = (sessionId: string) =>
  queryOptions({
    queryKey: ['sessions', sessionId, 'queue'],
    queryFn: () =>
      unwrap(
        api.GET('/sessions/{sessionId}/queue', {
          params: { path: { sessionId } },
        }),
      ),
    retry: false,
  });

export const mySharedThreadsQuery = queryOptions({
  queryKey: ['me', 'shared-threads'],
  queryFn: () => unwrap(api.GET('/me/shared-threads')),
  retry: false,
});

export const sessionSharesQuery = (sessionId: string) =>
  queryOptions({
    queryKey: ['sessions', sessionId, 'shares'],
    queryFn: () =>
      unwrap(
        api.GET('/sessions/{sessionId}/shares', {
          params: { path: { sessionId } },
        }),
      ),
    retry: false,
  });

export const sessionGuestLinksQuery = (sessionId: string) =>
  queryOptions({
    queryKey: ['sessions', sessionId, 'guest-links'],
    queryFn: () =>
      unwrap(
        api.GET('/sessions/{sessionId}/guest-links', {
          params: { path: { sessionId } },
        }),
      ),
    retry: false,
  });

export const sessionGuestRequestsQuery = (sessionId: string) =>
  queryOptions({
    queryKey: ['sessions', sessionId, 'guest-requests'],
    queryFn: () =>
      unwrap(
        api.GET('/sessions/{sessionId}/guest-requests', {
          params: { path: { sessionId } },
        }),
      ),
    retry: false,
  });

export const sessionReviewCommentsQuery = (sessionId: string) =>
  queryOptions({
    queryKey: ['sessions', sessionId, 'review-comments'],
    queryFn: () =>
      unwrap(
        api.GET('/sessions/{sessionId}/review-comments', {
          params: { path: { sessionId } },
        }),
      ),
    retry: false,
  });

export const skillsQuery = queryOptions({
  queryKey: ['skills'],
  queryFn: () => unwrap(api.GET('/skills')),
  staleTime: 30_000,
  retry: false,
});

export const agentSkillsQuery = (agentId: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'skills'],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/skills', {
          params: { path: { agentId } },
        }),
      ),
    staleTime: 30_000,
    retry: false,
  });

export const fileSearchQuery = (
  agentId: string,
  q: string,
  path?: string,
  limit = 10,
) =>
  queryOptions({
    queryKey: ['agents', agentId, 'files', 'search', path ?? '', q, limit],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/files/search', {
          params: {
            path: { agentId },
            query: { ...(path ? { path } : {}), q, limit },
          },
        }),
      ),
    staleTime: 10_000,
    retry: false,
  });

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

export const customHarnessesQuery = queryOptions({
  queryKey: ['me', 'harnesses'],
  queryFn: () => unwrap(api.GET('/me/harnesses')),
  retry: false,
});

export const acpRegistryQuery = (q: string, limit = 30) =>
  queryOptions({
    queryKey: ['acp-registry', q, limit],
    queryFn: () =>
      unwrap(
        api.GET('/acp-registry', {
          params: { query: { q: q || undefined, limit } },
        }),
      ),
    staleTime: 60_000,
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

export type BrowserState = components['schemas']['BrowserState'];
export type BrowserElement = components['schemas']['BrowserElement'];
export type BrowserRect = components['schemas']['BrowserRect'];
export type BrowserInteractInput =
  components['schemas']['BrowserInteractInput'];

/** The machine's headless browser (never starts it). */
export const browserStatusQuery = (agentId: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'browser', 'status'],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/browser/status', {
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

export type AdminUser = components['schemas']['AdminUser'];

/** Every user with its role. Admins only. */
export const adminUsersQuery = queryOptions({
  queryKey: ['admin', 'users'],
  queryFn: () => unwrap(api.GET('/admin/users')),
  retry: false,
});

export type GitSettings = components['schemas']['GitSettings'];
export type GitCredential = components['schemas']['GitCredential'];
export type GitRepo = components['schemas']['GitRepo'];
export type GitRepoList = components['schemas']['GitRepoList'];
export type GitRepoStatus = components['schemas']['GitRepoStatus'];

/** Your git author identity and connected HTTPS credentials. */
export const gitSettingsQuery = queryOptions({
  queryKey: ['me', 'git'],
  queryFn: () => unwrap(api.GET('/me/git')),
  retry: false,
});

/** Git repositories and worktrees found in a running agent's home. */
export const agentGitReposQuery = (agentId: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'git', 'repos'],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/git/repos', {
          params: { path: { agentId } },
        }),
      ),
    refetchInterval: 15_000,
    retry: false,
  });

/** Status and branches of one repository (path relative to home). */
export const agentGitStatusQuery = (agentId: string, path: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'git', 'status', path],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/git/status', {
          params: { path: { agentId }, query: { path } },
        }),
      ),
    retry: false,
  });

export type BuiltinBaseImage = components['schemas']['BuiltinBaseImage'];
export type CustomImage = components['schemas']['CustomImage'];
export type CustomImageDetail = components['schemas']['CustomImageDetail'];
export type ImageBuild = components['schemas']['ImageBuild'];
export type ImageBuildDetail = components['schemas']['ImageBuildDetail'];
export type ImageInput = components['schemas']['ImageInput'];
export type ImageList = components['schemas']['ImageList'];

/** Built-in base image and organization custom images. */
export const imagesQuery = queryOptions({
  queryKey: ['images'],
  queryFn: () => unwrap(api.GET('/images')),
  refetchInterval: (q) =>
    q.state.data?.images.some((i) => i.building) ? 3000 : false,
  retry: false,
});

export const imageQuery = (imageId: string) =>
  queryOptions({
    queryKey: ['images', imageId],
    queryFn: () =>
      unwrap(api.GET('/images/{imageId}', { params: { path: { imageId } } })),
    refetchInterval: (q) => (q.state.data?.building ? 2000 : false),
    retry: false,
  });

export const imageBuildQuery = (imageId: string, buildId: string) =>
  queryOptions({
    queryKey: ['images', imageId, 'builds', buildId],
    queryFn: () =>
      unwrap(
        api.GET('/images/{imageId}/builds/{buildId}', {
          params: { path: { imageId, buildId } },
        }),
      ),
    refetchInterval: (q) =>
      q.state.data?.status === 'queued' || q.state.data?.status === 'building'
        ? 1500
        : false,
    retry: false,
  });

export type ExecutorApprovalMode =
  components['schemas']['ExecutorApprovalMode'];
export type ExecutorStatus = components['schemas']['ExecutorStatus'];
export type ExecutorOverview = components['schemas']['ExecutorOverview'];
export type ExecutorApp = components['schemas']['ExecutorApp'];
export type ExecutorRequirement = components['schemas']['ExecutorRequirement'];
export type ExecutorAuthMethod = components['schemas']['ExecutorAuthMethod'];
export type ExecutorSecretField = components['schemas']['ExecutorSecretField'];
export type ExecutorAccount = components['schemas']['ExecutorAccount'];
export type ExecutorAccountHealth =
  components['schemas']['ExecutorAccountHealth'];
export type ExecutorCatalog = components['schemas']['ExecutorCatalog'];
export type ExecutorCatalogEntry =
  components['schemas']['ExecutorCatalogEntry'];
export type ExecutorConnectionSession =
  components['schemas']['ExecutorConnectionSession'];

export const executorStatusQuery = queryOptions({
  queryKey: ['me', 'executor'],
  queryFn: () => unwrap(api.GET('/me/executor')),
  retry: false,
});

export const executorOverviewQuery = queryOptions({
  queryKey: ['me', 'executor', 'overview'],
  queryFn: () => unwrap(api.GET('/me/executor/overview')),
  retry: false,
});

export const executorCatalogQuery = queryOptions({
  queryKey: ['me', 'executor', 'catalog'],
  queryFn: () => unwrap(api.GET('/me/executor/catalog')),
  staleTime: 60_000,
  retry: false,
});

export type Project = components['schemas']['Project'];
export type ProjectInput = components['schemas']['ProjectInput'];
export type ProjectSourceInput = components['schemas']['ProjectSourceInput'];
export type ProjectStatus = components['schemas']['ProjectStatus'];
export type ProjectThread = components['schemas']['ProjectThread'];
export type ProjectSourceResult = components['schemas']['ProjectSourceResult'];

export const projectsQuery = queryOptions({
  queryKey: ['projects'],
  queryFn: () => unwrap(api.GET('/projects')),
  retry: false,
});

export const projectQuery = (projectId: string) =>
  queryOptions({
    queryKey: ['projects', projectId],
    queryFn: () =>
      unwrap(
        api.GET('/projects/{projectId}', { params: { path: { projectId } } }),
      ),
    retry: false,
  });

/** Threads and what they changed; polled while the page is open. */
export const projectStatusQuery = (projectId: string, includeSettled = false) =>
  queryOptions({
    queryKey: ['projects', projectId, 'status', includeSettled],
    queryFn: () =>
      unwrap(
        api.GET('/projects/{projectId}/status', {
          params: { path: { projectId }, query: { includeSettled } },
        }),
      ),
    refetchInterval: RUNNING_POLL_MS,
    retry: false,
  });
export type GitDiff = components['schemas']['GitDiff'];
export type GitDiffFile = components['schemas']['GitDiffFile'];
export type GitCommit = components['schemas']['GitCommit'];
export type GitDiffMode = GitDiff['mode'];

/** A structured diff of the repository a directory (relative to home) is in. */
export const gitDiffQuery = (
  agentId: string,
  path: string,
  mode: GitDiffMode,
  ref?: string,
) =>
  queryOptions({
    queryKey: ['agents', agentId, 'git', 'diff', path, mode, ref ?? ''],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/git/diff', {
          params: {
            path: { agentId },
            query: {
              ...(path && { path }),
              mode,
              ...(ref && { ref }),
            },
          },
        }),
      ),
    // The working tree changes while the agent works.
    refetchInterval: mode === 'commit' ? false : RUNNING_POLL_MS,
    retry: false,
  });

/** The latest commits of a repository's branch. */
export const gitCommitsQuery = (agentId: string, path: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'git', 'commits', path],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/git/commits', {
          params: {
            path: { agentId },
            query: { ...(path && { path }), limit: 100 },
          },
        }),
      ),
    retry: false,
  });

export type TextFile = components['schemas']['TextFile'];

/** A workspace file as text (path relative to the home). */
export const fileTextQuery = (agentId: string, path: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'files', 'read', path],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/files/read', {
          params: { path: { agentId }, query: { path } },
        }),
      ),
    retry: false,
  });

export type ProjectScript = components['schemas']['ProjectScript'];
export type ProjectScripts = components['schemas']['ProjectScripts'];
export type ProjectScriptRun = components['schemas']['ProjectScriptRun'];

/** The scripts of agenthold.json (or t3.json) in a directory. */
export const projectScriptsQuery = (agentId: string, path: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'scripts', path],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/scripts', {
          params: { path: { agentId }, query: { ...(path && { path }) } },
        }),
      ),
    refetchInterval: RUNNING_POLL_MS,
    retry: false,
  });
