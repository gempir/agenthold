import { Link, Outlet, useRouterState } from '@tanstack/react-router';
import {
  Bot,
  KeyRound,
  LockKeyhole,
  Plug,
  Wrench,
  type LucideIcon,
} from 'lucide-react';
import {
  SettingsLayout,
  SettingsNavItem,
} from '@/components/layout/SettingsLayout';
import { Crumb } from '@/components/layout/TopBar';

/**
 * Account-level settings pages, each a child route of /settings. Add a page
 * by adding its route under settingsRoute in router.tsx and an entry here.
 */
export const ACCOUNT_SETTINGS_NAV: {
  to: string;
  label: string;
  icon: LucideIcon;
}[] = [
  { to: '/settings/agents', label: 'Agents', icon: Bot },
  { to: '/settings/secrets', label: 'Secrets', icon: LockKeyhole },
  { to: '/settings/tools', label: 'Tools', icon: Wrench },
  { to: '/settings/providers', label: 'Provider logins', icon: Plug },
  { to: '/settings/tokens', label: 'API tokens', icon: KeyRound },
];

export function AccountSettings() {
  const path = useRouterState({ select: (s) => s.location.pathname });
  return (
    <SettingsLayout
      navLabel="Account settings"
      crumbs={<Crumb current>Settings</Crumb>}
      nav={ACCOUNT_SETTINGS_NAV.map((n) => (
        <SettingsNavItem key={n.to} active={path.startsWith(n.to)}>
          <Link to={n.to}>
            <n.icon className="size-4 shrink-0" aria-hidden />
            {n.label}
          </Link>
        </SettingsNavItem>
      ))}
    >
      <Outlet />
    </SettingsLayout>
  );
}
