import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
  type WheelEvent,
} from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ArrowLeft,
  ArrowRight,
  Camera,
  Crosshair,
  ExternalLink,
  Globe,
  Loader2,
  MessageSquarePlus,
  MonitorSmartphone,
  Power,
  RefreshCw,
  RotateCw,
  Send,
  TriangleAlert,
} from 'lucide-react';
import { api, ApiError, unwrap } from '@/api/client';
import {
  browserStatusQuery,
  exposuresQuery,
  type BrowserElement,
  type BrowserInteractInput,
  type BrowserState,
  type Exposure,
} from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import {
  attachToComposer,
  newContextId,
  type PageRect,
} from '@/lib/composerContext';
import { cn } from '@/lib/utils';

/** Viewport presets; "fill" takes the panel's size (live app frame only). */
export type ViewportId = 'fill' | 'desktop' | 'tablet' | 'mobile';

export const VIEWPORTS: {
  id: ViewportId;
  label: string;
  width?: number;
  height?: number;
}[] = [
  { id: 'fill', label: 'Fill' },
  { id: 'desktop', label: 'Desktop', width: 1280, height: 800 },
  { id: 'tablet', label: 'Tablet', width: 768, height: 1024 },
  { id: 'mobile', label: 'Mobile', width: 390, height: 844 },
];

/** The size of a preset, swapped when rotated (null for "fill"). */
export function viewportSize(
  id: ViewportId,
  rotated = false,
): { width: number; height: number } | null {
  const v = VIEWPORTS.find((x) => x.id === id);
  if (!v?.width || !v.height) return null;
  return rotated
    ? { width: v.height, height: v.width }
    : { width: v.width, height: v.height };
}

type Box = { left: number; top: number; width: number; height: number };

/** A point on the shown screenshot in the page's viewport pixels. */
export function toViewport(
  clientX: number,
  clientY: number,
  box: Box,
  viewport: { width: number; height: number },
): { x: number; y: number } {
  const sx = box.width > 0 ? viewport.width / box.width : 1;
  const sy = box.height > 0 ? viewport.height / box.height : 1;
  const clamp = (v: number, max: number) => Math.min(Math.max(v, 0), max);
  return {
    x: Math.round(clamp((clientX - box.left) * sx, viewport.width)),
    y: Math.round(clamp((clientY - box.top) * sy, viewport.height)),
  };
}

/** The smallest element whose box holds the point. */
export function elementAt(
  elements: BrowserElement[],
  x: number,
  y: number,
): BrowserElement | undefined {
  let best: BrowserElement | undefined;
  for (const e of elements) {
    const r = e.rect;
    if (x < r.x || y < r.y || x > r.x + r.width || y > r.y + r.height) continue;
    if (!best || r.width * r.height < best.rect.width * best.rect.height)
      best = e;
  }
  return best;
}

/** The rectangle two corners span. */
export function spanRect(
  a: { x: number; y: number },
  b: { x: number; y: number },
): PageRect {
  return {
    x: Math.min(a.x, b.x),
    y: Math.min(a.y, b.y),
    width: Math.abs(a.x - b.x),
    height: Math.abs(a.y - b.y),
  };
}

/** Keys the page gets as key presses (the rest of single characters is typed). */
const PRESS_KEYS = new Set([
  'Enter',
  'Tab',
  'Backspace',
  'Delete',
  'Escape',
  'ArrowLeft',
  'ArrowRight',
  'ArrowUp',
  'ArrowDown',
  'Home',
  'End',
  'PageUp',
  'PageDown',
]);

/** How a key event goes to the page: typed text, a key press, or nothing. */
export function keyAction(e: {
  key: string;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
}): BrowserInteractInput | null {
  const mod = e.ctrlKey ? 'Control+' : e.metaKey ? 'Meta+' : '';
  if (mod && e.key.length === 1) return { action: 'press', key: mod + e.key };
  if (PRESS_KEYS.has(e.key)) return { action: 'press', key: mod + e.key };
  if (e.key.length === 1 && !e.altKey) return { action: 'type', text: e.key };
  return null;
}

const pct = (v: number, of: number) => `${(v / of) * 100}%`;

function boxStyle(r: PageRect, vp: { width: number; height: number }) {
  return {
    left: pct(r.x, vp.width),
    top: pct(r.y, vp.height),
    width: pct(r.width, vp.width),
    height: pct(r.height, vp.height),
  };
}

