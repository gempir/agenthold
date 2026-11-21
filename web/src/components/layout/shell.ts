import { createContext, useContext } from 'react';

export interface ShellState {
  /** Mobile: the sidebar drawer is showing. */
  drawerOpen: boolean;
  setDrawerOpen: (o: boolean) => void;
  /** Desktop: the sidebar is hidden to give the thread the full width. */
  collapsed: boolean;
  setCollapsed: (c: boolean) => void;
  openNewAgent: () => void;
}

export const ShellContext = createContext<ShellState | null>(null);

export function useShell(): ShellState {
  const s = useContext(ShellContext);
  if (!s) throw new Error('useShell outside AppShell');
  return s;
}

export function readPref(key: string, fallback: string): string {
  try {
    return localStorage.getItem(`agenthold.${key}`) ?? fallback;
  } catch {
    return fallback;
  }
}

export function writePref(key: string, value: string) {
  try {
    localStorage.setItem(`agenthold.${key}`, value);
  } catch {
    // storage unavailable: keep the choice for this page only
  }
}
