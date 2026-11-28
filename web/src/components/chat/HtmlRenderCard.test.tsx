import { act, fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, mock } from 'bun:test';
import { HtmlRenderCard } from './HtmlRenderCard';
import { setTheme } from '@/lib/theme';
import type { VisualItem } from '@/lib/session';

const sampleItem: VisualItem = {
  kind: 'visual',
  seq: 7,
  title: 'Latency Breakdown',
  html: '<div id="chart"><h1>p99: 42ms</h1></div>',
  height: 240,
};

beforeEach(() => {
  setTheme('dark');
});

describe('HtmlRenderCard', () => {
  it('renders inside a tightly sandboxed iframe with injected CSP and theme vars', () => {
    render(<HtmlRenderCard item={sampleItem} sessionId="ses_1" />);
    const iframe = screen.getByTitle('Latency Breakdown') as HTMLIFrameElement;
    expect(iframe).toBeInTheDocument();

    // Strict sandbox: allow-scripts ONLY (no allow-same-origin, no allow-top-navigation, no allow-forms).
    expect(iframe.getAttribute('sandbox')).toBe('allow-scripts');

    const srcDoc = iframe.getAttribute('srcdoc') ?? '';
    expect(srcDoc).toContain('http-equiv="Content-Security-Policy"');
    expect(srcDoc).toContain("connect-src 'none'");
    expect(srcDoc).toContain("frame-src 'none'");
    expect(srcDoc).toContain('--bg:');
    expect(srcDoc).toContain('--fg:');
    expect(srcDoc).toContain('--muted:');
    expect(srcDoc).toContain('--border:');
    expect(srcDoc).toContain('--accent:');
    expect(srcDoc).toContain('<div id="chart"><h1>p99: 42ms</h1></div>');
    expect(iframe.style.height).toBe('240px');
  });

  it('auto-sizes height via safe postMessage and clamps to 120px-900px', () => {
    render(<HtmlRenderCard item={sampleItem} />);
    const iframe = screen.getByTitle('Latency Breakdown') as HTMLIFrameElement;
    const srcDoc = iframe.getAttribute('srcdoc') ?? '';
    const match = /var FRAME_ID = ("[^"]+");/.exec(srcDoc);
    expect(match).not.toBeNull();
    const frameId = JSON.parse(match![1]) as string;

    const fakeWin = {} as Window;
    Object.defineProperty(iframe, 'contentWindow', {
      configurable: true,
      value: fakeWin,
    });

    // Valid resize within bounds
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: fakeWin,
          data: { type: 'agenthold:resize', frameId, height: 510 },
        }),
      );
    });
    expect(iframe.style.height).toBe('510px');

    // Clamp below minimum
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: fakeWin,
          data: { type: 'agenthold:resize', frameId, height: 30 },
        }),
      );
    });
    expect(iframe.style.height).toBe('120px');

    // Clamp above maximum
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: fakeWin,
          data: { type: 'agenthold:resize', frameId, height: 2400 },
        }),
      );
    });
    expect(iframe.style.height).toBe('900px');

    // Ignores messages from other windows or mismatched frameId
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: {} as Window,
          data: { type: 'agenthold:resize', frameId, height: 300 },
        }),
      );
      window.dispatchEvent(
        new MessageEvent('message', {
          source: fakeWin,
          data: { type: 'agenthold:resize', frameId: 'wrong-id', height: 300 },
        }),
      );
    });
    expect(iframe.style.height).toBe('900px');
  });

  it('toggles View source, supports Download HTML, and opens/closes Fullscreen modal with Esc', () => {
    const createUrl = mock(() => 'blob:mock-visual');
    const revokeUrl = mock(() => {});
    const origCreate = URL.createObjectURL;
    const origRevoke = URL.revokeObjectURL;
    URL.createObjectURL = createUrl;
    URL.revokeObjectURL = revokeUrl;
    try {
      render(<HtmlRenderCard item={sampleItem} />);

      // View source toggle
      fireEvent.click(screen.getByRole('button', { name: 'View source' }));
      expect(screen.getByTestId('html-render-source')).toHaveTextContent(
        '<div id="chart"><h1>p99: 42ms</h1></div>',
      );
      expect(screen.queryByTitle('Latency Breakdown')).toBeNull();

      // Toggle back to rendered preview
      fireEvent.click(screen.getByRole('button', { name: 'View rendered' }));
      expect(screen.getByTitle('Latency Breakdown')).toBeInTheDocument();

      // Download HTML
      fireEvent.click(screen.getByRole('button', { name: 'Download HTML' }));
      expect(createUrl).toHaveBeenCalledTimes(1);
      expect(revokeUrl).toHaveBeenCalledWith('blob:mock-visual');

      // Fullscreen modal opens and closes with Escape
      fireEvent.click(screen.getByRole('button', { name: 'Fullscreen' }));
      const dialog = screen.getByRole('dialog');
      expect(dialog).toBeInTheDocument();
      expect(
        screen.getByTitle('Latency Breakdown (fullscreen)'),
      ).toHaveAttribute('sandbox', 'allow-scripts');

      fireEvent.keyDown(dialog, { key: 'Escape' });
      expect(screen.queryByRole('dialog')).toBeNull();
    } finally {
      URL.createObjectURL = origCreate;
      URL.revokeObjectURL = origRevoke;
    }
  });

  it('adapts theme live when document light/dark mode switches', async () => {
    render(<HtmlRenderCard item={sampleItem} />);
    const iframe = screen.getByTitle('Latency Breakdown') as HTMLIFrameElement;
    const posted: unknown[] = [];
    const fakeWin = {
      postMessage: (msg: unknown) => posted.push(msg),
    } as unknown as Window;
    Object.defineProperty(iframe, 'contentWindow', {
      configurable: true,
      value: fakeWin,
    });

    act(() => {
      setTheme('light');
    });

    await act(async () => {
      await Promise.resolve();
    });

    const themeMsg = posted.find(
      (m) =>
        typeof m === 'object' &&
        m !== null &&
        (m as Record<string, unknown>).type === 'agenthold:theme',
    ) as Record<string, unknown> | undefined;
    expect(themeMsg).toBeDefined();
    expect(themeMsg?.theme).toBe('light');
    expect((themeMsg?.vars as Record<string, string>)['--bg']).toBe('#ffffff');
  });
});
