import { useEffect, useState } from 'react';
import { getTheme, type Theme } from '@/lib/theme';

export const MIN_VISUAL_HEIGHT = 120;
export const MAX_VISUAL_HEIGHT = 900;
export const DEFAULT_VISUAL_HEIGHT = 320;

/**
 * Strict Content-Security-Policy injected into every sandboxed visual & MCP App iframe.
 * Allows self-contained inline scripts/styles and public HTTPS assets while blocking
 * private/local-network fetches (connect-src 'none'), plugins, nested frames, and forms.
 */
export const VISUAL_CSP = [
  "default-src 'none'",
  "script-src 'unsafe-inline' https:",
  "style-src 'unsafe-inline' https:",
  'img-src data: blob: https:',
  'font-src data: https:',
  'media-src data: blob: https:',
  "connect-src 'none'",
  "frame-src 'none'",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
].join('; ');

export function clampVisualHeight(height?: number | null): number {
  if (typeof height !== 'number' || !Number.isFinite(height) || height <= 0) {
    return DEFAULT_VISUAL_HEIGHT;
  }
  return Math.max(
    MIN_VISUAL_HEIGHT,
    Math.min(MAX_VISUAL_HEIGHT, Math.round(height)),
  );
}

export function themeVars(theme: Theme): Record<string, string> {
  if (theme === 'light') {
    return {
      '--bg': '#ffffff',
      '--fg': '#18181b',
      '--muted': '#6b7280',
      '--border': 'rgba(0, 0, 0, 0.1)',
      '--accent': '#0284c7',
      '--card': '#f8fafc',
    };
  }
  return {
    '--bg': '#171717',
    '--fg': '#ededed',
    '--muted': '#a1a1aa',
    '--border': 'rgba(255, 255, 255, 0.1)',
    '--accent': '#38bdf8',
    '--card': '#202020',
  };
}

/** Subscribes to agenthold's live light/dark theme attribute on <html>. */
export function useCurrentTheme(): Theme {
  const [theme, setTheme] = useState<Theme>(() => getTheme());

  useEffect(() => {
    const sync = () => setTheme(getTheme());
    sync();
    if (typeof MutationObserver === 'undefined') return;
    const obs = new MutationObserver(sync);
    obs.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme'],
    });
    return () => obs.disconnect();
  }, []);

  return theme;
}

function escapeInlineJson(value: unknown): string {
  return JSON.stringify(value)
    .replace(/</g, '\\u003c')
    .replace(/>/g, '\\u003e')
    .replace(/&/g, '\\u0026')
    .replace(/\u2028/g, '\\u2028')
    .replace(/\u2029/g, '\\u2029');
}

function buildThemeCss(theme: Theme): string {
  const vars = themeVars(theme);
  const decls = Object.entries(vars)
    .map(([k, v]) => `  ${k}: ${v};`)
    .join('\n');
  return [
    ':root {',
    decls,
    `  color-scheme: ${theme};`,
    '}',
    '*, *::before, *::after { box-sizing: border-box; }',
    'html, body {',
    '  margin: 0;',
    '  padding: 0;',
    '  background: var(--bg);',
    '  color: var(--fg);',
    "  font-family: Inter, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', sans-serif;",
    '  line-height: 1.5;',
    '}',
    'body { padding: 12px; }',
  ].join('\n');
}

