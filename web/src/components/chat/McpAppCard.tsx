import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from 'react';
import * as DialogPrimitive from '@radix-ui/react-dialog';
import {
  AppWindow,
  Check,
  Loader2,
  Maximize2,
  ShieldAlert,
  X,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { Button } from '@/components/ui/button';
import type { McpAppAction, McpAppPayload } from '@/lib/session';
import {
  buildSandboxedHtml,
  clampVisualHeight,
  parseMcpAppMessage,
  themeVars,
  useCurrentTheme,
  type McpAppActionRequest,
} from '@/lib/visualSandbox';

export interface McpAppExecutionContext {
  app: McpAppPayload;
  request: McpAppActionRequest;
  readOnly: boolean;
}

function buildActionPrompt(
  app: McpAppPayload,
  request: McpAppActionRequest,
): string {
  if (request.prompt) return request.prompt;
  const serverPart = app.server ? ` on MCP server "${app.server}"` : '';
  const paramsPart =
    request.params && Object.keys(request.params).length > 0
      ? `\nArguments:\n\`\`\`json\n${JSON.stringify(request.params, null, 2)}\n\`\`\``
      : '';
  if (request.kind === 'tool') {
    return `Run MCP tool "${request.action}"${serverPart} requested by MCP App "${app.title}".${paramsPart}`;
  }
  return `Execute action "${request.action}"${serverPart} from MCP App "${app.title}".${paramsPart}`;
}

/**
 * Renders an interactive MCP App UI inside a tightly sandboxed iframe
 * (`sandbox="allow-scripts"` only). Any non-read-only action sent via
 * `postMessage` is intercepted with an explicit inline Allow / Decline
 * safety confirmation banner before executing.
 */
export function McpAppCard({
  app,
  sessionId,
  canUse,
  running = false,
  onExecuteAction,
}: {
  app: McpAppPayload;
  sessionId: string;
  canUse: boolean;
  running?: boolean;
  onExecuteAction?: (ctx: McpAppExecutionContext) => Promise<unknown> | unknown;
}) {
  const frameId = useId();
  const theme = useCurrentTheme();
  const [initialTheme] = useState(theme);
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const fullscreenIframeRef = useRef<HTMLIFrameElement>(null);

  const [height, setHeight] = useState<number>(() =>
    clampVisualHeight(app.height),
  );
  const [fullscreen, setFullscreen] = useState(false);
  const [pending, setPending] = useState<{
    request: McpAppActionRequest;
    actionMeta?: McpAppAction;
    sourceWindow: MessageEventSource | null;
  } | null>(null);
  const [executing, setExecuting] = useState(false);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  const srcDoc = useMemo(
    () =>
      buildSandboxedHtml(app.html, {
        frameId,
        theme: initialTheme,
        enableMcpBridge: true,
      }),
    [app.html, frameId, initialTheme],
  );

  const syncThemeTo = useCallback(
    (frame: HTMLIFrameElement | null) => {
      try {
        frame?.contentWindow?.postMessage(
          {
            type: 'agenthold:theme',
            frameId,
            theme,
            vars: themeVars(theme),
          },
          '*',
        );
      } catch {
        // iframe not ready
      }
    },
    [frameId, theme],
  );

  useEffect(() => {
    syncThemeTo(iframeRef.current);
    syncThemeTo(fullscreenIframeRef.current);
  }, [syncThemeTo]);

  const replyToFrame = useCallback(
    (
      target: MessageEventSource | null,
      request: McpAppActionRequest,
      payload: {
        ok: boolean;
        result?: unknown;
        error?: string;
        declined?: boolean;
      },
    ) => {
      const win =
        (target as Window | null) ?? iframeRef.current?.contentWindow ?? null;
      if (!win || typeof win.postMessage !== 'function') return;
      try {
        if (request.requestId !== undefined) {
          win.postMessage(
            {
              type: 'agenthold:mcp_result',
              frameId,
              requestId: request.requestId,
              ...payload,
            },
            '*',
          );
          if (request.jsonrpc) {
            win.postMessage(
              payload.ok
                ? {
                    jsonrpc: '2.0',
                    id: request.requestId,
                    result: payload.result ?? { ok: true },
                  }
                : {
                    jsonrpc: '2.0',
                    id: request.requestId,
                    error: {
                      code: payload.declined ? -32001 : -32000,
                      message: payload.error ?? 'Action failed',
                    },
                  },
              '*',
            );
          }
        }
      } catch {
        // iframe detached
      }
    },
    [frameId],
  );

  const runAction = useCallback(
    async (
      request: McpAppActionRequest,
      sourceWindow: MessageEventSource | null,
      readOnly: boolean,
    ) => {
      setExecuting(true);
      setErrorMessage(null);
      try {
        let result: unknown = { ok: true };
        if (onExecuteAction) {
          result = await onExecuteAction({ app, request, readOnly });
        } else if (request.kind === 'open_link' && request.url) {
          window.open(request.url, '_blank', 'noopener,noreferrer');
          result = { opened: request.url };
        } else if (sessionId) {
          if (!canUse) {
            throw new Error('You have read-only access to this session.');
          }
          const text = buildActionPrompt(app, request);
          if (running) {
            await unwrap(
              api.POST('/sessions/{sessionId}/queue', {
                params: { path: { sessionId } },
                body: { text, steer: false },
              }),
            );
            result = { queued: true };
          } else {
            await unwrap(
              api.POST('/sessions/{sessionId}/prompt', {
                params: { path: { sessionId } },
                body: { text },
              }),
            );
            result = { sent: true };
          }
        }
        setStatusMessage(`Allowed "${request.label}"`);
        replyToFrame(sourceWindow, request, { ok: true, result });
      } catch (err) {
        const msg =
          err instanceof Error ? err.message : 'Failed to execute action';
        setErrorMessage(msg);
        replyToFrame(sourceWindow, request, { ok: false, error: msg });
      } finally {
        setExecuting(false);
        setPending(null);
      }
    },
    [app, canUse, onExecuteAction, replyToFrame, running, sessionId],
  );

  useEffect(() => {
    const onMessage = (ev: MessageEvent) => {
      const inlineWin = iframeRef.current?.contentWindow;
      const fsWin = fullscreenIframeRef.current?.contentWindow;
      if (!ev.source || (ev.source !== inlineWin && ev.source !== fsWin)) {
        return;
      }

      const d = ev.data as Record<string, unknown> | null;
      if (
        d &&
        typeof d === 'object' &&
        d.type === 'agenthold:resize' &&
        d.frameId === frameId &&
        typeof d.height === 'number' &&
        Number.isFinite(d.height)
      ) {
        if (ev.source === inlineWin) {
          setHeight(clampVisualHeight(d.height));
        }
        return;
      }

      const request = parseMcpAppMessage(ev.data, frameId);
      if (!request) return;

      // Only trust readOnly status declared by the server/host manifest, never from the iframe.
      const actionMeta = app.actions?.find((a) => a.name === request.action);
      const isReadOnly =
        request.kind !== 'prompt' &&
        request.kind !== 'open_link' &&
        Boolean(actionMeta?.readOnly);

      if (isReadOnly) {
        void runAction(request, ev.source, true);
        return;
      }

      setStatusMessage(null);
      setErrorMessage(null);
      setPending({ request, actionMeta, sourceWindow: ev.source });
    };

    window.addEventListener('message', onMessage);
    return () => {
      window.removeEventListener('message', onMessage);
    };
  }, [app.actions, frameId, runAction]);

  // Clean lifecycle teardown when navigating away or switching sessions.
  useEffect(() => {
    const inlineEl = iframeRef.current;
    const fsEl = fullscreenIframeRef.current;
    return () => {
      setPending(null);
      setFullscreen(false);
      if (inlineEl) inlineEl.srcdoc = '';
      if (fsEl) fsEl.srcdoc = '';
    };
  }, [sessionId]);

  const handleDecline = () => {
    if (!pending) return;
    const { request, sourceWindow } = pending;
    setPending(null);
    setStatusMessage(`Declined "${request.label}"`);
    replyToFrame(sourceWindow, request, {
      ok: false,
      declined: true,
      error: 'Action declined by user',
    });
  };

  const handleAllow = () => {
    if (!pending) return;
    void runAction(pending.request, pending.sourceWindow, false);
  };

  const needsSessionAccess =
    pending && pending.request.kind !== 'open_link' && !onExecuteAction;
  const allowBlocked = Boolean(needsSessionAccess && !canUse);

  const confirmationBanner = pending ? (
    <div
      role="alertdialog"
      aria-label={`Confirm MCP App action: ${pending.request.label}`}
      data-testid="mcp-app-confirm"
      className="grid gap-2.5 border-b border-amber-500/40 bg-amber-500/10 px-3.5 py-3 text-xs"
    >
      <div className="flex items-start gap-2">
        <ShieldAlert
          className="mt-0.5 size-4 shrink-0 text-amber-500"
          aria-hidden
        />
        <div className="min-w-0 flex-1 space-y-1">
          <p className="font-medium text-foreground">
            Confirm action:{' '}
            <span className="font-semibold">{pending.request.label}</span>
            {app.server && (
              <span className="ml-1.5 font-normal text-muted-foreground">
                ({app.server})
              </span>
            )}
          </p>
          {(pending.actionMeta?.description || pending.request.description) && (
            <p className="break-words text-muted-foreground">
              {pending.actionMeta?.description || pending.request.description}
            </p>
          )}
          {pending.request.params &&
            Object.keys(pending.request.params).length > 0 && (
              <pre className="max-h-28 overflow-auto rounded-md border bg-background/70 p-2 font-mono text-[11px] text-muted-foreground">
                {JSON.stringify(pending.request.params, null, 2)}
              </pre>
            )}
          {allowBlocked && (
            <p className="text-amber-600 dark:text-amber-400">
              You have read-only access to this session and cannot run this
              action.
            </p>
          )}
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2 pl-6">
        <Button
          type="button"
          size="sm"
          disabled={executing || allowBlocked}
          onClick={handleAllow}
        >
          {executing && (
            <Loader2 className="mr-1.5 size-3.5 animate-spin" aria-hidden />
          )}
          Allow
        </Button>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={executing}
          onClick={handleDecline}
        >
          Decline
        </Button>
      </div>
    </div>
  ) : null;

  return (
    <>
      <div
        data-testid="mcp-app-card"
        className="mt-1.5 overflow-hidden rounded-xl border bg-card/70 shadow-xs"
      >
        <div className="flex items-center justify-between gap-2 border-b bg-muted/30 px-3 py-2">
          <div className="flex min-w-0 items-center gap-2">
            <AppWindow
              className="size-3.5 shrink-0 text-violet-500"
              aria-hidden
            />
            <span className="truncate text-xs font-medium text-foreground">
              {app.title}
            </span>
            {app.server && (
              <span className="shrink-0 rounded-full border bg-background/80 px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
                {app.server}
              </span>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            {statusMessage && (
              <span
                role="status"
                className="inline-flex items-center gap-1 text-[11px] text-muted-foreground"
              >
                <Check className="size-3 text-emerald-500" aria-hidden />
                {statusMessage}
              </span>
            )}
            <button
              type="button"
              onClick={() => setFullscreen(true)}
              aria-label="Fullscreen MCP App"
              className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            >
              <Maximize2 className="size-3.5" aria-hidden />
              <span className="sr-only sm:not-sr-only">Fullscreen</span>
            </button>
          </div>
        </div>

        {confirmationBanner}

        {errorMessage && (
          <p
            role="alert"
            className="border-b border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive"
          >
            {errorMessage}
          </p>
        )}

        <iframe
          ref={iframeRef}
          title={app.title}
          sandbox="allow-scripts"
          srcDoc={srcDoc}
          onLoad={() => syncThemeTo(iframeRef.current)}
          style={{ height: `${height}px` }}
          className="block w-full border-0 bg-background"
        />
      </div>

      <DialogPrimitive.Root open={fullscreen} onOpenChange={setFullscreen}>
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/80 backdrop-blur-xs" />
          <DialogPrimitive.Content
            aria-describedby={undefined}
            className="fixed inset-4 z-50 flex flex-col overflow-hidden rounded-xl border bg-background shadow-2xl focus:outline-none md:inset-8"
          >
            <div className="flex items-center justify-between gap-2 border-b bg-card px-4 py-2.5">
              <div className="flex min-w-0 items-center gap-2">
                <AppWindow
                  className="size-4 shrink-0 text-violet-500"
                  aria-hidden
                />
                <DialogPrimitive.Title className="truncate text-sm font-medium text-foreground">
                  {app.title}
                </DialogPrimitive.Title>
                {app.server && (
                  <span className="shrink-0 rounded-full border bg-background px-2 py-0.5 font-mono text-xs text-muted-foreground">
                    {app.server}
                  </span>
                )}
              </div>
              <DialogPrimitive.Close
                aria-label="Close fullscreen"
                className="inline-flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
              >
                <X className="size-4" />
              </DialogPrimitive.Close>
            </div>
            {confirmationBanner}
            <iframe
              ref={fullscreenIframeRef}
              title={`${app.title} (fullscreen)`}
              sandbox="allow-scripts"
              srcDoc={srcDoc}
              onLoad={() => syncThemeTo(fullscreenIframeRef.current)}
              className="min-h-0 w-full flex-1 border-0 bg-background"
            />
          </DialogPrimitive.Content>
        </DialogPrimitive.Portal>
      </DialogPrimitive.Root>
    </>
  );
}
