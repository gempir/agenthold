import { expect, it } from 'vitest';
import {
  applyEvent,
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
});
