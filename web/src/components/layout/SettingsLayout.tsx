import type { ReactNode } from 'react';
import { Slot } from '@radix-ui/react-slot';
import { TopBar } from '@/components/layout/TopBar';
import { cn } from '@/lib/utils';

/**
 * Frame for every settings surface (account, workspace, agent): breadcrumb
 * bar, a section list on the left (a scrolling strip on phones) and a
 * readable content column.
 */
export function SettingsLayout({
  crumbs,
  actions,
  nav,
  navLabel,
  children,
}: {
  crumbs: ReactNode;
  actions?: ReactNode;
  nav: ReactNode;
  navLabel: string;
  children: ReactNode;
}) {
  return (
    <>
      <TopBar actions={actions}>{crumbs}</TopBar>
      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto md:flex-row md:overflow-hidden">
        <nav
          aria-label={navLabel}
          className="shrink-0 overflow-x-auto border-b px-3 pb-2 md:w-56 md:overflow-y-auto md:border-b-0 md:px-3 md:pt-4"
        >
          <ul className="flex gap-0.5 md:flex-col">{nav}</ul>
        </nav>
        <div className="min-w-0 flex-1 md:overflow-y-auto">
          <div className="w-full max-w-3xl px-4 py-6 md:px-10 md:py-8">
            {children}
          </div>
        </div>
      </div>
    </>
  );
}

/** A section entry; wrap a Link or button. */
export function SettingsNavItem({
  active,
  children,
}: {
  active?: boolean;
  children: ReactNode;
}) {
  return (
    <li className="shrink-0">
      <Slot
        aria-current={active ? 'page' : undefined}
        className={cn(
          'flex items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-sm whitespace-nowrap',
          active
            ? 'bg-accent font-medium text-foreground'
            : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
        )}
      >
        {children}
      </Slot>
    </li>
  );
}

export function SettingsHeader({
  title,
  description,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <header className="mb-6 flex flex-wrap items-start gap-3">
      <div className="min-w-0 flex-1">
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {description && (
          <p className="mt-1 text-sm text-muted-foreground">{description}</p>
        )}
      </div>
      {children}
    </header>
  );
}
