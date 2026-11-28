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
import { ExecutorAccountsPage } from '@/pages/ExecutorAccountsPage';
import { GitSettingsPage } from '@/pages/GitSettingsPage';
import { AgentSettingsPage } from '@/pages/AgentSettingsPage';
import { AppPage, isAppSection, type AppSection } from '@/pages/AppPage';
import { HomePage } from '@/pages/HomePage';
import { ImagesPage } from '@/pages/ImagesPage';
import { LoginPage } from '@/pages/LoginPage';
import { NewChatPage } from '@/pages/NewChatPage';
import { NodesPage } from '@/pages/NodesPage';
import { OverviewPage } from '@/pages/OverviewPage';
import { ProfilePage } from '@/pages/ProfilePage';
import {
  isProjectSection,
  ProjectPage,
  type ProjectSection,
} from '@/pages/ProjectPage';
import { ProjectsPage } from '@/pages/ProjectsPage';
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
  ): { s?: string; panel?: PanelTab; cwd?: string } => ({
    ...(str(s.s) && { s: str(s.s) }),
    ...(isPanelTab(s.panel) && { panel: s.panel }),
    ...(str(s.cwd) && { cwd: str(s.cwd) }),
  }),
  component: function Agent() {
    const { agentId } = agentRoute.useParams();
    const { s, panel, cwd } = agentRoute.useSearch();
    const navigate = useNavigate();
    return (
      <AgentPage
        key={agentId}
        agentId={agentId}
        sessionId={s}
        panel={panel}
        cwd={cwd}
        onPanel={(p) =>
          void navigate({
            to: '/a/$agentId',
            params: { agentId },
            search: { s, panel: p, cwd },
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

// The overview moved into the settings; old links still land there.
const overviewRedirectRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/overview',
  beforeLoad: () => {
    throw redirect({ to: '/settings/overview', replace: true });
  },
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

// Projects moved into settings; old /projects links still land there.
const projectsRedirectRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/projects',
  component: () => (
    <ProjectsPage create={projectsRedirectRoute.useSearch().new} />
  ),
  validateSearch: (s: Record<string, unknown>): { new?: boolean } => ({
    ...((s.new === true || s.new === 'true') && { new: true }),
  }),
});

const projectRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/projects/$projectId',
  validateSearch: (s: Record<string, unknown>): { tab?: ProjectSection } => ({
    ...(isProjectSection(s.tab) && { tab: s.tab }),
  }),
  component: function ProjectRoute() {
    const { projectId } = projectRoute.useParams();
    const { tab } = projectRoute.useSearch();
    return <ProjectPage key={projectId} projectId={projectId} tab={tab} />;
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
    throw redirect({ to: '/settings/profile', replace: true });
  },
});

const profileRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/profile',
  component: ProfilePage,
});

const overviewRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/overview',
  component: OverviewPage,
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

const imagesRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/images',
  component: ImagesPage,
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

const accountsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/accounts',
  component: ExecutorAccountsPage,
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

const projectsSettingsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/projects',
  validateSearch: (s: Record<string, unknown>): { new?: boolean } => ({
    ...((s.new === true || s.new === 'true') && { new: true }),
  }),
  beforeLoad: ({ search }) => {
    throw redirect({ to: '/projects', search, replace: true });
  },
});

const gitRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/git',
  component: GitSettingsPage,
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
    projectsRedirectRoute,
    projectRoute,
    overviewRedirectRoute,
    slackLinkRoute,
    settingsRoute.addChildren([
      settingsIndexRoute,
      profileRoute,
      overviewRoute,
      threadsSettingsRoute,
      projectsSettingsRoute,
      agentsSettingsRoute,
      accountsRoute,
      secretsRoute,
      toolsRoute,
      tokensRoute,
      slackRoute,
      gitRoute,
      providersRoute,
      imagesRoute,
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
