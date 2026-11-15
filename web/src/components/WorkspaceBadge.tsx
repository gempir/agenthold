import { cn } from '@/lib/utils';

// Muted hues for workspace badges: colour marks identity, nothing else.
const HUES = [25, 60, 145, 190, 230, 265, 300, 340];

function hash(s: string) {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0;
  return Math.abs(h);
}

export function initials(name: string) {
  const words = name
    .trim()
    .split(/[\s_-]+/)
    .filter(Boolean);
  if (words.length === 0) return '?';
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (words[0][0] + words[1][0]).toUpperCase();
}

/** Small coloured square with a workspace's initials. */
export function WorkspaceBadge({
  id,
  name,
  className,
}: {
  id: string;
  name: string;
  className?: string;
}) {
  const hue = HUES[hash(id) % HUES.length];
  return (
    <span
      aria-hidden
      className={cn(
        'inline-flex size-4 shrink-0 items-center justify-center rounded-[4px] text-[8px] font-bold leading-none tracking-tight',
        className,
      )}
      style={{
        background: `oklch(0.62 0.13 ${hue} / 22%)`,
        color: `oklch(0.78 0.12 ${hue})`,
      }}
    >
      {initials(name)}
    </span>
  );
}
