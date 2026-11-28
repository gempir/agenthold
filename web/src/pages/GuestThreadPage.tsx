import { lazy, Suspense, useEffect, useReducer, useRef, useState } from 'react';
import {
  CircleAlert,
  Clock,
  Eye,
  Loader2,
  Send,
  Wifi,
  WifiOff,
} from 'lucide-react';
import type { GuestRequest, GuestThreadView } from '@/api/queries';
import { ToolCard } from '@/components/chat/ToolCard';
import { Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
  backoffMs,
  chatReducer,
  groupItems,
  guestStreamUrl,
  initialChatState,
  parseStreamMessage,
  type ChatItem,
} from '@/lib/session';

const Markdown = lazy(() => import('@/components/chat/Markdown'));

function GuestItem({ item }: { item: ChatItem }) {
  switch (item.kind) {
    case 'user':
      return (
        <div className="ml-auto flex max-w-[85%] flex-col items-end gap-1">
          {item.user && (
            <span className="px-1 text-xs text-muted-foreground">
              {item.user}
            </span>
          )}
          {item.text && (
            <p className="rounded-2xl rounded-br-md bg-secondary px-4 py-2.5 text-[15px] leading-6 whitespace-pre-wrap text-secondary-foreground">
              {item.text}
            </p>
          )}
        </div>
      );
    case 'agent':
      return (
        <div className="text-[15px] leading-7">
          <Suspense
            fallback={<p className="whitespace-pre-wrap">{item.text}</p>}
          >
            <Markdown text={item.text} />
          </Suspense>
        </div>
      );
    case 'thought':
      return (
        <p className="border-l pl-3 text-xs whitespace-pre-wrap text-muted-foreground">
          {item.text}
        </p>
      );
    case 'tool':
      return <ToolCard tool={item} />;
    case 'error':
      return (
        <p
          role="alert"
          className="flex items-start gap-2 rounded-xl border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive"
        >
          <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          {item.message}
        </p>
      );
    case 'notice':
      return (
        <p className="flex items-center gap-3 text-xs text-muted-foreground before:h-px before:flex-1 before:bg-border after:h-px after:flex-1 after:bg-border">
          {item.message}
        </p>
      );
    default:
      return null;
  }
}

