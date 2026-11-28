import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { describe, expect, it, mock } from 'bun:test';
import { McpAppCard } from './McpAppCard';
import type { McpAppPayload } from '@/lib/session';
import { mockApi } from '@/test-utils';

const sampleApp: McpAppPayload = {
  title: 'GitHub PR Actions',
  html: '<div><button id="merge">Merge PR</button></div>',
  server: 'github',
  actions: [
    { name: 'refresh_checks', label: 'Refresh checks', readOnly: true },
    {
      name: 'merge_pr',
      label: 'Merge Pull Request',
      description: 'Merges PR #12 into main',
      readOnly: false,
    },
  ],
};

function attachFakeWindow(iframe: HTMLIFrameElement) {
  const replies: unknown[] = [];
  const fakeWin = {
    postMessage: (msg: unknown) => replies.push(msg),
  } as unknown as Window;
  Object.defineProperty(iframe, 'contentWindow', {
    configurable: true,
    value: fakeWin,
  });
  return { fakeWin, replies };
}

function extractFrameId(iframe: HTMLIFrameElement): string {
  const srcDoc = iframe.getAttribute('srcdoc') ?? '';
  const match = /var FRAME_ID = ("[^"]+");/.exec(srcDoc);
  if (!match) throw new Error('FRAME_ID not found in srcDoc');
  return JSON.parse(match[1]) as string;
}

