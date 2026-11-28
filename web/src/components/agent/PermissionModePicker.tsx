import {
  Check,
  ChevronDown,
  Compass,
  FileCheck2,
  Shield,
  ShieldOff,
} from 'lucide-react';
import type { PermissionMode } from '@/api/queries';
import { Menu, MenuItem } from '@/components/ui/menu';
import { cn } from '@/lib/utils';

export interface PermissionModeOption {
  value: PermissionMode;
  label: string;
  shortLabel: string;
  description: string;
}

export const PERMISSION_MODES: PermissionModeOption[] = [
  {
    value: 'full-access',
    label: 'Full access',
    shortLabel: 'Full access',
    description: 'Run tools and edit files without asking',
  },
  {
    value: 'auto-accept-edits',
    label: 'Auto-accept edits',
    shortLabel: 'Auto-accept edits',
    description: 'Auto-approve file edits; ask before shell commands',
  },
  {
    value: 'supervised',
    label: 'Supervised',
    shortLabel: 'Supervised',
    description: 'Ask before running tools or editing files',
  },
  {
    value: 'plan',
    label: 'Plan',
    shortLabel: 'Plan',
    description: 'Read-only planning mode; no edits or commands',
  },
];

function ModeIcon({
  mode,
  className,
}: {
  mode: PermissionMode;
  className?: string;
}) {
  switch (mode) {
    case 'full-access':
      return <ShieldOff className={className} aria-hidden />;
    case 'auto-accept-edits':
      return <FileCheck2 className={className} aria-hidden />;
    case 'supervised':
      return <Shield className={className} aria-hidden />;
    case 'plan':
      return <Compass className={className} aria-hidden />;
  }
}

export function permissionModeLabel(mode: PermissionMode): string {
  return PERMISSION_MODES.find((m) => m.value === mode)?.label ?? 'Full access';
}

/**
 * Sleek permission-mode pill placed beside the model picker in the composer
 * footer bar.
 */
export function PermissionModePicker({
  value = 'full-access',
  onChange,
  disabled,
}: {
  value?: PermissionMode;
  onChange: (mode: PermissionMode) => void;
  disabled?: boolean;
}) {
  const active =
    PERMISSION_MODES.find((m) => m.value === value) ?? PERMISSION_MODES[0];

  return (
    <Menu
      side="top"
      trigger={({ open, toggle }) => (
        <button
          type="button"
          aria-label={`Permission mode: ${active.label}`}
          aria-haspopup="menu"
          aria-expanded={open}
          disabled={disabled}
          title={active.description}
          onClick={toggle}
          className={cn(
            'flex shrink-0 items-center gap-1 rounded-md px-1.5 py-1 text-xs transition-colors hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-60',
            active.value === 'plan'
              ? 'text-sky-600 dark:text-sky-400'
              : active.value === 'supervised'
                ? 'text-amber-600 dark:text-amber-400'
                : 'text-muted-foreground',
          )}
        >
          <ModeIcon mode={active.value} className="size-3.5 shrink-0" />
          <span className="max-w-28 truncate">{active.shortLabel}</span>
          <ChevronDown className="size-3 shrink-0 opacity-70" aria-hidden />
        </button>
      )}
      className="w-64 max-w-[90vw]"
    >
      {(close) =>
        PERMISSION_MODES.map((opt) => (
          <MenuItem
            key={opt.value}
            active={opt.value === active.value}
            onClick={() => {
              onChange(opt.value);
              close();
            }}
            className="items-start py-2"
          >
            <ModeIcon
              mode={opt.value}
              className="mt-0.5 size-3.5 shrink-0 text-muted-foreground"
            />
            <div className="min-w-0 flex-1">
              <div className="flex items-center justify-between gap-2 text-xs font-medium text-foreground">
                <span>{opt.label}</span>
                {opt.value === active.value && (
                  <Check className="size-3.5 shrink-0" aria-hidden />
                )}
              </div>
              <p className="mt-0.5 text-[11px] leading-snug text-muted-foreground">
                {opt.description}
              </p>
            </div>
          </MenuItem>
        ))
      }
    </Menu>
  );
}
