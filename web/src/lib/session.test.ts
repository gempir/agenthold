import { describe, expect, it } from 'bun:test';
import {
  applyEvent,
  groupItems,
  type ChatItem,
  initialChatState,
  parseStreamMessage,
  type ChatState,
  type SessionEvent,
  type ToolCall,
} from './session';

let n = 0;
const ev = (
  type: SessionEvent['type'],
  data: Record<string, unknown>,
  seq = ++n,
): SessionEvent => ({ seq, at: '2030-01-01T00:00:00Z', type, data });
const upd = (u: Record<string, unknown>, seq?: number) => ev('update', u, seq);
const fold = (evs: SessionEvent[]): ChatState =>
  evs.reduce(applyEvent, initialChatState);
const chunk = (kind: string, text: string) =>
  upd({ sessionUpdate: kind, content: { type: 'text', text } });

it('concatenates consecutive chunks of the same kind', () => {
  n = 0;
  const s = fold([
    chunk('agent_message_chunk', 'Hel'),
    chunk('agent_message_chunk', 'lo'),
    chunk('agent_thought_chunk', 'hmm'),
    chunk('agent_message_chunk', 'again'),
  ]);
  expect(s.items.map((i) => [i.kind, 'text' in i && i.text])).toEqual([
    ['agent', 'Hello'],
    ['thought', 'hmm'],
    ['agent', 'again'],
  ]);
  expect(s.lastSeq).toBe(4);
});

it('keeps prompts separate from user chunks', () => {
  n = 0;
  const s = fold([
    ev('prompt', { text: 'a', user: { id: 'u', name: 'Al' } }),
    ev('prompt', { text: 'b', user: { id: 'u', name: 'Al' } }),
  ]);
  expect(s.items).toHaveLength(2);
});

it('merges tool_call_update into the tool call', () => {
  n = 0;
  const s = fold([
    upd({
      sessionUpdate: 'tool_call',
      toolCallId: 't1',
      title: 'Edit a.go',
      kind: 'edit',
      status: 'pending',
    }),
    upd({
      sessionUpdate: 'tool_call_update',
      toolCallId: 't1',
      status: 'completed',
      content: [{ type: 'diff', path: 'a.go', oldText: 'x', newText: 'y' }],
    }),
  ]);
  expect(s.items).toHaveLength(1);
  const t = s.items[0] as ToolCall;
  expect(t).toMatchObject({
    title: 'Edit a.go',
    toolKind: 'edit',
    status: 'completed',
  });
  expect(t.content).toHaveLength(1);
});

it('tracks permission resolution', () => {
  n = 0;
  const perm = ev('permission', {
    requestId: 'r1',
    toolCall: { toolCallId: 't1', title: 'Run ls' },
    options: [{ optionId: 'allow', name: 'Allow', kind: 'allow_once' }],
    policy: 'ask',
  });
  const open = fold([perm]);
  expect(open.items[0]).toMatchObject({ kind: 'permission', title: 'Run ls' });
  expect(open.items[0]).not.toHaveProperty('resolved');
  const done = applyEvent(
    open,
    ev('permission_resolved', {
      requestId: 'r1',
      optionId: 'allow',
      by: 'auto',
    }),
  );
  expect(done.items[0]).toMatchObject({
    resolved: { optionId: 'allow', by: 'auto' },
  });
});

it('ignores duplicate or older seq', () => {
  n = 0;
  const a = chunk('agent_message_chunk', 'x');
  const s1 = fold([a]);
  const s2 = applyEvent(s1, a);
  expect(s2).toBe(s1);
  expect(applyEvent(s1, { ...a, seq: 0 })).toBe(s1);
});

it('keeps latest plan and records turn_end/errors', () => {
  n = 0;
  const s = fold([
    upd({
      sessionUpdate: 'plan',
      entries: [{ content: 'a', status: 'pending' }],
    }),
    upd({
      sessionUpdate: 'plan',
      entries: [{ content: 'b', status: 'completed' }],
    }),
    upd({ sessionUpdate: 'available_commands_update' }),
    ev('turn_end', { stopReason: 'cancelled' }),
    ev('error', { message: 'boom' }),
  ]);
  expect(s.plan?.[0].content).toBe('b');
  expect(s.items.map((i) => i.kind)).toEqual(['turn_end', 'error']);
});

it('parses stream messages', () => {
  expect(parseStreamMessage('{"type":"status","running":true}')).toEqual({
    type: 'status',
    running: true,
  });
  expect(
    parseStreamMessage('{"seq":3,"at":"x","type":"notice","data":{}}'),
  ).toMatchObject({ type: 'event' });
  expect(parseStreamMessage('nope')).toBeNull();
  expect(
    parseStreamMessage(
      '{"type":"presence","viewers":[{"id":"u1","name":"A","avatarUrl":"","role":"admin"},{"name":"bad"}]}',
    ),
  ).toEqual({
    type: 'presence',
    viewers: [{ id: 'u1', name: 'A', avatarUrl: '', role: 'admin' }],
  });
});

