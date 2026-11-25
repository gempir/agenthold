import { useEffect, useRef } from 'react';

/** Px from the left edge where a swipe may start to open the drawer. */
export const EDGE_WIDTH = 24;
/** Px a touch must travel horizontally before it counts as a swipe. */
export const SWIPE_DISTANCE = 60;
/** Below the md breakpoint (768px) the sidebar is a drawer. */
export const MOBILE_MAX_WIDTH = 767;

export interface SwipeGesture {
  startX: number;
  startY: number;
  endX: number;
  endY: number;
}

/**
 * What a completed touch gesture means for the drawer, or null for none.
 * A swipe must be mostly horizontal, so vertical scrolling never toggles it.
 * Opening starts at the left edge; closing is a leftward swipe anywhere.
 */
export function swipeAction(
  g: SwipeGesture,
  drawerOpen: boolean,
): 'open' | 'close' | null {
  const dx = g.endX - g.startX;
  const dy = g.endY - g.startY;
  if (Math.abs(dx) < SWIPE_DISTANCE || Math.abs(dx) < Math.abs(dy) * 1.5)
    return null;
  if (!drawerOpen) return dx > 0 && g.startX <= EDGE_WIDTH ? 'open' : null;
  return dx < 0 ? 'close' : null;
}

/**
 * Opens the mobile drawer on a rightward swipe from the left edge and closes
 * it on a leftward swipe. Inert at the desktop breakpoint, where the sidebar
 * is always visible.
 */
export function useSwipeDrawer(
  drawerOpen: boolean,
  setDrawerOpen: (open: boolean) => void,
) {
  // Refs keep the window listeners stable across renders.
  const open = useRef(drawerOpen);
  const setOpen = useRef(setDrawerOpen);
  useEffect(() => {
    open.current = drawerOpen;
    setOpen.current = setDrawerOpen;
  });

  useEffect(() => {
    let start: { x: number; y: number } | null = null;

    const onStart = (e: TouchEvent) => {
      const t = e.touches[0];
      start =
        e.touches.length === 1 && t ? { x: t.clientX, y: t.clientY } : null;
    };
    const onCancel = () => {
      start = null;
    };
    const onEnd = (e: TouchEvent) => {
      const s = start;
      start = null;
      const t = e.changedTouches[0];
      if (!s || !t || window.innerWidth > MOBILE_MAX_WIDTH) return;
      const action = swipeAction(
        { startX: s.x, startY: s.y, endX: t.clientX, endY: t.clientY },
        open.current,
      );
      if (action === 'open') setOpen.current(true);
      if (action === 'close') setOpen.current(false);
    };

    const opts = { passive: true } as const;
    window.addEventListener('touchstart', onStart, opts);
    window.addEventListener('touchend', onEnd, opts);
    window.addEventListener('touchcancel', onCancel, opts);
    return () => {
      window.removeEventListener('touchstart', onStart);
      window.removeEventListener('touchend', onEnd);
      window.removeEventListener('touchcancel', onCancel);
    };
  }, []);
}