describe('McpAppCard', () => {
  it('renders tightly sandboxed iframe with server badge and opens/closes fullscreen with Esc', () => {
    render(<McpAppCard app={sampleApp} sessionId="s1" canUse />);
    const iframe = screen.getByTitle('GitHub PR Actions') as HTMLIFrameElement;
    expect(iframe.getAttribute('sandbox')).toBe('allow-scripts');
    expect(screen.getByText('github')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Fullscreen MCP App' }));
    const dialog = screen.getByRole('dialog');
    expect(dialog).toBeInTheDocument();
    expect(screen.getByTitle('GitHub PR Actions (fullscreen)')).toHaveAttribute(
      'sandbox',
      'allow-scripts',
    );

    fireEvent.keyDown(dialog, { key: 'Escape' });
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('intercepts non-read-only postMessage actions with Allow / Decline confirmation', async () => {
    const onExecute = mock(async () => ({ merged: true }));
    render(
      <McpAppCard
        app={sampleApp}
        sessionId="s1"
        canUse
        onExecuteAction={onExecute}
      />,
    );
    const iframe = screen.getByTitle('GitHub PR Actions') as HTMLIFrameElement;
    const frameId = extractFrameId(iframe);
    const { fakeWin, replies } = attachFakeWindow(iframe);

    // Non-read-only tool call must trigger inline confirmation banner.
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: fakeWin,
          data: {
            jsonrpc: '2.0',
            id: 1,
            frameId,
            method: 'tools/call',
            params: { name: 'merge_pr', arguments: { pr: 12 } },
          },
        }),
      );
    });

    const banner = await screen.findByTestId('mcp-app-confirm');
    expect(banner).toHaveTextContent('merge_pr');
    expect(banner).toHaveTextContent('Merges PR #12 into main');
    expect(onExecute).not.toHaveBeenCalled();

    // Decline stops execution and replies to iframe.
    fireEvent.click(screen.getByRole('button', { name: 'Decline' }));
    expect(screen.queryByTestId('mcp-app-confirm')).toBeNull();
    expect(onExecute).not.toHaveBeenCalled();
    expect(
      replies.some(
        (r) =>
          typeof r === 'object' &&
          r !== null &&
          (r as Record<string, unknown>).type === 'agenthold:mcp_result' &&
          (r as Record<string, unknown>).declined === true,
      ),
    ).toBe(true);

    // Send again and click Allow -> executes action and replies ok.
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: fakeWin,
          data: {
            jsonrpc: '2.0',
            id: 2,
            frameId,
            method: 'tools/call',
            params: { name: 'merge_pr', arguments: { pr: 12 } },
          },
        }),
      );
    });
    expect(await screen.findByTestId('mcp-app-confirm')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Allow' }));
    await waitFor(() => expect(onExecute).toHaveBeenCalledTimes(1));
    expect(screen.queryByTestId('mcp-app-confirm')).toBeNull();
    expect(
      replies.some(
        (r) =>
          typeof r === 'object' &&
          r !== null &&
          (r as Record<string, unknown>).type === 'agenthold:mcp_result' &&
          (r as Record<string, unknown>).requestId === 2 &&
          (r as Record<string, unknown>).ok === true,
      ),
    ).toBe(true);
  });

  it('sends approved ui/message prompts to the thread via API', async () => {
    const { calls } = mockApi({ 'POST /sessions/s1/prompt': { status: 202 } });
    render(<McpAppCard app={sampleApp} sessionId="s1" canUse />);
    const iframe = screen.getByTitle('GitHub PR Actions') as HTMLIFrameElement;
    const frameId = extractFrameId(iframe);
    const { fakeWin } = attachFakeWindow(iframe);

    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: fakeWin,
          data: {
            jsonrpc: '2.0',
            id: 'msg-1',
            frameId,
            method: 'ui/message',
            params: { text: 'Please rebase PR #12' },
          },
        }),
      );
    });

    const banner = await screen.findByTestId('mcp-app-confirm');
    expect(banner).toHaveTextContent('Send message to thread');
    expect(banner).toHaveTextContent('Please rebase PR #12');
    expect(calls).toHaveLength(0);

    fireEvent.click(screen.getByRole('button', { name: 'Allow' }));
    await waitFor(() =>
      expect(
        calls.find((c) => c.key === 'POST /sessions/s1/prompt')?.body,
      ).toEqual({ text: 'Please rebase PR #12' }),
    );
  });

  it('executes server-declared readOnly actions directly without trusting iframe claims', async () => {
    const onExecute = mock(async () => ({ ok: true }));
    render(
      <McpAppCard
        app={sampleApp}
        sessionId="s1"
        canUse
        onExecuteAction={onExecute}
      />,
    );
    const iframe = screen.getByTitle('GitHub PR Actions') as HTMLIFrameElement;
    const frameId = extractFrameId(iframe);
    const { fakeWin } = attachFakeWindow(iframe);

    // Server-declared readOnly action runs immediately without confirmation.
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: fakeWin,
          data: {
            type: 'agenthold:mcp_action',
            frameId,
            requestId: 'r1',
            action: 'refresh_checks',
          },
        }),
      );
    });
    await waitFor(() => expect(onExecute).toHaveBeenCalledTimes(1));
    expect(screen.queryByTestId('mcp-app-confirm')).toBeNull();

    // Undeclared action claiming readOnly: true from iframe STILL requires confirmation.
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: fakeWin,
          data: {
            type: 'agenthold:mcp_action',
            frameId,
            requestId: 'r2',
            action: 'delete_repo',
            readOnly: true,
          },
        }),
      );
    });
    expect(await screen.findByTestId('mcp-app-confirm')).toHaveTextContent(
      'delete_repo',
    );
    expect(onExecute).toHaveBeenCalledTimes(1);
  });

  it('cleans up listeners and iframe srcdoc on unmount', () => {
    const onExecute = mock(async () => ({ ok: true }));
    const { unmount } = render(
      <McpAppCard
        app={sampleApp}
        sessionId="s1"
        canUse
        onExecuteAction={onExecute}
      />,
    );
    const iframe = screen.getByTitle('GitHub PR Actions') as HTMLIFrameElement;
    const frameId = extractFrameId(iframe);
    const { fakeWin } = attachFakeWindow(iframe);

    unmount();
    expect(iframe.srcdoc).toBe('');

    // Messages after unmount are ignored.
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: fakeWin,
          data: {
            type: 'agenthold:mcp_action',
            frameId,
            requestId: 'after-unmount',
            action: 'refresh_checks',
          },
        }),
      );
    });
    expect(onExecute).not.toHaveBeenCalled();
  });
});
