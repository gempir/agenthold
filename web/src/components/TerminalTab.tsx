import { useEffect, useRef, useState } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { Button } from '@/components/ui/button';
import { parseServerMessage, terminalUrl } from '@/lib/terminal';

type Status =
  | { kind: 'connecting' }
  | { kind: 'status'; message: string }
  | { kind: 'live' }
  | { kind: 'ended'; text: string };

export function TerminalTab({
  agentId,
  auth,
}: {
  agentId: string;
  auth?: string;
}) {
  const host = useRef<HTMLDivElement>(null);
  const [session, setSession] = useState(0);
  const [status, setStatus] = useState<Status>({ kind: 'connecting' });

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    setStatus({ kind: 'connecting' });
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: 'ui-monospace, Menlo, Consolas, monospace',
      theme: { background: '#0b0b0c' },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    fit.fit();

    const ws = new WebSocket(
      terminalUrl(globalThis.location, agentId, term.cols, term.rows, auth),
    );
    ws.binaryType = 'arraybuffer';
    const enc = new TextEncoder();
    let closed = false;
    const end = (text: string) => setStatus({ kind: 'ended', text });

    ws.onopen = () => setStatus({ kind: 'live' });
    ws.onmessage = (ev: MessageEvent) => {
      if (typeof ev.data === 'string') {
        const m = parseServerMessage(ev.data);
        if (!m) return;
        if (m.type === 'status')
          setStatus({ kind: 'status', message: m.message });
        else if (m.type === 'exit') end(`Session ended (exit ${m.code})`);
        else end(m.message || 'Terminal error');
      } else {
        term.write(new Uint8Array(ev.data as ArrayBuffer));
        setStatus((s) => (s.kind === 'ended' ? s : { kind: 'live' }));
      }
    };
    ws.onclose = () =>
      setStatus((s) =>
        closed || s.kind === 'ended'
          ? s
          : { kind: 'ended', text: 'Disconnected' },
      );

    const data = term.onData((d) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(enc.encode(d));
    });
    const resize = term.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN)
        ws.send(JSON.stringify({ type: 'resize', cols, rows }));
    });
    const ro = new ResizeObserver(() => {
      try {
        fit.fit();
      } catch {
        // element hidden
      }
    });
    ro.observe(el);

    return () => {
      closed = true;
      ro.disconnect();
      data.dispose();
      resize.dispose();
      ws.close();
      term.dispose();
    };
  }, [agentId, auth, session]);

  return (
    <div className="grid gap-2">
      <div
        ref={host}
        data-testid="terminal"
        className="h-[60vh] min-h-[300px] overflow-hidden rounded-md bg-[#0b0b0c] p-2"
      />
      <div className="flex items-center gap-3 text-sm text-muted-foreground">
        {status.kind === 'connecting' && <span>Connecting...</span>}
        {status.kind === 'status' && <span>{status.message}</span>}
        {status.kind === 'ended' && (
          <>
            <span role="status">{status.text}</span>
            <Button size="sm" onClick={() => setSession((s) => s + 1)}>
              Reconnect
            </Button>
          </>
        )}
      </div>
    </div>
  );
}
