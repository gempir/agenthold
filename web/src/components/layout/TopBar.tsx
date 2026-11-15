import type { ReactNode } from 'react';
import { PanelLeft } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { useShell } from '@/components/layout/shell';
import { cn } from '@/lib/utils';

/** The thin bar above every main view: sidebar toggle, breadcrumb, actions. */
export function TopBar({
  children,
  actions,
  className,
}: {
  children?: ReactNode;
  actions?: ReactNode;
  className?: string;
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
      {actions && (
        <div className="flex shrink-0 items-center gap-0.5">{actions}</div>
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