function buildBridgeScript(frameId: string, enableMcpBridge: boolean): string {
  const idLiteral = escapeInlineJson(frameId);
  const mcpSnippet = enableMcpBridge
    ? `
  var pending = Object.create(null);
  var reqSeq = 0;
  window.agentholdMcpApp = {
    invoke: function(action, params, meta) {
      var id = 'req_' + (++reqSeq);
      meta = meta || {};
      return new Promise(function(resolve, reject) {
        pending[id] = { resolve: resolve, reject: reject };
        parent.postMessage({
          type: 'agenthold:mcp_action',
          frameId: FRAME_ID,
          requestId: id,
          action: String(action || ''),
          label: typeof meta.label === 'string' ? meta.label : undefined,
          description: typeof meta.description === 'string' ? meta.description : undefined,
          prompt: typeof meta.prompt === 'string' ? meta.prompt : undefined,
          params: params && typeof params === 'object' ? params : undefined
        }, '*');
      });
    },
    sendMessage: function(text) {
      var id = 'req_' + (++reqSeq);
      return new Promise(function(resolve, reject) {
        pending[id] = { resolve: resolve, reject: reject };
        parent.postMessage({
          jsonrpc: '2.0',
          id: id,
          frameId: FRAME_ID,
          method: 'ui/message',
          params: { text: String(text || '') }
        }, '*');
      });
    },
    openLink: function(url) {
      var id = 'req_' + (++reqSeq);
      return new Promise(function(resolve, reject) {
        pending[id] = { resolve: resolve, reject: reject };
        parent.postMessage({
          jsonrpc: '2.0',
          id: id,
          frameId: FRAME_ID,
          method: 'ui/open-link',
          params: { url: String(url || '') }
        }, '*');
      });
    }
  };`
    : '';

  return `(function(){
  var FRAME_ID = ${idLiteral};
  var rafId = 0;
  var lastHeight = 0;
  function measure() {
    rafId = 0;
    var de = document.documentElement;
    var b = document.body;
    var h = Math.max(
      de ? de.scrollHeight : 0,
      b ? b.scrollHeight : 0,
      b ? Math.ceil(b.getBoundingClientRect().height) : 0
    );
    if (h > 0 && h !== lastHeight) {
      lastHeight = h;
      parent.postMessage({ type: 'agenthold:resize', frameId: FRAME_ID, height: h }, '*');
    }
  }
  function schedule() {
    if (!rafId) rafId = requestAnimationFrame(measure);
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', schedule);
  } else {
    schedule();
  }
  window.addEventListener('load', schedule);
  if (typeof ResizeObserver !== 'undefined') {
    var ro = new ResizeObserver(schedule);
    if (document.documentElement) ro.observe(document.documentElement);
    window.addEventListener('DOMContentLoaded', function(){ if (document.body) ro.observe(document.body); });
  }
  if (typeof MutationObserver !== 'undefined') {
    var mo = new MutationObserver(schedule);
    if (document.documentElement) mo.observe(document.documentElement, { childList: true, subtree: true, attributes: true });
  }
  window.addEventListener('message', function(ev) {
    var d = ev && ev.data;
    if (!d || typeof d !== 'object' || d.frameId !== FRAME_ID) return;
    if (d.type === 'agenthold:theme' && d.vars && typeof d.vars === 'object') {
      var root = document.documentElement;
      if (d.theme) {
        root.dataset.theme = d.theme;
        root.style.colorScheme = d.theme;
      }
      for (var k in d.vars) {
        if (Object.prototype.hasOwnProperty.call(d.vars, k)) {
          root.style.setProperty(k, String(d.vars[k]));
        }
      }
      schedule();
    }${
      enableMcpBridge
        ? `
    if (d.type === 'agenthold:mcp_result' && d.requestId && pending[d.requestId]) {
      var entry = pending[d.requestId];
      delete pending[d.requestId];
      if (d.ok) entry.resolve(d.result);
      else entry.reject(new Error(d.error || 'Action declined'));
    }`
        : ''
    }
  });${mcpSnippet}
})();`;
}

/**
 * Wraps raw agent/MCP HTML in a self-contained document with an injected strict CSP meta tag,
 * theme CSS variables, and a safe postMessage auto-resize/theme bridge.
 */
export function buildSandboxedHtml(
  html: string,
  opts: { frameId: string; theme: Theme; enableMcpBridge?: boolean },
): string {
  const headInjection = [
    '<meta charset="utf-8">',
    '<meta name="viewport" content="width=device-width, initial-scale=1">',
    `<meta http-equiv="Content-Security-Policy" content="${VISUAL_CSP}">`,
    `<style data-agenthold-theme="true">\n${buildThemeCss(opts.theme)}\n</style>`,
    `<script data-agenthold-bridge="true">\n${buildBridgeScript(opts.frameId, Boolean(opts.enableMcpBridge))}\n</script>`,
  ].join('\n');

  const trimmed = html.trim();
  const headMatch = /<head\b[^>]*>/i.exec(trimmed);
  if (headMatch) {
    const idx = headMatch.index + headMatch[0].length;
    return `${trimmed.slice(0, idx)}\n${headInjection}\n${trimmed.slice(idx)}`;
  }

  const htmlMatch = /<html\b[^>]*>/i.exec(trimmed);
  if (htmlMatch) {
    const idx = htmlMatch.index + htmlMatch[0].length;
    return `${trimmed.slice(0, idx)}\n<head>\n${headInjection}\n</head>\n${trimmed.slice(idx)}`;
  }

  return `<!doctype html>
<html data-theme="${opts.theme}">
<head>
${headInjection}
</head>
<body>
${html}
</body>
</html>`;
}