describe('groupItems', () => {
  const tool = (seq: number): ChatItem => ({
    kind: 'tool',
    id: `t${seq}`,
    seq,
    title: 'Run',
    toolKind: 'execute',
    status: 'completed',
    content: [],
    locations: [],
  });
  it('folds runs of work and keeps single steps and messages apart', () => {
    const items: ChatItem[] = [
      { kind: 'user', seq: 1, text: 'hi', chunked: false },
      tool(2),
      { kind: 'agent', seq: 3, text: 'one' },
      { kind: 'thought', seq: 4, text: 'hmm' },
      tool(5),
      tool(6),
      { kind: 'agent', seq: 7, text: 'done' },
    ];
    const g = groupItems(items);
    expect(g.map((x) => x.kind)).toEqual([
      'item',
      'item',
      'item',
      'work',
      'item',
    ]);
    expect(g[3]).toMatchObject({ kind: 'work' });
    expect((g[3] as { items: ChatItem[] }).items).toHaveLength(3);
  });

  it('upserts subthread events in place and keeps them outside folded work groups', () => {
    let s = initialChatState;
    s = applyEvent(s, {
      seq: 1,
      at: '2030-01-01T00:00:00Z',
      type: 'subthread',
      data: {
        sessionId: 'ses_sub1',
        agentId: 'ag_1',
        title: 'Codex review',
        harness: 'codex',
        branch: 'feat/auth',
        status: 'running',
      },
    });
    expect(s.items).toHaveLength(1);
    expect(s.items[0]).toMatchObject({
      kind: 'subthread',
      sessionId: 'ses_sub1',
      harness: 'codex',
      status: 'running',
    });
    s = applyEvent(s, {
      seq: 2,
      at: '2030-01-01T00:00:10Z',
      type: 'subthread',
      data: {
        sessionId: 'ses_sub1',
        agentId: 'ag_1',
        title: 'Codex review',
        harness: 'codex',
        status: 'completed',
        summary: 'Fixed 2 edge cases and added tests.',
      },
    });
    expect(s.items).toHaveLength(1);
    expect(s.items[0]).toMatchObject({
      kind: 'subthread',
      status: 'completed',
      branch: 'feat/auth',
      summary: 'Fixed 2 edge cases and added tests.',
    });
    const g = groupItems([tool(1), s.items[0], tool(3)]);
    expect(g.map((x) => x.kind)).toEqual(['item', 'item', 'item']);
  });

  it('handles visual and mcp_app events and keeps them outside folded work groups', () => {
    n = 0;
    const s = fold([
      ev('visual', {
        title: 'Revenue Chart',
        html: '<div>chart</div>',
        height: 360,
      }),
      upd({
        sessionUpdate: 'tool_call',
        toolCallId: 'tc_1',
        title: 'Query Metrics',
        kind: 'fetch',
        status: 'completed',
      }),
      ev('mcp_app', {
        title: 'Metrics Explorer',
        html: '<div>interactive</div>',
        server: 'prometheus',
        toolCallId: 'tc_1',
        actions: [{ name: 'reload', readOnly: true }],
      }),
      ev('mcp_app', {
        title: 'Standalone Widget',
        html: '<p>widget</p>',
      }),
    ]);

    expect(s.items).toHaveLength(3);
    expect(s.items[0]).toMatchObject({
      kind: 'visual',
      title: 'Revenue Chart',
      html: '<div>chart</div>',
      height: 360,
    });
    expect(s.items[1]).toMatchObject({
      kind: 'tool',
      id: 'tc_1',
      mcpApp: {
        title: 'Metrics Explorer',
        server: 'prometheus',
        toolCallId: 'tc_1',
      },
    });
    expect(s.items[2]).toMatchObject({
      kind: 'mcp_app',
      title: 'Standalone Widget',
      html: '<p>widget</p>',
    });

    // Tools with MCP Apps and visual/mcp_app items are never buried inside folded work groups.
    const g = groupItems([
      tool(10),
      s.items[1],
      tool(11),
      s.items[0],
      tool(12),
    ]);
    expect(g.map((x) => x.kind)).toEqual([
      'item',
      'item',
      'item',
      'item',
      'item',
    ]);
  });

  it('detects MCP App UI payloads from tool_call_update _meta.ui, ui_resource, and ui:// resources', () => {
    n = 0;
    const s = fold([
      upd({
        sessionUpdate: 'tool_call',
        toolCallId: 't_meta',
        title: 'Dashboard',
        kind: 'other',
        status: 'completed',
        rawOutput: {
          _meta: {
            ui: {
              title: 'Live Dashboard',
              html: '<section>stats</section>',
              server: 'grafana',
            },
          },
        },
      }),
      upd({
        sessionUpdate: 'tool_call',
        toolCallId: 't_res',
        title: 'Inspect Form',
        kind: 'other',
        status: 'completed',
        rawOutput: {
          ui_resource: {
            uri: 'ui://forms/1',
            html: '<form>hello</form>',
          },
        },
      }),
      upd({
        sessionUpdate: 'tool_call',
        toolCallId: 't_uri',
        title: 'Chart Tool',
        kind: 'other',
        status: 'completed',
        content: [
          {
            type: 'content',
            content: {
              type: 'resource',
              resource: {
                uri: 'ui://charts/cpu',
                mimeType: 'text/html',
                text: '<div>cpu chart</div>',
              },
            },
          },
        ],
      }),
    ]);

    expect((s.items[0] as ToolCall).mcpApp).toMatchObject({
      title: 'Live Dashboard',
      html: '<section>stats</section>',
      server: 'grafana',
    });
    expect((s.items[1] as ToolCall).mcpApp).toMatchObject({
      title: 'Inspect Form',
      html: '<form>hello</form>',
      resourceUri: 'ui://forms/1',
    });
    expect((s.items[2] as ToolCall).mcpApp).toMatchObject({
      title: 'Chart Tool',
      html: '<div>cpu chart</div>',
      resourceUri: 'ui://charts/cpu',
    });
  });
});

