import { cn } from '@/lib/utils';

/** Up to two letters for an agent: "web-dev" → "WD", "builder" → "BU". */
export function initials(name: string): string {
  const words = name
    .split(/[^\p{L}\p{N}]+/u)
    .filter(Boolean)
    .map((w) => [...w]);
  if (words.length === 0) return '?';
  const two =
    words.length > 1 ? [words[0][0], words[1][0]] : words[0].slice(0, 2);
  return two.join('').toUpperCase();
}

/** An agent's initials in a small rounded square. */
export function AgentBadge({
  name,
  className,
}: {
  name: string;
  className?: string;
}) {
  return (
    <span
      aria-hidden
      className={cn(
        'flex size-4.5 shrink-0 items-center justify-center rounded-[5px] border bg-muted text-[9px] leading-none font-semibold tracking-tight text-foreground/80',
        className,
      )}
    >
      {initials(name)}
    </span>
  );
}