export function slugifyFilename(title?: string, fallback = 'visual'): string {
  const base = (title ?? '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 60);
  return `${base || fallback}.html`;
}

export interface McpAppActionRequest {
  kind: 'prompt' | 'tool' | 'open_link' | 'action';
  action: string;
  label: string;
  description?: string;
  requestId?: string | number;
  jsonrpc?: boolean;
  prompt?: string;
  url?: string;
  params?: Record<string, unknown>;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v);
}

function extractMessageText(params: Record<string, unknown>): string {
  if (typeof params.text === 'string' && params.text.trim()) {
    return params.text.trim();
  }
  const content = params.content;
  if (typeof content === 'string' && content.trim()) {
    return content.trim();
  }
  if (isRecord(content) && typeof content.text === 'string') {
    return content.text.trim();
  }
  if (Array.isArray(content)) {
    return content
      .map((item) =>
        isRecord(item) && typeof item.text === 'string' ? item.text : '',
      )
      .filter(Boolean)
      .join('\n')
      .trim();
  }
  return '';
}

function safeHttpUrl(raw: unknown): string | null {
  if (typeof raw !== 'string') return null;
  try {
    const parsed = new URL(raw.trim());
    if (parsed.protocol === 'https:' || parsed.protocol === 'http:') {
      return parsed.toString();
    }
  } catch {
    // invalid URL
  }
  return null;
}

/**
 * Parses an incoming postMessage payload from an embedded MCP App iframe.
 * Returns null for resize/theme/unrecognized messages.
 */
export function parseMcpAppMessage(
  data: unknown,
  expectedFrameId?: string,
): McpAppActionRequest | null {
  if (!isRecord(data)) return null;
  if (
    expectedFrameId &&
    typeof data.frameId === 'string' &&
    data.frameId !== expectedFrameId
  ) {
    return null;
  }

  // Standard MCP Apps JSON-RPC 2.0 protocol messages
  if (typeof data.method === 'string') {
    const params = isRecord(data.params) ? data.params : {};
    const requestId =
      typeof data.id === 'string' || typeof data.id === 'number'
        ? data.id
        : undefined;

    if (data.method === 'ui/message') {
      const prompt = extractMessageText(params);
      if (!prompt) return null;
      return {
        kind: 'prompt',
        action: 'ui/message',
        label: 'Send message to thread',
        description: prompt,
        requestId,
        jsonrpc: true,
        prompt,
      };
    }

    if (data.method === 'tools/call') {
      const name = typeof params.name === 'string' ? params.name.trim() : '';
      if (!name) return null;
      const args = isRecord(params.arguments) ? params.arguments : undefined;
      return {
        kind: 'tool',
        action: name,
        label: name,
        requestId,
        jsonrpc: true,
        params: args,
      };
    }

    if (data.method === 'ui/open-link') {
      const url = safeHttpUrl(params.url);
      if (!url) return null;
      return {
        kind: 'open_link',
        action: 'ui/open-link',
        label: 'Open external link',
        description: url,
        requestId,
        jsonrpc: true,
        url,
      };
    }
  }

  // Agenthold MCP App action message
  if (data.type === 'agenthold:mcp_action' || data.type === 'mcp_app:action') {
    const action = typeof data.action === 'string' ? data.action.trim() : '';
    if (!action) return null;
    const label =
      typeof data.label === 'string' && data.label.trim()
        ? data.label.trim()
        : action;
    const description =
      typeof data.description === 'string' && data.description.trim()
        ? data.description.trim()
        : undefined;
    const prompt =
      typeof data.prompt === 'string' && data.prompt.trim()
        ? data.prompt.trim()
        : undefined;
    const params = isRecord(data.params) ? data.params : undefined;
    const requestId =
      typeof data.requestId === 'string' || typeof data.requestId === 'number'
        ? data.requestId
        : undefined;
    return {
      kind: prompt ? 'prompt' : 'action',
      action,
      label,
      description,
      requestId,
      prompt,
      params,
    };
  }

  return null;
}
