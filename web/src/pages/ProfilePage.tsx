import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Bot, LogOut, Moon, Sun } from 'lucide-react';
import { meQuery } from '@/api/queries';
import { Avatar } from '@/components/Avatar';
import { SettingsHeader } from '@/components/layout/SettingsLayout';
import { useShell } from '@/components/layout/shell';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { useLogout } from '@/hooks/useLogout';
import { ROLE_LABEL } from '@/lib/roles';
import { getTheme, setTheme, type Theme } from '@/lib/theme';
import { cn } from '@/lib/utils';

/** Account settings: who you are, the theme, new agents and signing out. */
export function ProfilePage() {
  const me = useQuery(meQuery);
  const shell = useShell();
  const logout = useLogout();
  const [theme, setThemeState] = useState<Theme>(getTheme);
  const user = me.data?.user;
  const pick = (t: Theme) => {
    setTheme(t);
    setThemeState(t);
  };
  return (
    <>
      <SettingsHeader title="Profile" />
      <div className="grid gap-4">
        <Card className="flex flex-wrap items-center gap-3 p-4">
          {user && <Avatar name={user.name} url={user.avatarUrl} />}
          <div className="grid min-w-0 flex-1 leading-tight">
            <span className="truncate font-medium">{user?.name}</span>
            <span className="truncate text-sm text-muted-foreground">
              {[user?.email, me.data?.role && ROLE_LABEL[me.data.role]]
                .filter(Boolean)
                .join(' · ')}
            </span>
          </div>
          <Button
            variant="outline"
            size="sm"
            disabled={logout.isPending}
            onClick={() => logout.mutate()}
          >
            <LogOut className="size-3.5" aria-hidden />
            Sign out
          </Button>
        </Card>

        <Card className="flex flex-wrap items-center gap-3 p-4">
          <div className="min-w-0 flex-1">
            <p className="text-sm font-medium">Theme</p>
          </div>
          <div
            role="radiogroup"
            aria-label="Theme"
            className="flex rounded-lg border p-0.5"
          >
            {(
              [
                ['dark', 'Dark', Moon],
                ['light', 'Light', Sun],
              ] as const
            ).map(([t, label, Icon]) => (
              <button
                key={t}
                type="button"
                role="radio"
                aria-checked={theme === t}
                onClick={() => pick(t)}
                className={cn(
                  'flex items-center gap-1.5 rounded-md px-2.5 py-1 text-sm',
                  theme === t
                    ? 'bg-accent text-foreground'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                <Icon className="size-3.5" aria-hidden />
                {label}
              </button>
            ))}
          </div>
        </Card>

        <Card className="flex flex-wrap items-center gap-3 p-4">
          <div className="min-w-0 flex-1">
            <p className="text-sm font-medium">Agents</p>
            <p className="text-sm text-muted-foreground">
              A sandboxed machine with a harness of your choice.
            </p>
          </div>
          <Button size="sm" onClick={shell.openNewAgent}>
            <Bot className="size-3.5" aria-hidden />
            New agent
          </Button>
        </Card>
      </div>
    </>
  );
}
