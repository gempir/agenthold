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
import { AppPage, isAppSection, type AppSection } from '@/pages/AppPage';
import { HomePage } from '@/pages/HomePage';
import { LoginPage } from '@/pages/LoginPage';
import { NewChatPage } from '@/pages/NewChatPage';
import { NodesPage } from '@/pages/NodesPage';
import { OverviewPage } from '@/pages/OverviewPage';
import { ProvidersPage } from '@/pages/ProvidersPage';
import { SlackLinkPage } from '@/pages/SlackLinkPage';
import { SlackPage } from '@/pages/SlackPage';
import { ThreadsSettingsPage } from '@/pages/ThreadsSettingsPage';
import {
  AgentsSettingsPage,
  SecretsSettingsPage,
  ToolsSettingsPage,
} from '@/pages/LibraryPages';

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

const overviewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/overview',
  component: OverviewPage,
});

const appPageRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/apps/$appId',
  validateSearch: (s: Record<string, unknown>): { tab?: AppSection } => ({
    ...(isAppSection(s.tab) && { tab: s.tab }),
  }),
  component: function AppRoute() {
    const { appId } = appPageRoute.useParams();
    const { tab } = appPageRoute.useSearch();
    return <AppPage key={appId} appId={appId} tab={tab} />;
  },
});

// Where the Slack bot's "Connect your agenthold account" link lands.
const slackLinkRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/slack/link',
  validateSearch: (s: Record<string, unknown>): { code?: string } => ({
    ...(str(s.code) && { code: str(s.code) }),
  }),
  component: function SlackLink() {
    const { code } = slackLinkRoute.useSearch();
    return <SlackLinkPage code={code} />;
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

const nodesRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/nodes',
  component: NodesPage,
});

const providersRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/providers',
  component: ProvidersPage,
});

const agentsSettingsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/agents',
  component: AgentsSettingsPage,
});

const secretsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/secrets',
  component: SecretsSettingsPage,
});

const slackRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/slack',
  component: SlackPage,
});

const threadsSettingsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/threads',
  component: ThreadsSettingsPage,
});

const toolsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/tools',
  component: ToolsSettingsPage,
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([
    indexRoute,
    newChatRoute,
    agentRoute,
    agentSettingsRoute,
    appPageRoute,
    overviewRoute,
    slackLinkRoute,
    settingsRoute.addChildren([
      settingsIndexRoute,
      threadsSettingsRoute,
      agentsSettingsRoute,
      secretsRoute,
      toolsRoute,
      tokensRoute,
      slackRoute,
      providersRoute,
      nodesRoute,
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
