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
  Check,
  Code2,
  Copy,
  Download,
  Eye,
  Maximize2,
  Sparkles,
  X,
} from 'lucide-react';
import type { VisualItem } from '@/lib/session';
import {
  buildSandboxedHtml,
  clampVisualHeight,
  slugifyFilename,
  themeVars,
  useCurrentTheme,
} from '@/lib/visualSandbox';

/**
 * Renders a self-contained HTML visual inside a tightly sandboxed iframe
 * (`sandbox="allow-scripts"` only, no `allow-same-origin` or `allow-top-navigation`).
 * Automatically adapts to agenthold's light/dark theme and resizes via postMessage.
 */
export function HtmlRenderCard({
  item,
  sessionId,
}: {
  item: VisualItem;
  sessionId?: string;
}) {
  const frameId = useId();
  const theme = useCurrentTheme();
  const [initialTheme] = useState(theme);
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const fullscreenIframeRef = useRef<HTMLIFrameElement>(null);

  const [height, setHeight] = useState<number>(() =>
    clampVisualHeight(item.height),
  );
  const [showSource, setShowSource] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  const [copied, setCopied] = useState(false);

  const title = item.title || 'Visual';

  const srcDoc = useMemo(
    () =>
      buildSandboxedHtml(item.html, {
        frameId,
        theme: initialTheme,
      }),
    [item.html, frameId, initialTheme],
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

  useEffect(() => {
    const onMessage = (ev: MessageEvent) => {
      const win = iframeRef.current?.contentWindow;
      if (!win || ev.source !== win) return;
      const d = ev.data as Record<string, unknown> | null;
      if (!d || typeof d !== 'object') return;
      if (d.type !== 'agenthold:resize' || d.frameId !== frameId) return;
      if (typeof d.height !== 'number' || !Number.isFinite(d.height)) return;
      setHeight(clampVisualHeight(d.height));
    };
    window.addEventListener('message', onMessage);
    return () => window.removeEventListener('message', onMessage);
  }, [frameId]);

  const handleDownload = () => {
    const filename = slugifyFilename(title, `visual-${item.seq}`);
    const blob = new Blob([item.html], { type: 'text/html;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  };

  const handleCopySource = async () => {
    try {
      await navigator.clipboard.writeText(item.html);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard unavailable
    }
  };

  const rawVisualUrl =
    sessionId && item.seq > 0
      ? `/api/v1/sessions/${encodeURIComponent(sessionId)}/visuals/${item.seq}`
      : undefined;

  return (
    <>
      <div
        data-testid="html-render-card"
        data-visual-url={rawVisualUrl}
        className="overflow-hidden rounded-xl border bg-card/70 shadow-xs"
      >
        <div className="flex items-center justify-between gap-2 border-b bg-muted/30 px-3 py-2">
          <div className="flex min-w-0 items-center gap-2">
            <Sparkles className="size-3.5 shrink-0 text-sky-500" aria-hidden />
            <span className="truncate text-xs font-medium text-foreground">
              {title}
            </span>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <button
              type="button"
              onClick={() => setShowSource((s) => !s)}
              aria-pressed={showSource}
              aria-label={showSource ? 'View rendered' : 'View source'}
              className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            >
              {showSource ? (
                <>
                  <Eye className="size-3.5" aria-hidden />
                  <span>Preview</span>
                </>
              ) : (
                <>
                  <Code2 className="size-3.5" aria-hidden />
                  <span>View source</span>
                </>
              )}
            </button>
            <button
              type="button"
              onClick={handleDownload}
              aria-label="Download HTML"
              className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            >
              <Download className="size-3.5" aria-hidden />
              <span>Download HTML</span>
            </button>
            <button
              type="button"
              onClick={() => setFullscreen(true)}
              aria-label="Fullscreen"
              className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            >
              <Maximize2 className="size-3.5" aria-hidden />
              <span className="sr-only sm:not-sr-only">Fullscreen</span>
            </button>
          </div>
        </div>

        {showSource ? (
          <div className="relative bg-background/60">
            <button
              type="button"
              onClick={() => void handleCopySource()}
              aria-label="Copy HTML source"
              className="absolute top-2 right-2 inline-flex items-center gap-1 rounded-md border bg-card px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
            >
              {copied ? (
                <>
                  <Check className="size-3 text-emerald-500" aria-hidden />
                  <span>Copied</span>
                </>
              ) : (
                <>
                  <Copy className="size-3" aria-hidden />
                  <span>Copy</span>
                </>
              )}
            </button>
            <pre
              data-testid="html-render-source"
              className="max-h-[480px] overflow-auto p-3 pr-20 font-mono text-xs leading-relaxed whitespace-pre-wrap text-muted-foreground"
            >
              <code>{item.html}</code>
            </pre>
          </div>
        ) : (
          <iframe
            ref={iframeRef}
            title={title}
            sandbox="allow-scripts"
            srcDoc={srcDoc}
            onLoad={() => syncThemeTo(iframeRef.current)}
            style={{ height: `${height}px` }}
            className="block w-full border-0 bg-background"
          />
        )}
      </div>

      <DialogPrimitive.Root open={fullscreen} onOpenChange={setFullscreen}>
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/80 backdrop-blur-xs" />
          <DialogPrimitive.Content
            aria-describedby={undefined}
            className="fixed inset-4 z-50 flex flex-col overflow-hidden rounded-xl border bg-background shadow-2xl focus:outline-none md:inset-8"
          >
            <div className="flex items-center justify-between gap-2 border-b bg-card px-4 py-2.5">
              <DialogPrimitive.Title className="truncate text-sm font-medium text-foreground">
                {title}
              </DialogPrimitive.Title>
              <div className="flex shrink-0 items-center gap-2">
                <button
                  type="button"
                  onClick={handleDownload}
                  className="inline-flex items-center gap-1.5 rounded-md border bg-background px-2.5 py-1 text-xs text-muted-foreground hover:text-foreground"
                >
                  <Download className="size-3.5" aria-hidden />
                  Download HTML
                </button>
                <DialogPrimitive.Close
                  aria-label="Close fullscreen"
                  className="inline-flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
                >
                  <X className="size-4" />
                </DialogPrimitive.Close>
              </div>
            </div>
            <iframe
              ref={fullscreenIframeRef}
              title={`${title} (fullscreen)`}
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