describe('secret and account connect requests', () => {
  it('parses a secret_request into a pending card item', () => {
    n = 0;
    const s = fold([
      ev('secret_request', {
        requestId: 'sreq_1',
        name: 'STRIPE_KEY',
        description: 'Needed to create test charges',
        hosts: ['api.stripe.com', 42],
        envVar: 'STRIPE_API_KEY',
        header: 'Authorization',
      }),
    ]);
    expect(s.items).toEqual([
      {
        kind: 'secret_request',
        seq: 1,
        requestId: 'sreq_1',
        name: 'STRIPE_KEY',
        description: 'Needed to create test charges',
        hosts: ['api.stripe.com'],
        envVar: 'STRIPE_API_KEY',
        header: 'Authorization',
      },
    ]);
  });

  it('ignores a secret_request without a request id', () => {
    n = 0;
    const s = fold([ev('secret_request', { name: 'X' })]);
    expect(s.items).toEqual([]);
    expect(s.lastSeq).toBe(1);
  });

  it('resolves the matching secret request and leaves others pending', () => {
    n = 0;
    const s = fold([
      ev('secret_request', { requestId: 'a', name: 'A', hosts: [] }),
      ev('secret_request', { requestId: 'b', name: 'B', hosts: [] }),
      ev('secret_request_resolved', {
        requestId: 'a',
        status: 'submitted',
        secretId: 'sec_1',
        name: 'A',
        envVar: 'A_KEY',
        placeholder: 'ah_placeholder',
        by: 'u1',
      }),
      ev('secret_request_resolved', { requestId: 'zzz', status: 'declined' }),
    ]);
    const [a, b] = s.items as Extract<ChatItem, { kind: 'secret_request' }>[];
    expect(a.resolved).toEqual({
      status: 'submitted',
      name: 'A',
      envVar: 'A_KEY',
      placeholder: 'ah_placeholder',
      by: 'u1',
    });
    expect(b.resolved).toBeUndefined();
    expect(s.items).toHaveLength(2);
  });

  it('treats any non-submitted resolution as declined', () => {
    n = 0;
    const s = fold([
      ev('secret_request', { requestId: 'a', name: 'A', hosts: [] }),
      ev('secret_request_resolved', { requestId: 'a', status: 'declined' }),
    ]);
    const [a] = s.items as Extract<ChatItem, { kind: 'secret_request' }>[];
    expect(a.resolved).toEqual({ status: 'declined' });
  });

  it('parses account_connect_request into a connect card item', () => {
    n = 0;
    const s = fold([
      ev('account_connect_request', {
        requestId: 'acreq_1',
        toolId: 'otl_1',
        slug: 'linear',
        name: 'Linear',
        description: 'Issues',
        kind: 'executor_app',
        authMode: 'user_oauth',
        reason: 'To file the bug you described',
        connected: false,
        connectEndpoint: '/api/v1/me/org-tools/otl_1/connect',
      }),
      ev('account_connect_request', { slug: 'missing-tool-id' }),
    ]);
    expect(s.items).toEqual([
      {
        kind: 'account_connect',
        seq: 1,
        requestId: 'acreq_1',
        toolId: 'otl_1',
        slug: 'linear',
        name: 'Linear',
        description: 'Issues',
        toolKind: 'executor_app',
        authMode: 'user_oauth',
        reason: 'To file the bug you described',
        connected: false,
      },
    ]);
  });

  it('keeps secret and connect cards out of work groups', () => {
    n = 0;
    const s = fold([
      upd({ sessionUpdate: 'tool_call', toolCallId: 't1', title: 'a' }),
      ev('secret_request', { requestId: 'a', name: 'A', hosts: [] }),
      upd({ sessionUpdate: 'tool_call', toolCallId: 't2', title: 'b' }),
      upd({ sessionUpdate: 'tool_call', toolCallId: 't3', title: 'c' }),
    ]);
    expect(groupItems(s.items).map((g) => g.kind)).toEqual([
      'item',
      'item',
      'work',
    ]);
  });
});
