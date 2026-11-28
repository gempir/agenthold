import { useQuery } from '@tanstack/react-query';
import { Link, Outlet, useRouterState } from '@tanstack/react-router';
import {
  Bot,
  FolderKanban,
  GitBranch,
  KeyRound,
  LayoutDashboard,
  Layers,
  Link2,
  LockKeyhole,
  MessageSquare,
  MessagesSquare,
  Plug,
  Server,
  UserRound,
  Wrench,
  type LucideIcon,
} from 'lucide-react';
import {
  SettingsLayout,
  SettingsNavItem,
} from '@/components/layout/SettingsLayout';
import { Crumb } from '@/components/layout/TopBar';
import { meQuery, versionQuery, type Version } from '@/api/queries';

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
  { to: '/settings/profile', label: 'Profile', icon: UserRound },
  { to: '/settings/threads', label: 'Threads', icon: MessagesSquare },
  { to: '/settings/projects', label: 'Projects', icon: FolderKanban },
  { to: '/settings/agents', label: 'Agents', icon: Bot },
  { to: '/settings/accounts', label: 'Accounts', icon: Link2 },
  { to: '/settings/secrets', label: 'Secrets', icon: LockKeyhole },
  { to: '/settings/tools', label: 'Tools', icon: Wrench },
  { to: '/settings/git', label: 'Git', icon: GitBranch },
  { to: '/settings/providers', label: 'Provider logins', icon: Plug },
  { to: '/settings/tokens', label: 'API tokens', icon: KeyRound },
  { to: '/settings/slack', label: 'Slack', icon: MessageSquare },
  {
    to: '/settings/overview',
    label: 'Overview',
    icon: LayoutDashboard,
    admin: true,
  },
  { to: '/settings/images', label: 'Images', icon: Layers, admin: true },
  { to: '/settings/nodes', label: 'Nodes', icon: Server, admin: true },
];

function formatVersion(v?: Version): string {
  if (!v) return '';
  const ver = v.version?.trim() ?? '';
  const commit = v.commit?.trim() ?? '';
  const shortCommit = commit && commit !== 'unknown' ? commit.slice(0, 7) : '';
  if (ver && shortCommit && !ver.includes(shortCommit)) {
    return `${ver} · ${shortCommit}`;
  }
  return ver || shortCommit;
}

export function AccountSettings() {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const admin = useQuery(meQuery).data?.admin ?? false;
  const version = useQuery(versionQuery);
  const versionLabel = formatVersion(version.data);
  const versionTitle = version.data
    ? [version.data.version, version.data.commit]
        .filter((s) => s && s !== 'unknown')
        .join(' · ')
    : undefined;
  return (
    <SettingsLayout
      navLabel="Account settings"
      wide={path.startsWith('/settings/overview')}
      crumbs={
        <Crumb current>
          <span>Settings</span>
          {versionLabel && (
            <span
              data-testid="app-version"
              title={versionTitle}
              className="truncate font-mono text-xs font-normal text-muted-foreground/70"
            >
              {versionLabel}
            </span>
          )}
        </Crumb>
      }
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