type Mode = 'app' | 'browser';

/**
 * The Preview tab: the machine's exposed ports live in a frame, and its
 * headless browser (for any port inside the machine, exposed or not) as a
 * screenshot you can click, type into and annotate for the composer.
 */
export function BrowserPane({
  agentId,
  canUse,
  composerKey,
  starting,
  onStart,
}: {
  agentId: string;
  canUse: boolean;
  /** Draft key annotations go to. */
  composerKey?: string;
  starting?: boolean;
  onStart?: () => void;
}) {
  const exposures = useQuery(exposuresQuery(agentId));
  const list = exposures.data?.exposures ?? [];
  const [picked, setPicked] = useState<Mode | null>(null);
  // Without exposures the sandbox browser is the useful view.
  const mode: Mode =
    picked ??
    (!canUse || list.length > 0 || exposures.isPending ? 'app' : 'browser');
  const [inspect, setInspect] = useState<string | null>(null);
  return (
    <div data-testid="browser-pane" className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-1 border-b px-2 py-1.5">
        <div
          role="tablist"
          aria-label="Preview mode"
          className="flex items-center gap-0.5 rounded-lg bg-muted p-0.5"
        >
          <ModeTab
            active={mode === 'app'}
            onClick={() => setPicked('app')}
            icon={<Globe className="size-3.5" aria-hidden />}
          >
            Live app
          </ModeTab>
          {canUse && (
            <ModeTab
              active={mode === 'browser'}
              onClick={() => setPicked('browser')}
              icon={<MonitorSmartphone className="size-3.5" aria-hidden />}
            >
              Sandbox browser
            </ModeTab>
          )}
        </div>
      </div>
      {mode === 'app' ? (
        <AppFrame
          exposures={list}
          loading={exposures.isPending}
          error={exposures.error}
          enabled={exposures.data?.enabled ?? true}
          canUse={canUse}
          onInspect={(url) => {
            setInspect(url);
            setPicked('browser');
          }}
        />
      ) : (
        <SandboxBrowser
          agentId={agentId}
          exposures={list}
          composerKey={composerKey}
          initialUrl={inspect}
          starting={starting}
          onStart={onStart}
        />
      )}
    </div>
  );
}

function ModeTab({
  active,
  onClick,
  icon,
  children,
}: {
  active: boolean;
  onClick: () => void;
  icon: ReactNode;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={cn(
        'flex items-center gap-1.5 rounded-md px-2 py-1 text-xs',
        active
          ? 'bg-background font-medium text-foreground shadow-sm'
          : 'text-muted-foreground hover:text-foreground',
      )}
    >
      {icon}
      {children}
    </button>
  );
}

function ViewportPicker({
  value,
  rotated,
  allowFill,
  onChange,
  onRotate,
}: {
  value: ViewportId;
  rotated: boolean;
  allowFill: boolean;
  onChange: (v: ViewportId) => void;
  onRotate: () => void;
}) {
  const size = viewportSize(value, rotated);
  return (
    <div className="flex items-center gap-1">
      <select
        aria-label="Viewport"
        value={value}
        onChange={(e) => onChange(e.target.value as ViewportId)}
        className="h-7 rounded-md border border-input bg-transparent px-1.5 text-xs"
      >
        {VIEWPORTS.filter((v) => allowFill || v.id !== 'fill').map((v) => (
          <option key={v.id} value={v.id}>
            {v.label}
            {v.width ? ` ${v.width}×${v.height}` : ''}
          </option>
        ))}
      </select>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label="Rotate viewport"
        title="Rotate"
        disabled={!size}
        aria-pressed={rotated}
        onClick={onRotate}
      >
        <RotateCw className="size-3.5" />
      </Button>
    </div>
  );
}

const isHttp = (u: string) => /^https?:\/\//i.test(u);