export function GuestThreadPage({ token }: { token: string }) {
  const [meta, setMeta] = useState<Omit<
    GuestThreadView,
    'events' | 'pendingRequests'
  > | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [state, dispatch] = useReducer(chatReducer, initialChatState);
  const [running, setRunning] = useState(false);
  const [connected, setConnected] = useState(false);
  const [pendingRequests, setPendingRequests] = useState<GuestRequest[]>([]);

  const [guestName, setGuestName] = useState('');
  const [requestText, setRequestText] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [submittedNote, setSubmittedNote] = useState<string | null>(null);

  const lastSeqRef = useRef(0);

  useEffect(() => {
    let cancelled = false;
    let ws: WebSocket | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;

    const connectStream = () => {
      if (cancelled) return;
      const sock = new WebSocket(
        guestStreamUrl(globalThis.location, token, lastSeqRef.current),
      );
      ws = sock;
      sock.onopen = () => {
        attempt = 0;
        setConnected(true);
      };
      sock.onmessage = (e: MessageEvent) => {
        if (typeof e.data !== 'string') return;
        const m = parseStreamMessage(e.data);
        if (!m) return;
        if (m.type === 'status') {
          setRunning(m.running);
        } else if (m.type === 'guest_request') {
          setPendingRequests((prev) => {
            const exists = prev.some((r) => r.id === m.request.id);
            if (m.request.status !== 'pending') {
              return prev.filter((r) => r.id !== m.request.id);
            }
            return exists
              ? prev.map((r) => (r.id === m.request.id ? m.request : r))
              : [...prev, m.request];
          });
        } else if (m.type === 'event') {
          if (m.event.seq > lastSeqRef.current)
            lastSeqRef.current = m.event.seq;
          dispatch({ type: 'event', event: m.event });
        }
      };
      sock.onclose = (ev: CloseEvent) => {
        if (cancelled) return;
        setConnected(false);
        if (ev.code === 1008) {
          setLoadError('This guest link has been revoked or expired.');
          return;
        }
        timer = setTimeout(connectStream, backoffMs(attempt++));
      };
    };

    const load = async () => {
      setLoading(true);
      setLoadError(null);
      dispatch({ type: 'reset' });
      lastSeqRef.current = 0;
      try {
        const res = await fetch(`/api/v1/guest/${encodeURIComponent(token)}`);
        if (!res.ok) {
          setLoadError('This guest link is invalid, expired, or revoked.');
          setLoading(false);
          return;
        }
        const data = (await res.json()) as GuestThreadView;
        if (cancelled) return;
        setMeta({
          sessionId: data.sessionId,
          title: data.title,
          agentName: data.agentName,
          running: data.running,
          expiresAt: data.expiresAt,
        });
        setRunning(data.running);
        setPendingRequests(data.pendingRequests ?? []);
        for (const ev of data.events ?? []) {
          if (ev.seq > lastSeqRef.current) lastSeqRef.current = ev.seq;
          dispatch({ type: 'event', event: ev });
        }
        setLoading(false);
        connectStream();
      } catch {
        if (!cancelled) {
          setLoadError('Could not load this shared thread.');
          setLoading(false);
        }
      }
    };

    void load();

    return () => {
      cancelled = true;
      clearTimeout(timer);
      ws?.close();
    };
  }, [token]);

  const submitRequest = async (e: React.FormEvent) => {
    e.preventDefault();
    const text = requestText.trim();
    if (!text || submitting) return;
    setSubmitting(true);
    setSubmitError(null);
    setSubmittedNote(null);
    try {
      const res = await fetch(
        `/api/v1/guest/${encodeURIComponent(token)}/requests`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            ...(guestName.trim() ? { name: guestName.trim() } : {}),
            text,
          }),
        },
      );
      if (!res.ok) {
        const errBody = (await res.json().catch(() => ({}))) as {
          error?: string;
        };
        setSubmitError(errBody.error || 'Could not submit request.');
        return;
      }
      const created = (await res.json()) as GuestRequest;
      setPendingRequests((prev) =>
        prev.some((r) => r.id === created.id) ? prev : [...prev, created],
      );
      setRequestText('');
      setSubmittedNote('Message queued for host approval.');
    } catch {
      setSubmitError('Network error while submitting request.');
    } finally {
      setSubmitting(false);
    }
  };

  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <Loading />
      </div>
    );
  }

  if (loadError || !meta) {
    return (
      <div className="flex min-h-screen flex-col items-center justify-center gap-3 bg-background p-6 text-center">
        <CircleAlert className="size-8 text-destructive" aria-hidden />
        <h1 className="text-lg font-semibold">Guest link unavailable</h1>
        <p className="max-w-md text-sm text-muted-foreground">
          {loadError ?? 'This guest link is no longer active.'}
        </p>
      </div>
    );
  }

  const groups = groupItems(state.items);

  return (
    <div className="flex min-h-screen flex-col bg-background text-foreground">
      <header className="sticky top-0 z-10 flex flex-wrap items-center justify-between gap-2 border-b bg-background/95 px-4 py-3 backdrop-blur">
        <div className="flex min-w-0 items-center gap-2">
          <Eye className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <div className="min-w-0">
            <h1 className="truncate text-sm font-semibold">
              {meta.title || 'Shared thread'}
            </h1>
            <p className="text-xs text-muted-foreground">
              {meta.agentName ? `${meta.agentName} · ` : ''}Guest view
              (read-only)
            </p>
          </div>
        </div>
        <div className="flex items-center gap-3 text-xs text-muted-foreground">
          <span className="flex items-center gap-1">
            {connected ? (
              <Wifi className="size-3.5 text-emerald-500" aria-hidden />
            ) : (
              <WifiOff className="size-3.5 text-amber-500" aria-hidden />
            )}
            {connected ? 'Live' : 'Reconnecting…'}
          </span>
          <span className="hidden items-center gap-1 sm:flex">
            <Clock className="size-3.5" aria-hidden />
            Expires {new Date(meta.expiresAt).toLocaleString()}
          </span>
        </div>
      </header>

      <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col gap-5 px-4 py-6">
        {state.items.length === 0 && !running && (
          <p className="py-12 text-center text-sm text-muted-foreground">
            No messages yet.
          </p>
        )}
        {groups.map((g, i) =>
          g.kind === 'work' ? (
            <div
              key={`w-${g.items[0].seq}-${i}`}
              className="grid gap-1 border-l pl-3 text-xs text-muted-foreground"
            >
              {g.items.map((it, j) => (
                <GuestItem key={`${it.kind}-${it.seq}-${j}`} item={it} />
              ))}
            </div>
          ) : (
            <GuestItem
              key={`${g.item.kind}-${g.item.seq}-${i}`}
              item={g.item}
            />
          ),
        )}
        {running && (
          <p
            role="status"
            className="flex items-center gap-2 text-sm text-muted-foreground"
          >
            <Loader2
              className="size-3.5 animate-spin text-sky-400"
              aria-hidden
            />
            Working…
          </p>
        )}

        {pendingRequests.length > 0 && (
          <section
            aria-label="Pending guest requests"
            className="grid gap-2 rounded-xl border border-amber-500/30 bg-amber-500/5 p-3 text-sm"
          >
            <h2 className="text-xs font-medium text-amber-700 dark:text-amber-400">
              Pending host approval ({pendingRequests.length})
            </h2>
            {pendingRequests.map((r) => (
              <div
                key={r.id}
                className="rounded-lg border bg-background px-3 py-2 text-xs"
              >
                <span className="font-medium text-foreground">
                  {r.guestName}
                </span>
                <p className="mt-0.5 whitespace-pre-wrap text-muted-foreground">
                  {r.text}
                </p>
              </div>
            ))}
          </section>
        )}
      </main>

      <footer className="sticky bottom-0 border-t bg-background/95 px-4 py-3 backdrop-blur">
        <form
          aria-label="Request message"
          onSubmit={(e) => void submitRequest(e)}
          className="mx-auto grid w-full max-w-3xl gap-2"
        >
          <div className="flex flex-wrap items-center gap-2">
            <Input
              aria-label="Your name"
              placeholder="Your name (optional)"
              value={guestName}
              onChange={(e) => setGuestName(e.target.value)}
              className="h-8 w-44 text-xs"
            />
            <span className="text-xs text-muted-foreground">
              Messages from guests wait for the thread owner to approve before
              running.
            </span>
          </div>
          <div className="flex gap-2">
            <textarea
              aria-label="Message to request"
              rows={2}
              placeholder="Suggest a message for the agent…"
              value={requestText}
              onChange={(e) => setRequestText(e.target.value)}
              className="min-h-11 flex-1 resize-y rounded-lg border border-input bg-transparent px-3 py-2 text-sm placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
            />
            <Button
              type="submit"
              className="self-end"
              disabled={!requestText.trim() || submitting}
            >
              <Send className="size-3.5" aria-hidden />
              Request message
            </Button>
          </div>
          {submitError && (
            <p role="alert" className="text-xs text-destructive">
              {submitError}
            </p>
          )}
          {submittedNote && (
            <p
              role="status"
              className="text-xs text-emerald-600 dark:text-emerald-400"
            >
              {submittedNote}
            </p>
          )}
        </form>
      </footer>
    </div>
  );
}
