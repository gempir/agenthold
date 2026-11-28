import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Outlet, useRouterState } from "@tanstack/react-router";
import { meQuery } from "@/api/queries";
import { NewAgentDialog } from "@/components/AgentsSection";
import { NoAccess } from "@/components/NoAccess";
import { ErrorText, Loading } from "@/components/Spinner";
import { Sidebar } from "@/components/layout/Sidebar";
import {
  readPref,
  ShellContext,
  writePref,
  type ShellState,
} from "@/components/layout/shell";
import { useSwipeDrawer } from "@/components/layout/swipe";

const DEFAULT_WIDTH = 288;
const clampWidth = (w: number) => Math.min(560, Math.max(220, Math.round(w)));

/**
 * Full-viewport frame: the threads sidebar on the left and the routed view in
 * the middle. Below the md breakpoint the sidebar becomes a drawer.
 */
export function AppShell() {
  const me = useQuery(meQuery);
  const href = useRouterState({ select: (s) => s.location.href });
  // The mobile drawer remembers the location it was opened at, so any
  // navigation closes it.
  const [drawerAt, setDrawerAt] = useState<string | null>(null);
  const drawerOpen = drawerAt === href;
  useSwipeDrawer(drawerOpen, (o) => setDrawerAt(o ? href : null));
  const [collapsed, setCollapsedState] = useState(
    () => readPref("sidebar", "open") === "collapsed",
  );
  const [width, setWidth] = useState(() => {
    const n = Number(readPref("sidebar.width", ""));
    return Number.isFinite(n) && n > 0 ? clampWidth(n) : DEFAULT_WIDTH;
  });
  const resize = (w: number) => {
    const c = clampWidth(w);
    setWidth(c);
    writePref("sidebar.width", String(c));
  };
  const startResize = (e: React.PointerEvent) => {
    e.preventDefault();
    const x0 = e.clientX;
    const w0 = width;
    const move = (ev: PointerEvent) =>
      setWidth(clampWidth(w0 + ev.clientX - x0));
    const up = (ev: PointerEvent) => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      resize(w0 + ev.clientX - x0);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };
  const [newAgent, setNewAgent] = useState(false);

  const shell = useMemo<ShellState>(
    () => ({
      drawerOpen,
      setDrawerOpen: (o) => setDrawerAt(o ? href : null),
      collapsed,
      setCollapsed: (c) => {
        setCollapsedState(c);
        writePref("sidebar", c ? "collapsed" : "open");
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

  if (me.data.role === "disabled") return <NoAccess user={me.data.user} />;

  return (
    <ShellContext.Provider value={shell}>
      <div className="flex h-dvh overflow-hidden">
        {!collapsed && (
          <aside
            style={{ width }}
            className="relative hidden shrink-0 border-r md:block"
          >
            <Sidebar />
            <div
              role="separator"
              aria-orientation="vertical"
              aria-label="Resize sidebar"
              tabIndex={0}
              onPointerDown={startResize}
              onKeyDown={(e) => {
                if (e.key === "ArrowLeft") resize(width - 16);
                else if (e.key === "ArrowRight") resize(width + 16);
                else return;
                e.preventDefault();
              }}
              className="absolute inset-y-0 -right-1 z-10 w-2 cursor-col-resize hover:bg-accent focus-visible:bg-accent focus-visible:outline-none"
            />
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
              <Sidebar />
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