/** The exposed ports in a live frame. */
function AppFrame({
  exposures,
  loading,
  error,
  enabled,
  canUse,
  onInspect,
}: {
  exposures: Exposure[];
  loading: boolean;
  error: unknown;
  enabled: boolean;
  canUse: boolean;
  onInspect: (url: string) => void;
}) {
  const [chosen, setChosen] = useState<string | null>(null);
  const current = exposures.find((e) => e.id === chosen) ?? exposures[0];
  // What the frame and the address bar show, for one exposure: a new
  // exposure shows its root.
  const base = current?.url ?? '';
  const [nav, setNav] = useState({ base, src: base, addr: base });
  const src = nav.base === base ? nav.src : base;
  const addr = nav.base === base ? nav.addr : base;
  const setSrc = (v: string) => setNav({ base, src: v, addr: v });
  const setAddr = (v: string) => setNav({ base, src, addr: v });
  const [reload, setReload] = useState(0);
  const [vp, setVp] = useState<ViewportId>('fill');
  const [rotated, setRotated] = useState(false);
  const frameBox = useRef<HTMLDivElement>(null);
  const [avail, setAvail] = useState<{ w: number; h: number } | null>(null);

  useEffect(() => {
    const el = frameBox.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(([entry]) =>
      setAvail({ w: entry.contentRect.width, h: entry.contentRect.height }),
    );
    ro.observe(el);
    return () => ro.disconnect();
  }, [current]);

  if (loading) return <Loading />;
  if (error)
    return (
      <div className="p-4">
        <ErrorText error={error} />
      </div>
    );
  if (!current)
    return (
      <div className="grid gap-2 p-4 text-sm text-muted-foreground">
        <p>
          {enabled
            ? 'No ports are exposed yet. Ask the agent to expose its dev server (expose_port), or expose one in the agent settings under Preview.'
            : 'Preview URLs are not enabled on this server.'}
        </p>
        {canUse && (
          <p>
            The sandbox browser opens any port inside the machine, exposed or
            not.
          </p>
        )}
      </div>
    );

  const size = viewportSize(vp, rotated);
  const scale =
    size && avail
      ? Math.min(1, avail.w / size.width, avail.h / size.height)
      : 1;
  const go = () => {
    const u = addr.trim();
    if (!u) return;
    const next = isHttp(u)
      ? u
      : new URL(u.replace(/^\/?/, '/'), current.url).toString();
    setSrc(next);
    setReload((n) => n + 1);
  };
  const path = (() => {
    try {
      const u = new URL(src);
      return u.pathname + u.search;
    } catch {
      return '/';
    }
  })();
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div
        role="list"
        aria-label="Exposed ports"
        className="flex shrink-0 flex-wrap gap-1 border-b px-2 py-1.5"
      >
        {exposures.map((e) => (
          <button
            key={e.id}
            type="button"
            role="listitem"
            aria-pressed={e.id === current.id}
            title={e.url}
            onClick={() => {
              setChosen(e.id);
              setReload((n) => n + 1);
            }}
            className={cn(
              'flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs',
              e.id === current.id
                ? 'border-primary/50 bg-primary/10 text-foreground'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            <span className="font-medium">{e.name}</span>
            <span className="font-mono tabular-nums">:{e.port}</span>
          </button>
        ))}
      </div>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          go();
        }}
        className="flex shrink-0 items-center gap-1 border-b px-2 py-1.5"
      >
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label="Reload"
          title="Reload"
          onClick={() => setReload((n) => n + 1)}
        >
          <RefreshCw className="size-3.5" />
        </Button>
        <input
          aria-label="Address"
          value={addr}
          onChange={(e) => setAddr(e.target.value)}
          onKeyDown={(e) => {
            // The form has no submit button: Enter goes there.
            if (e.key === 'Enter') {
              e.preventDefault();
              go();
            }
          }}
          className="h-7 min-w-0 flex-1 rounded-md border border-input bg-transparent px-2 font-mono text-xs"
        />
        <Button
          variant="ghost"
          size="icon-sm"
          asChild
          aria-label="Open in new tab"
          title="Open in new tab"
        >
          <a href={src} target="_blank" rel="noreferrer">
            <ExternalLink className="size-3.5" />
          </a>
        </Button>
        {canUse && (
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label="Inspect in the sandbox browser"
            title="Inspect and annotate in the sandbox browser"
            onClick={() => onInspect(`http://localhost:${current.port}${path}`)}
          >
            <Crosshair className="size-3.5" />
          </Button>
        )}
        <ViewportPicker
          value={vp}
          rotated={rotated}
          allowFill
          onChange={(v) => {
            setVp(v);
            setRotated(false);
          }}
          onRotate={() => setRotated((r) => !r)}
        />
      </form>
      <div
        ref={frameBox}
        className={cn(
          'relative min-h-0 flex-1 overflow-hidden',
          size && 'bg-muted/40',
        )}
      >
        <div
          data-testid="app-frame"
          data-viewport={size ? `${size.width}x${size.height}` : 'fill'}
          className={cn(
            'origin-top-left bg-white',
            size ? 'absolute top-0 left-1/2 shadow-lg' : 'size-full',
          )}
          style={
            size
              ? {
                  width: size.width,
                  height: size.height,
                  transform: `translateX(-50%) scale(${scale})`,
                  transformOrigin: 'top center',
                }
              : undefined
          }
        >
          <iframe
            key={`${src}#${reload}`}
            title={`Live preview of ${current.name}`}
            src={src}
            sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-modals allow-downloads"
            className="size-full border-0"
          />
        </div>
      </div>
      {size && (
        <p className="shrink-0 border-t px-2 py-1 text-[11px] text-muted-foreground tabular-nums">
          {size.width}×{size.height} · {Math.round(scale * 100)}%
        </p>
      )}
    </div>
  );
}

