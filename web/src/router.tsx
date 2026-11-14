import { useEffect } from 'react';
import {
  MutationCache,
  QueryCache,
  QueryClient,
  useQuery,
} from '@tanstack/react-query';
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  useNavigate,
  type RouterHistory,
} from '@tanstack/react-router';
import { ApiError } from '@/api/client';
import { meQuery } from '@/api/queries';
import { AppShell } from '@/components/AppShell';
import { Loading } from '@/components/Spinner';
import { InvitePage } from '@/pages/InvitePage';
import { LoginPage } from '@/pages/LoginPage';
import { WorkspacePage } from '@/pages/WorkspacePage';

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

function HomeRedirect() {
  const me = useQuery(meQuery);
  const navigate = useNavigate();
  const id = me.data?.personalWorkspaceId;
  useEffect(() => {
    if (id)
      void navigate({
        to: '/w/$workspaceId',
        params: { workspaceId: id },
        replace: true,
      });
  }, [id, navigate]);
  return <Loading />;
}

const indexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/',
  component: HomeRedirect,
});

const workspaceRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/w/$workspaceId',
  component: function Workspace() {
    const { workspaceId } = workspaceRoute.useParams();
    return <WorkspacePage key={workspaceId} workspaceId={workspaceId} />;
  },
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  inviteRoute,
  appRoute.addChildren([indexRoute, workspaceRoute]),
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
