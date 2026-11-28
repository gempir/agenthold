import { useCallback, useEffect, useReducer, useRef, useState } from 'react';
import type { GuestRequest, SessionReviewComment } from '@/api/queries';
import {
  backoffMs,
  chatReducer,
  initialChatState,
  parseStreamMessage,
  sessionStreamUrl,
  type QueuedMessage,
  type Viewer,
  type ViewerActivity,
} from '@/lib/session';

export type SessionStreamCallbacks = {
  onGuestRequest?: (req: GuestRequest) => void;
  onReviewCommentsUpdated?: (comments: SessionReviewComment[]) => void;
};

/** Streams a session's events over a WebSocket, reconnecting with backoff and
 * resuming from the last seq seen. */
export function useSessionStream(
  sessionId: string,
  initialRunning: boolean,
  onQueueUpdated?: (messages: QueuedMessage[]) => void,
  callbacks?: SessionStreamCallbacks,
) {
  const [state, dispatch] = useReducer(chatReducer, initialChatState);
  const [running, setRunning] = useState(initialRunning);
  const [connected, setConnected] = useState(false);
  const [viewers, setViewers] = useState<Viewer[]>([]);
  const [reloadNonce, setReloadNonce] = useState(0);
  const wsRef = useRef<WebSocket | null>(null);
  const queueHandler = useRef(onQueueUpdated);
  const guestHandler = useRef(callbacks?.onGuestRequest);
  const reviewHandler = useRef(callbacks?.onReviewCommentsUpdated);
  useEffect(() => {
    queueHandler.current = onQueueUpdated;
    guestHandler.current = callbacks?.onGuestRequest;
    reviewHandler.current = callbacks?.onReviewCommentsUpdated;
  }, [
    onQueueUpdated,
    callbacks?.onGuestRequest,
    callbacks?.onReviewCommentsUpdated,
  ]);

  const reload = useCallback(() => {
    setReloadNonce((n) => n + 1);
  }, []);

  const sendPresence = useCallback((activity: ViewerActivity) => {
    const sock = wsRef.current;
    if (sock && typeof sock.send === 'function') {
      try {
        if (sock.readyState === undefined || sock.readyState === 1) {
          sock.send(JSON.stringify({ type: 'presence_update', activity }));
        }
      } catch {
        // ignore if socket is closing
      }
    }
  }, []);

  useEffect(() => {
    dispatch({ type: 'reset' });
    let cancelled = false;
    let ws: WebSocket | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    let lastSeq = 0;

    const connect = () => {
      if (cancelled) return;
      const sock = new WebSocket(
        sessionStreamUrl(globalThis.location, sessionId, lastSeq),
      );
      ws = sock;
      wsRef.current = sock;
      sock.onopen = () => {
        attempt = 0;
        setConnected(true);
      };
      sock.onmessage = (e: MessageEvent) => {
        if (typeof e.data !== 'string') return;
        const m = parseStreamMessage(e.data);
        if (!m) return;
        if (m.type === 'status') setRunning(m.running);
        else if (m.type === 'presence') setViewers(m.viewers);
        else if (m.type === 'queue_updated') queueHandler.current?.(m.messages);
        else if (m.type === 'guest_request') guestHandler.current?.(m.request);
        else if (m.type === 'review_comments_updated')
          reviewHandler.current?.(m.comments);
        else {
          if (m.event.seq > lastSeq) lastSeq = m.event.seq;
          dispatch({ type: 'event', event: m.event });
        }
      };
      sock.onclose = () => {
        if (cancelled) return;
        if (wsRef.current === sock) wsRef.current = null;
        setConnected(false);
        setViewers([]);
        timer = setTimeout(connect, backoffMs(attempt++));
      };
    };
    connect();

    return () => {
      cancelled = true;
      clearTimeout(timer);
      if (wsRef.current === ws) wsRef.current = null;
      ws?.close();
      setViewers([]);
    };
  }, [sessionId, reloadNonce]);

  return { state, running, connected, viewers, reload, sendPresence };
}
