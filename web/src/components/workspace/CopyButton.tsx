import { useEffect, useState } from 'react';
import { Check, Copy } from 'lucide-react';
import { cn } from '@/lib/utils';

/** Copies text to the clipboard and says so for a moment. */
export function CopyButton({
  text,
  label = 'Copy',
  className,
  showLabel,
}: {
  text: string | (() => string);
  label?: string;
  className?: string;
  showLabel?: boolean;
}) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);
  return (
    <button
      type="button"
      aria-label={copied ? 'Copied' : label}
      title={copied ? 'Copied' : label}
      onClick={() => {
        const v = typeof text === 'function' ? text() : text;
        void navigator.clipboard
          ?.writeText(v)
          .then(() => setCopied(true))
          .catch(() => {});
      }}
      className={cn(
        'inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground',
        className,
      )}
    >
      {copied ? (
        <Check className="size-3.5" aria-hidden />
      ) : (
        <Copy className="size-3.5" aria-hidden />
      )}
      {showLabel && <span>{copied ? 'Copied' : label}</span>}
    </button>
  );
}
