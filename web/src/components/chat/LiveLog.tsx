import { useEffect, useRef, useState } from 'react';
import { ScrollText } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import type { components } from '@/api/schema';
import { cn } from '@/lib/utils';

export type ActivityEntry = components['schemas']['ActivityEntry'];

/**
 * Polls an agent's activity log; `live` keeps polling once a second (not
 * while the page is hidden).
 */
export function useActivity(agentId: string, live: boolean) {
  const [entries, setEntries] = useState<ActivityEntry[]>([]);
  const [error, setError] = useState<string>();
  const after = useRef(0);
  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      try {
        const got = await unwrap(
          api.GET('/agents/{agentId}/activity', {
            params: { path: { agentId }, query: { after: after.current } },
          }),
        );
        if (stopped) return;
        setError(undefined);
        if (got.length > 0) {
          after.current = got[got.length - 1].seq;
          setEntries((prev) => [...prev, ...got].slice(-500));
        }
      } catch (e) {
        if (!stopped) setError(e instanceof Error ? e.message : String(e));
      }
      if (live && !stopped) timer = setTimeout(tick, 1000);
    };
    const tick = () => {
      if (document.hidden) timer = setTimeout(tick, 1000);
      else void poll();
    };
    void poll();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
    };
  }, [agentId, live]);
  return { entries, error };
}

const levelClass: Record<ActivityEntry['level'], string> = {
  debug: 'text-muted-foreground/70',
  info: 'text-foreground/90',
  warn: 'text-amber-500',
  error: 'text-destructive',
};

function clock(at: string) {
  const d = new Date(at);
  return (
    d.toLocaleTimeString([], { hour12: false }) +
    '.' +
    String(d.getMilliseconds()).padStart(3, '0')
  );
}

/**
 * The agent's live activity: what the platform is doing for it right now
 * (wake, guest boot console, tunnel, harness start with timings, harness
 * stderr). Newest at the bottom, follows new lines while scrolled down.
 */
export function LiveLog({
  agentId,
  live,
  fill,
}: {
  agentId: string;
  live: boolean;
  /** Take the height of the parent (a flex column) instead of a fixed one. */
  fill?: boolean;
}) {
  const { entries, error } = useActivity(agentId, live);
  const box = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const [showDebug, setShowDebug] = useState(true);
  useEffect(() => {
    const el = box.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [entries, showDebug]);
  const shown = showDebug
    ? entries
    : entries.filter((e) => e.level !== 'debug');
  return (
    <div
      className={cn(
        'grid gap-1.5',
        fill && 'min-h-0 flex-1 grid-rows-[auto_1fr]',
      )}
    >
      <div className="flex items-center gap-3 text-xs text-muted-foreground">
        <span>
          {live ? 'Live' : 'Recent'} activity · {entries.length} lines
        </span>
        <label className="flex items-center gap-1">
          <input
            type="checkbox"
            checked={showDebug}
            onChange={(e) => setShowDebug(e.target.checked)}
          />
          console and stderr
        </label>
      </div>
      <div
        ref={box}
        data-testid="live-log"
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
        className={cn(
          'overflow-auto rounded-lg border bg-muted/40 p-2 font-mono text-[11px] leading-relaxed',
          fill ? 'min-h-0' : 'max-h-80',
        )}
      >
        {error && <p className="text-destructive">{error}</p>}
        {shown.length === 0 && !error && (
          <p className="text-muted-foreground">Nothing logged yet.</p>
        )}
        {shown.map((e) => (
          <div
            key={e.seq}
            className={cn(
              'break-words whitespace-pre-wrap',
              levelClass[e.level],
            )}
          >
            <span className="text-muted-foreground/70">{clock(e.at)}</span>{' '}
            <span className="text-sky-500">{e.source}</span> {e.message}
          </div>
        ))}
      </div>
    </div>
  );
}

/** A quiet row in the chat that opens the Activity tab of the agent panel. */
export function ActivityLink({
  label,
  onOpen,
}: {
  label: string;
  onOpen: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="flex items-center gap-2 rounded-md py-1 text-left text-xs text-muted-foreground hover:text-foreground"
    >
      <ScrollText className="size-3.5" aria-hidden />
      <span>{label}</span>
    </button>
  );
}
