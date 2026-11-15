import { TokensPage } from '@/pages/TokensPage';
import { MutationCache, QueryCache, QueryClient } from '@tanstack/react-query';
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  redirect,
  useNavigate,
  type RouterHistory,
} from '@tanstack/react-router';
import { ApiError } from '@/api/client';
import { AppShell } from '@/components/AppShell';
import { isPanelTab, type PanelTab } from '@/components/agent/AgentPanel';
import { isAgentSection, type AgentSection } from '@/components/agent/sections';
import { AccountSettings } from '@/pages/AccountSettings';
import { AgentPage } from '@/pages/AgentPage';
import { AgentSettingsPage } from '@/pages/AgentSettingsPage';
import { HomePage } from '@/pages/HomePage';
import { InvitePage } from '@/pages/InvitePage';
import { LoginPage } from '@/pages/LoginPage';
import { NewChatPage } from '@/pages/NewChatPage';
import { ProvidersPage } from '@/pages/ProvidersPage';
import { WorkspacePage, type WorkspaceTab } from '@/pages/WorkspacePage';

const str = (v: unknown) => (v == null || v === '' ? undefined : String(v));

const rootRoute = createRootRoute({ component: Outlet });

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  validateSearch: (
    s: Record<string, unknown>,
  ): { redirect?: string; error?: string } => ({
    ...(str(s.redirect) && { redirect: str(s.redirect) }),
    ...(str(s.error) && { error: str(s.error) }),
  }),
  component: function Login() {
    const { redirect, error } = loginRoute.useSearch();
    return <LoginPage redirect={redirect} error={error} />;
  },
});

const inviteRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/invite/$token',
  component: function Invite() {
    const { token } = inviteRoute.useParams();
    return <InvitePage token={token} />;
  },
});

const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'app',
  component: AppShell,
});

const indexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/',
  component: HomePage,
});

const newChatRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/new',
  component: NewChatPage,
});

const workspaceRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/w/$workspaceId',
  validateSearch: (s: Record<string, unknown>): { tab?: WorkspaceTab } => ({
    ...(str(s.tab) && { tab: str(s.tab) as WorkspaceTab }),
  }),
  component: function Workspace() {
    const { workspaceId } = workspaceRoute.useParams();
    const { tab } = workspaceRoute.useSearch();
    return (
      <WorkspacePage key={workspaceId} workspaceId={workspaceId} tab={tab} />
    );
  },
});

const agentRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/a/$agentId',
  validateSearch: (
    s: Record<string, unknown>,
  ): { s?: string; panel?: PanelTab } => ({
    ...(str(s.s) && { s: str(s.s) }),
    ...(isPanelTab(s.panel) && { panel: s.panel }),
  }),
  component: function Agent() {
    const { agentId } = agentRoute.useParams();
    const { s, panel } = agentRoute.useSearch();
    const navigate = useNavigate();
    return (
      <AgentPage
        key={agentId}
        agentId={agentId}
        sessionId={s}
        panel={panel}
        onPanel={(p) =>
          void navigate({
            to: '/a/$agentId',
            params: { agentId },
            search: { s, panel: p },
            replace: true,
          })
        }
      />
    );
  },
});

const agentSettingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/a/$agentId/settings',
  validateSearch: (s: Record<string, unknown>): { tab?: AgentSection } => ({
    ...(isAgentSection(s.tab) && { tab: s.tab }),
  }),
  component: function AgentSettings() {
    const { agentId } = agentSettingsRoute.useParams();
    const { tab } = agentSettingsRoute.useSearch();
    return <AgentSettingsPage key={agentId} agentId={agentId} tab={tab} />;
  },
});

// Account settings: /settings/<page>, framed by AccountSettings' nav.
const settingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/settings',
  component: AccountSettings,
});

const settingsIndexRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/',
  beforeLoad: () => {
    throw redirect({ to: '/settings/tokens', replace: true });
  },
});

const tokensRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/tokens',
  component: TokensPage,
});

const providersRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/providers',
  component: ProvidersPage,
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  inviteRoute,
  appRoute.addChildren([
    indexRoute,
    newChatRoute,
    workspaceRoute,
    agentRoute,
    agentSettingsRoute,
    settingsRoute.addChildren([
      settingsIndexRoute,
      tokensRoute,
      providersRoute,
    ]),
  ]),
]);

export function createAppRouter(
  opts: { history?: RouterHistory; initialPath?: string } = {},
) {
  const history =
    opts.history ??
    (opts.initialPath
      ? createMemoryHistory({ initialEntries: [opts.initialPath] })
      : undefined);

  // Any 401 from the API sends the user to the login page and back.
  const onError = (e: unknown) => {
    if (!(e instanceof ApiError) || e.status !== 401) return;
    const loc = router.state.location;
    if (loc.pathname === '/login') return;
    void router.navigate({ to: '/login', search: { redirect: loc.href } });
  };
  const queryClient = new QueryClient({
    queryCache: new QueryCache({ onError }),
    mutationCache: new MutationCache({ onError }),
  });
  const router = createRouter({ routeTree, history, context: {} });
  return { router, queryClient };
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof createAppRouter>['router'];
  }
}