type Op =
  | { kind: 'navigate'; url: string }
  | { kind: 'screenshot'; silent?: boolean; width?: number; height?: number }
  | { kind: 'interact'; body: BrowserInteractInput }
  | { kind: 'close' };

type Selection =
  | { element: BrowserElement; region?: undefined }
  | { region: PageRect; element?: undefined };

/** The machine's headless Chromium, shown as a live screenshot. */
function SandboxBrowser({
  agentId,
  exposures,
  composerKey,
  initialUrl,
  starting,
  onStart,
}: {
  agentId: string;
  exposures: Exposure[];
  composerKey?: string;
  initialUrl: string | null;
  starting?: boolean;
  onStart?: () => void;
}) {
  const qc = useQueryClient();
  const status = useQuery(browserStatusQuery(agentId));
  const [page, setPage] = useState<BrowserState | null>(null);
  const [addr, setAddr] = useState(initialUrl ?? '');
  const [vp, setVp] = useState<ViewportId>('desktop');
  const [rotated, setRotated] = useState(false);
  const [auto, setAuto] = useState(false);
  const [annotate, setAnnotate] = useState(false);
  const [typed, setTyped] = useState('');
  const [hover, setHover] = useState<BrowserElement | undefined>();
  const [drag, setDrag] = useState<{
    from: { x: number; y: number };
    to: { x: number; y: number };
  } | null>(null);
  const [selection, setSelection] = useState<Selection | null>(null);
  const [note, setNote] = useState('');
  const [sent, setSent] = useState(false);
  const surface = useRef<HTMLDivElement>(null);
  const wheel = useRef({ dx: 0, dy: 0, timer: 0 as unknown });

  useEffect(() => {
    const w = wheel.current;
    return () => clearTimeout(w.timer as number);
  }, []);

  const size = viewportSize(vp, rotated) ?? { width: 1280, height: 800 };
  const run = useMutation({
    mutationFn: async (op: Op): Promise<BrowserState> => {
      const path = { agentId };
      switch (op.kind) {
        case 'navigate':
          return unwrap(
            api.POST('/agents/{agentId}/browser/navigate', {
              params: { path },
              body: { url: op.url, width: size.width, height: size.height },
            }),
          );
        case 'screenshot':
          return unwrap(
            api.GET('/agents/{agentId}/browser/screenshot', {
              params: {
                path,
                query: {
                  width: op.width ?? size.width,
                  height: op.height ?? size.height,
                },
              },
            }),
          );
        case 'interact':
          return unwrap(
            api.POST('/agents/{agentId}/browser/interact', {
              params: { path },
              body: op.body,
            }),
          );
        case 'close':
          return unwrap(
            api.POST('/agents/{agentId}/browser/close', { params: { path } }),
          );
      }
    },
    onSuccess: (r, op) => {
      setPage(op.kind === 'close' ? null : r);
      if (r.url && op.kind !== 'close') setAddr(r.url);
      if (op.kind === 'navigate' || op.kind === 'close')
        void qc.invalidateQueries({
          queryKey: browserStatusQuery(agentId).queryKey,
        });
    },
  });
  const { mutate } = run;

  // Show what the browser shows when the tab opens; open the page asked for.
  const running = status.data?.running;
  const started = useRef(false);
  useEffect(() => {
    if (started.current || !status.data) return;
    started.current = true;
    if (initialUrl) mutate({ kind: 'navigate', url: initialUrl });
    else if (running) mutate({ kind: 'screenshot', silent: true });
  }, [status.data, running, initialUrl, mutate]);

  // Auto refresh while the agent works in it.
  const busy = run.isPending;
  useEffect(() => {
    if (!auto || !page) return;
    const t = setInterval(() => {
      if (!busy && document.visibilityState !== 'hidden')
        mutate({ kind: 'screenshot', silent: true });
    }, 3000);
    return () => clearInterval(t);
  }, [auto, page, busy, mutate]);

  if (status.isPending) return <Loading />;
  if (status.error) {
    const stopped =
      status.error instanceof ApiError && status.error.status === 409;
    return (
      <div className="grid justify-items-start gap-2 p-4 text-sm">
        <ErrorText error={status.error} />
        {stopped && onStart && (
          <Button size="sm" disabled={starting} onClick={onStart}>
            {starting && <Loader2 className="size-3.5 animate-spin" />}
            Start the agent
          </Button>
        )}
      </div>
    );
  }
  if (!status.data.available)
    return (
      <p className="flex items-start gap-2 p-4 text-sm text-muted-foreground">
        <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
        This machine has no headless Chromium. Install chromium in its image (or
        set CHROME_BIN) to use the sandbox browser.
      </p>
    );

  const vpOf = page ?? size;
  const at = (e: MouseEvent) => {
    const r = surface.current?.getBoundingClientRect();
    return toViewport(
      e.clientX,
      e.clientY,
      r ?? { left: 0, top: 0, width: vpOf.width, height: vpOf.height },
      vpOf,
    );
  };
  const interact = (body: BrowserInteractInput) =>
    mutate({ kind: 'interact', body });
  // A new viewport size shows at once when a page is open.
  const resize = (s: { width: number; height: number } | null) => {
    if (page && s) mutate({ kind: 'screenshot', ...s });
  };

  const onMouseDown = (e: MouseEvent) => {
    if (!annotate || e.button !== 0) return;
    const p = at(e);
    setDrag({ from: p, to: p });
    setSelection(null);
    setSent(false);
  };
  const onMouseMove = (e: MouseEvent) => {
    if (!annotate || !page) return;
    const p = at(e);
    if (drag) setDrag({ ...drag, to: p });
    else setHover(elementAt(page.elements, p.x, p.y));
  };
  const onMouseUp = (e: MouseEvent) => {
    if (!annotate || !drag || !page) return;
    const p = at(e);
    const r = spanRect(drag.from, p);
    setDrag(null);
    if (r.width > 6 || r.height > 6) setSelection({ region: r });
    else {
      const el = elementAt(page.elements, p.x, p.y);
      setSelection(
        el
          ? { element: el }
          : { region: { x: p.x - 20, y: p.y - 20, width: 40, height: 40 } },
      );
    }
  };
  const onClick = (e: MouseEvent) => {
    if (annotate || !page) return;
    surface.current?.focus();
    interact({ action: 'click', ...at(e) });
  };
  const onWheel = (e: WheelEvent) => {
    if (annotate || !page) return;
    const w = wheel.current;
    w.dx += e.deltaX;
    w.dy += e.deltaY;
    clearTimeout(w.timer as number);
    w.timer = setTimeout(() => {
      const { dx, dy } = w;
      w.dx = w.dy = 0;
      if (dx || dy) interact({ action: 'scroll', dx, dy });
    }, 200);
  };
  const onKeyDown = (e: KeyboardEvent) => {
    if (annotate || !page) return;
    const a = keyAction(e);
    if (!a) return;
    e.preventDefault();
    interact(a);
  };
  const send = () => {
    if (!composerKey || !page || !selection || !note.trim()) return;
    attachToComposer(composerKey, {
      kind: 'browser',
      id: newContextId(),
      url: page.url ?? '',
      title: page.title,
      viewport: { width: page.width, height: page.height },
      element: selection.element && {
        role: selection.element.role,
        name: selection.element.name,
        selector: selection.element.selector,
        rect: selection.element.rect,
      },
      region: selection.region,
      body: note.trim(),
    });
    setSelection(null);
    setNote('');
    setSent(true);
  };

  const ports = [...new Set(exposures.map((e) => e.port))];
  const shot = page?.screenshot;
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (addr.trim()) mutate({ kind: 'navigate', url: addr.trim() });
        }}
        className="flex shrink-0 items-center gap-1 border-b px-2 py-1.5"
      >
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label="Back"
          disabled={!page || busy}
          onClick={() => interact({ action: 'back' })}
        >
          <ArrowLeft className="size-3.5" />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label="Forward"
          disabled={!page || busy}
          onClick={() => interact({ action: 'forward' })}
        >
          <ArrowRight className="size-3.5" />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label="Reload page"
          disabled={!page || busy}
          onClick={() => interact({ action: 'reload' })}
        >
          <RefreshCw className="size-3.5" />
        </Button>
        <input
          aria-label="Browser address"
          placeholder="localhost:3000"
          value={addr}
          onChange={(e) => setAddr(e.target.value)}
          className="h-7 min-w-0 flex-1 rounded-md border border-input bg-transparent px-2 font-mono text-xs"
        />
        <Button
          type="submit"
          size="sm"
          className="h-7 px-2 text-xs"
          disabled={!addr.trim() || busy}
        >
          Go
        </Button>
      </form>
      <div className="flex shrink-0 flex-wrap items-center gap-1 border-b px-2 py-1">
        {ports.map((p) => (
          <button
            key={p}
            type="button"
            onClick={() =>
              mutate({ kind: 'navigate', url: `http://localhost:${p}/` })
            }
            className="rounded-full border px-2 py-0.5 font-mono text-[11px] text-muted-foreground hover:text-foreground"
          >
            localhost:{p}
          </button>
        ))}
        <span className="flex-1" />
        <ViewportPicker
          value={vp}
          rotated={rotated}
          allowFill={false}
          onChange={(v) => {
            setVp(v);
            setRotated(false);
            resize(viewportSize(v));
          }}
          onRotate={() => {
            setRotated(!rotated);
            resize(viewportSize(vp, !rotated));
          }}
        />
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Refresh screenshot"
          title="Refresh screenshot"
          disabled={!page || busy}
          onClick={() => mutate({ kind: 'screenshot' })}
        >
          <Camera className="size-3.5" />
        </Button>
        <label className="flex items-center gap-1 text-[11px] text-muted-foreground">
          <input
            type="checkbox"
            checked={auto}
            onChange={(e) => setAuto(e.target.checked)}
          />
          Auto
        </label>
        {composerKey && (
          <Button
            variant={annotate ? 'secondary' : 'ghost'}
            size="sm"
            className="h-7 px-2 text-xs"
            aria-pressed={annotate}
            disabled={!page}
            onClick={() => {
              setAnnotate((a) => !a);
              setSelection(null);
              setHover(undefined);
              setSent(false);
            }}
          >
            <Crosshair className="size-3.5" aria-hidden />
            Annotate
          </Button>
        )}
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Close browser"
          title="Stop the browser"
          disabled={!page || busy}
          onClick={() => mutate({ kind: 'close' })}
        >
          <Power className="size-3.5" />
        </Button>
      </div>
      {page && (
        <div className="flex shrink-0 items-center gap-2 px-2 py-1 text-[11px] text-muted-foreground">
          <span className="min-w-0 flex-1 truncate" title={page.title}>
            {page.title || page.url}
          </span>
          {page.consoleErrors > 0 && (
            <span
              title={page.lastError}
              className="rounded bg-destructive/10 px-1.5 text-destructive"
            >
              {page.consoleErrors} console error
              {page.consoleErrors === 1 ? '' : 's'}
            </span>
          )}
          <span className="tabular-nums">
            {page.width}×{page.height}
          </span>
          {busy && (
            <Loader2
              role="img"
              aria-label="loading"
              className="size-3 animate-spin"
            />
          )}
        </div>
      )}
      {!(run.variables?.kind === 'screenshot' && run.variables.silent) && (
        <div className="px-2">
          <ErrorText error={run.error} />
        </div>
      )}
      <div className="min-h-0 flex-1 overflow-auto bg-muted/40 p-2">
        {!page && !busy && (
          <p className="p-2 text-sm text-muted-foreground">
            Open a URL in the machine&apos;s headless Chromium:{' '}
            <code className="font-mono">localhost:&lt;port&gt;</code> reaches
            servers inside it, exposed or not.
          </p>
        )}
        {shot && (
          <div
            className="relative mx-auto"
            style={{
              maxWidth: page.width,
              aspectRatio: `${page.width} / ${page.height}`,
            }}
          >
            <img
              alt={`Screenshot of ${page.title || page.url}`}
              src={`data:image/png;base64,${shot}`}
              draggable={false}
              className="block size-full rounded border bg-white select-none"
            />
            <div
              ref={surface}
              role="application"
              aria-label="Page viewport"
              tabIndex={0}
              onClick={onClick}
              onMouseDown={onMouseDown}
              onMouseMove={onMouseMove}
              onMouseUp={onMouseUp}
              onMouseLeave={() => setHover(undefined)}
              onWheel={onWheel}
              onKeyDown={onKeyDown}
              className={cn(
                'absolute inset-0 outline-none focus-visible:ring-2 focus-visible:ring-ring',
                annotate ? 'cursor-crosshair' : 'cursor-pointer',
              )}
            >
              {annotate && hover && !drag && !selection && (
                <div
                  aria-hidden
                  className="pointer-events-none absolute rounded-sm border-2 border-sky-400/80 bg-sky-400/10"
                  style={boxStyle(hover.rect, page)}
                />
              )}
              {drag && (
                <div
                  aria-hidden
                  className="pointer-events-none absolute border-2 border-dashed border-amber-400 bg-amber-400/10"
                  style={boxStyle(spanRect(drag.from, drag.to), page)}
                />
              )}
              {selection && (
                <div
                  data-testid="annotation-box"
                  className="pointer-events-none absolute rounded-sm border-2 border-amber-500 bg-amber-400/15"
                  style={boxStyle(
                    selection.element
                      ? selection.element.rect
                      : selection.region,
                    page,
                  )}
                />
              )}
            </div>
          </div>
        )}
      </div>
      {annotate && selection && (
        <form
          role="group"
          aria-label="Annotation"
          onSubmit={(e) => {
            e.preventDefault();
            send();
          }}
          className="grid shrink-0 gap-2 border-t p-2"
        >
          <span className="flex items-center gap-1.5 truncate text-xs text-muted-foreground">
            <MessageSquarePlus className="size-3.5 shrink-0" aria-hidden />
            {selection.element
              ? `${selection.element.role}${selection.element.name ? ` “${selection.element.name}”` : ''} · ${selection.element.selector}`
              : `Region ${Math.round(selection.region.width)}×${Math.round(selection.region.height)}`}
          </span>
          <textarea
            autoFocus
            aria-label="Note"
            rows={2}
            value={note}
            placeholder="What should change here? e.g. Make this button aligned right"
            onChange={(e) => setNote(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') setSelection(null);
              else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                send();
              }
            }}
            className="w-full resize-y rounded-md border border-input bg-transparent px-2 py-1.5 text-sm placeholder:text-muted-foreground"
          />
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => setSelection(null)}
            >
              Cancel
            </Button>
            <Button type="submit" size="sm" disabled={!note.trim()}>
              <Send className="size-3.5" aria-hidden />
              Send to composer
            </Button>
          </div>
        </form>
      )}
      {annotate && !selection && (
        <p className="shrink-0 border-t px-2 py-1.5 text-xs text-muted-foreground">
          {sent
            ? 'Sent to the composer. Pick another element or drag a region.'
            : 'Click an element or drag a region on the page to annotate it.'}
        </p>
      )}
      {!annotate && page && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (!typed) return;
            interact({ action: 'type', text: typed });
            setTyped('');
          }}
          className="flex shrink-0 items-center gap-1 border-t px-2 py-1.5"
        >
          <input
            aria-label="Type into the page"
            placeholder="Type into the focused field…"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            className="h-7 min-w-0 flex-1 rounded-md border border-input bg-transparent px-2 text-xs"
          />
          <Button
            type="submit"
            size="sm"
            variant="outline"
            className="h-7 px-2 text-xs"
            disabled={!typed || busy}
          >
            Type
          </Button>
          {['Enter', 'Tab', 'Escape'].map((k) => (
            <Button
              key={k}
              type="button"
              size="sm"
              variant="ghost"
              className="h-7 px-1.5 text-[11px]"
              aria-label={`Press ${k}`}
              disabled={busy}
              onClick={() => interact({ action: 'press', key: k })}
            >
              {k === 'Escape' ? 'Esc' : k}
            </Button>
          ))}
        </form>
      )}
    </div>
  );
}
