import { harnessBrand, type Brand } from '@/lib/harnesses';
import { cn } from '@/lib/utils';

/**
 * Small logos for harnesses and model providers, drawn inline so they follow
 * the theme (the marks use currentColor; Claude and Gemini keep their hue).
 * Decorative: the name next to them or the control's label says what they are.
 */
export function BrandIcon({
  brand,
  className,
}: {
  brand: Brand;
  className?: string;
}) {
  const cls = cn('size-4 shrink-0', className);
  switch (brand) {
    case 'anthropic':
      // The Claude spark: rays of uneven length around a centre.
      return (
        <svg viewBox="0 0 24 24" className={cls} aria-hidden>
          <g
            stroke="#D97757"
            strokeWidth="2.4"
            strokeLinecap="round"
            fill="none"
          >
            <path d="M12 3.2v5.3" />
            <path d="M12 15.5v5.3" />
            <path d="M3.2 12h5.3" />
            <path d="M15.5 12h5.3" />
            <path d="m5.8 5.8 3.6 3.6" />
            <path d="m14.6 14.6 3.6 3.6" />
            <path d="m18.2 5.8-3.6 3.6" />
            <path d="m9.4 14.6-3.6 3.6" />
            <path d="m8.2 3.9 1.5 4.4" />
            <path d="m15.8 20.1-1.5-4.4" />
            <path d="m20.1 8.2-4.4 1.5" />
            <path d="m3.9 15.8 4.4-1.5" />
          </g>
        </svg>
      );
    case 'openai':
      // The knot: three interlaced loops at 60° steps.
      return (
        <svg viewBox="0 0 24 24" className={cls} aria-hidden>
          <g fill="none" stroke="currentColor" strokeWidth="1.7">
            {[0, 60, 120].map((r) => (
              <rect
                key={r}
                x="8.2"
                y="2.6"
                width="7.6"
                height="18.8"
                rx="3.8"
                transform={`rotate(${r} 12 12)`}
              />
            ))}
          </g>
        </svg>
      );
    case 'opencode':
      // A terminal block: the outer frame and the cursor inside it.
      return (
        <svg viewBox="0 0 24 24" className={cls} aria-hidden>
          <path
            fill="currentColor"
            fillRule="evenodd"
            d="M4 3h16v18H4zm3 3v12h10V6z"
          />
          <path fill="currentColor" opacity=".55" d="M9 10h6v6H9z" />
        </svg>
      );
    case 'google':
      // The four-pointed Gemini star.
      return (
        <svg viewBox="0 0 24 24" className={cls} aria-hidden>
          <defs>
            <linearGradient id="ah-gemini" x1="0" y1="1" x2="1" y2="0">
              <stop offset="0" stopColor="#4285F4" />
              <stop offset="1" stopColor="#9B72CB" />
            </linearGradient>
          </defs>
          <path
            fill="url(#ah-gemini)"
            d="M12 2c.6 5.2 4.8 9.4 10 10-5.2.6-9.4 4.8-10 10-.6-5.2-4.8-9.4-10-10 5.2-.6 9.4-4.8 10-10z"
          />
        </svg>
      );
    default:
      // A generic agent: a rounded head with two eyes.
      return (
        <svg
          viewBox="0 0 24 24"
          className={cls}
          aria-hidden
          fill="none"
          stroke="currentColor"
          strokeWidth="1.8"
          strokeLinecap="round"
        >
          <rect x="4" y="7" width="16" height="12" rx="3" />
          <path d="M12 3v4M9.5 12.5v1.5M14.5 12.5v1.5" />
        </svg>
      );
  }
}

/** The logo of a harness by its id ("claude-code", "codex", "antigravity"). */
export function HarnessIcon({
  harness,
  className,
}: {
  harness: string | undefined;
  className?: string;
}) {
  return (
    <BrandIcon brand={harnessBrand(harness ?? '')} className={className} />
  );
}
