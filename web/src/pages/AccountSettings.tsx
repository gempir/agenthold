import { useQuery } from '@tanstack/react-query';
import { Link, Outlet, useRouterState } from '@tanstack/react-router';
import {
  Bot,
  GitBranch,
  KeyRound,
  LockKeyhole,
  MessageSquare,
  Plug,
  Server,
  Wrench,
  type LucideIcon,
} from 'lucide-react';
import {
  SettingsLayout,
  SettingsNavItem,
} from '@/components/layout/SettingsLayout';
import { Crumb } from '@/components/layout/TopBar';
import { meQuery } from '@/api/queries';

/**
 * Account-level settings pages, each a child route of /settings. Add a page
 * by adding its route under settingsRoute in router.tsx and an entry here.
 */
export const ACCOUNT_SETTINGS_NAV: {
  to: string;
  label: string;
  icon: LucideIcon;
  /** Only shown to admins (the local admin or auth.admins). */
  admin?: boolean;
}[] = [
  { to: '/settings/agents', label: 'Agents', icon: Bot },
  { to: '/settings/secrets', label: 'Secrets', icon: LockKeyhole },
  { to: '/settings/tools', label: 'Tools', icon: Wrench },
  { to: '/settings/git', label: 'Git', icon: GitBranch },
  { to: '/settings/providers', label: 'Provider logins', icon: Plug },
  { to: '/settings/tokens', label: 'API tokens', icon: KeyRound },
  { to: '/settings/slack', label: 'Slack', icon: MessageSquare },
  { to: '/settings/nodes', label: 'Nodes', icon: Server, admin: true },
];

export function AccountSettings() {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const admin = useQuery(meQuery).data?.admin ?? false;
  return (
    <SettingsLayout
      navLabel="Account settings"
      crumbs={<Crumb current>Settings</Crumb>}
      nav={ACCOUNT_SETTINGS_NAV.filter((n) => !n.admin || admin).map((n) => (
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
