import { useCallback, useEffect, useReducer, useRef, useState } from 'react';
import {
  backoffMs,
  chatReducer,
  initialChatState,
  parseStreamMessage,
  sessionStreamUrl,
  type QueuedMessage,
  type Viewer,
} from '@/lib/session';

/** Streams a session's events over a WebSocket, reconnecting with backoff and
 * resuming from the last seq seen. */
export function useSessionStream(
  sessionId: string,
  initialRunning: boolean,
  onQueueUpdated?: (messages: QueuedMessage[]) => void,
) {
  const [state, dispatch] = useReducer(chatReducer, initialChatState);
  const [running, setRunning] = useState(initialRunning);
  const [connected, setConnected] = useState(false);
  const [viewers, setViewers] = useState<Viewer[]>([]);
  const [reloadNonce, setReloadNonce] = useState(0);
  const queueHandler = useRef(onQueueUpdated);
  useEffect(() => {
    queueHandler.current = onQueueUpdated;
  }, [onQueueUpdated]);

  const reload = useCallback(() => {
    setReloadNonce((n) => n + 1);
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
        else {
          if (m.event.seq > lastSeq) lastSeq = m.event.seq;
          dispatch({ type: 'event', event: m.event });
        }
      };
      sock.onclose = () => {
        if (cancelled) return;
        setConnected(false);
        setViewers([]);
        timer = setTimeout(connect, backoffMs(attempt++));
      };
    };
    connect();

    return () => {
      cancelled = true;
      clearTimeout(timer);
      ws?.close();
      setViewers([]);
    };
  }, [sessionId, reloadNonce]);

  return { state, running, connected, viewers, reload };
}
