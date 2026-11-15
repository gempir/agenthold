import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Outlet, useRouterState } from '@tanstack/react-router';
import { meQuery } from '@/api/queries';
import { NewAgentDialog } from '@/components/AgentsSection';
import { ErrorText, Loading } from '@/components/Spinner';
import { Sidebar } from '@/components/layout/Sidebar';
import {
  readPref,
  ShellContext,
  writePref,
  type ShellState,
} from '@/components/layout/shell';

/**
 * Full-viewport frame: the chat sidebar on the left and the routed view in
 * the middle. Below the md breakpoint the sidebar becomes a drawer.
 */
export function AppShell() {
  const me = useQuery(meQuery);
  const href = useRouterState({ select: (s) => s.location.href });
  // The mobile drawer remembers the location it was opened at, so any
  // navigation closes it.
  const [drawerAt, setDrawerAt] = useState<string | null>(null);
  const drawerOpen = drawerAt === href;
  const [collapsed, setCollapsedState] = useState(
    () => readPref('sidebar', 'open') === 'collapsed',
  );
  const [newAgent, setNewAgent] = useState(false);

  const shell = useMemo<ShellState>(
    () => ({
      drawerOpen,
      setDrawerOpen: (o) => setDrawerAt(o ? href : null),
      collapsed,
      setCollapsed: (c) => {
        setCollapsedState(c);
        writePref('sidebar', c ? 'collapsed' : 'open');
      },
      openNewAgent: () => setNewAgent(true),
    }),
    [drawerOpen, href, collapsed],
  );

  if (me.isPending)
    return (
      <div className="flex h-dvh items-center justify-center">
        <Loading />
      </div>
    );
  if (me.error)
    return (
      <div className="flex h-dvh items-center justify-center p-6">
        <ErrorText error={me.error} />
      </div>
    );

  return (
    <ShellContext.Provider value={shell}>
      <div className="flex h-dvh overflow-hidden">
        {!collapsed && (
          <aside className="hidden w-[288px] shrink-0 border-r md:block">
            <Sidebar />
          </aside>
        )}
        {drawerOpen && (
          <div className="fixed inset-0 z-40 md:hidden">
            <div
              aria-hidden
              className="absolute inset-0 bg-black/60"
              onClick={() => setDrawerAt(null)}
            />
            <aside className="absolute inset-y-0 left-0 w-[min(320px,88vw)] border-r shadow-2xl">
              <Sidebar onClose={() => setDrawerAt(null)} />
            </aside>
          </div>
        )}
        <main className="flex min-w-0 flex-1 flex-col">
          <Outlet />
        </main>
      </div>
      {newAgent && <NewAgentDialog onClose={() => setNewAgent(false)} />}
    </ShellContext.Provider>
  );
}
