import type { ReactNode } from 'react';
import { Link, useParams, useSearch } from '@tanstack/react-router';
import { PanelLeft, SquarePen } from 'lucide-react';
import type { PanelTab } from '@/components/agent/AgentPanel';
import { Button } from '@/components/ui/button';
import { useShell } from '@/components/layout/shell';
import { cn } from '@/lib/utils';

/**
 * The thin bar above every main view: sidebar toggle, breadcrumb, actions,
 * and New thread (with the agent in view, else the new-thread page).
 */
export function TopBar({
  children,
  actions,
  className,
  newThread = true,
}: {
  children?: ReactNode;
  actions?: ReactNode;
  className?: string;
  /** Show the New thread button (not on the new-thread page itself). */
  newThread?: boolean;
}) {
  const { collapsed, setCollapsed, setDrawerOpen } = useShell();
  return (
    <div
      className={cn(
        'flex h-13 shrink-0 items-center gap-2 px-3 md:px-4',
        className,
      )}
    >
      <Button
        variant="ghost"
        size="icon-sm"
        className="text-muted-foreground md:hidden"
        aria-label="Open sidebar"
        onClick={() => setDrawerOpen(true)}
      >
        <PanelLeft className="size-4" />
      </Button>
      {collapsed && (
        <Button
          variant="ghost"
          size="icon-sm"
          className="hidden text-muted-foreground md:inline-flex"
          aria-label="Show sidebar"
          title="Show sidebar"
          onClick={() => setCollapsed(false)}
        >
          <PanelLeft className="size-4" />
        </Button>
      )}
      <div className="flex min-w-0 flex-1 items-center gap-2 text-sm">
        {children}
      </div>
      {(actions || newThread) && (
        <div className="flex shrink-0 items-center gap-0.5">
          {actions}
          {newThread && <NewThreadButton />}
        </div>
      )}
    </div>
  );
}

export function Crumb({
  children,
  current,
  className,
}: {
  children: ReactNode;
  current?: boolean;
  className?: string;
}) {
  return (
    <span
      className={cn(
        'flex min-w-0 items-center gap-1.5',
        current ? 'font-medium text-foreground' : 'text-muted-foreground',
        className,
      )}
    >
      {children}
    </span>
  );
}

export function CrumbSep({ className }: { className?: string }) {
  return (
    <span aria-hidden className={cn('text-muted-foreground/60', className)}>
      /
    </span>
  );
}

function NewThreadButton() {
  const { agentId } = useParams({ strict: false }) as { agentId?: string };
  const { panel } = useSearch({ strict: false }) as { panel?: PanelTab };
  const cls =
    'inline-flex size-8 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground';
  return agentId ? (
    <Link
      to="/a/$agentId"
      params={{ agentId }}
      search={{ panel }}
      aria-label="New thread"
      title="New thread"
      className={cls}
    >
      <SquarePen className="size-4" />
    </Link>
  ) : (
    <Link to="/new" aria-label="New thread" title="New thread" className={cls}>
      <SquarePen className="size-4" />
    </Link>
  );
}
