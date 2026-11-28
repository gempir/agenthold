import type { components } from '@/api/schema';

export type SessionEvent = components['schemas']['SessionEvent'];
export type QueuedMessage = components['schemas']['QueuedMessage'];

export type ToolKind =
  | 'read'
  | 'edit'
  | 'delete'
  | 'move'
  | 'search'
  | 'execute'
  | 'think'
  | 'fetch'
  | 'other';
export type ToolStatus = 'pending' | 'in_progress' | 'completed' | 'failed';

export type ToolContent =
  | { type: 'content'; content: { type: string; text?: string } }
  | { type: 'diff'; path: string; oldText?: string | null; newText: string }
  | { type: 'terminal'; terminalId: string };

export interface ToolLocation {
  path: string;
  line?: number | null;
}

export interface ToolCall {
  kind: 'tool';
  id: string;
  seq: number;
  title: string;
  toolKind: ToolKind;
  status: ToolStatus;
  content: ToolContent[];
  locations: ToolLocation[];
  rawInput?: unknown;
  rawOutput?: unknown;
}

export interface PermissionOption {
  optionId: string;
  name: string;
  kind?: string;
}

export interface PlanEntry {
  content: string;
  priority?: string;
  status: string;
}

export interface SubthreadItem {
  kind: 'subthread';
  seq: number;
  sessionId: string;
  agentId: string;
  title: string;
  harness: string;
  model?: string;
  cwd?: string;
  branch?: string;
  status: 'running' | 'completed' | 'failed' | 'idle';
  summary?: string;
}

export type ChatItem =
  | {
      kind: 'user';
      seq: number;
      text: string;
      user?: string;
      images?: { mimeType: string; data: string }[];
      chunked: boolean;
    }
  | { kind: 'agent'; seq: number; text: string }
  | { kind: 'thought'; seq: number; text: string }
  | ToolCall
  | SubthreadItem
  | {
      kind: 'permission';
      seq: number;
      requestId: string;
      title: string;
      options: PermissionOption[];
      resolved?: { optionId: string; by: string };
    }
  | { kind: 'turn_end'; seq: number; stopReason: string }
  | { kind: 'notice' | 'error'; seq: number; message: string };

export interface ContextUsage {
  used: number;
  size: number;
  costAmount?: number;
  costCurrency?: string;
}

export interface ChatState {
  items: ChatItem[];
  plan: PlanEntry[] | null;
  usage: ContextUsage | null;
  lastSeq: number;
}

export const initialChatState: ChatState = {
  items: [],
  plan: null,
  usage: null,
  lastSeq: 0,
};

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj =>
  !!v && typeof v === 'object' && !Array.isArray(v);
const str = (v: unknown, d = ''): string => (typeof v === 'string' ? v : d);

function chunkText(u: Obj): string {
  const c = u.content;
  return isObj(c) && typeof c.text === 'string' ? c.text : '';
}

function appendChunk(
  items: ChatItem[],
  kind: 'agent' | 'thought' | 'user',
  seq: number,
  text: string,
): ChatItem[] {
  const last = items[items.length - 1];
  if (last && last.kind === kind && (last.kind !== 'user' || last.chunked)) {
    return [...items.slice(0, -1), { ...last, text: last.text + text }];
  }
  if (kind === 'user')
    return [...items, { kind, seq, text, chunked: true } as ChatItem];
  return [...items, { kind, seq, text }];
}

function toolFields(u: Obj): Partial<ToolCall> {
  const out: Partial<ToolCall> = {};
  if (typeof u.title === 'string') out.title = u.title;
  if (typeof u.kind === 'string') out.toolKind = u.kind as ToolKind;
  if (typeof u.status === 'string') out.status = u.status as ToolStatus;
  if (Array.isArray(u.content)) out.content = u.content as ToolContent[];
  if (Array.isArray(u.locations)) out.locations = u.locations as ToolLocation[];
  if (u.rawInput != null) out.rawInput = u.rawInput;
  if (u.rawOutput != null) out.rawOutput = u.rawOutput;
  return out;
}

function upsertTool(items: ChatItem[], seq: number, u: Obj): ChatItem[] {
  const id = str(u.toolCallId);
  const idx = items.findIndex((i) => i.kind === 'tool' && i.id === id);
  const fields = toolFields(u);
  if (idx >= 0) {
    const next = items.slice();
    next[idx] = { ...(items[idx] as ToolCall), ...fields };
    return next;
  }
  const t: ToolCall = {
    kind: 'tool',
    id,
    seq,
    title: id,
    toolKind: 'other',
    status: 'pending',
    content: [],
    locations: [],
    ...fields,
  };
  return [...items, t];
}

function applyUpdate(s: ChatState, seq: number, u: Obj): ChatState {
  switch (u.sessionUpdate) {
    case 'agent_message_chunk':
      return { ...s, items: appendChunk(s.items, 'agent', seq, chunkText(u)) };
    case 'agent_thought_chunk':
      return {
        ...s,
        items: appendChunk(s.items, 'thought', seq, chunkText(u)),
      };
    case 'user_message_chunk':
      return { ...s, items: appendChunk(s.items, 'user', seq, chunkText(u)) };
    case 'tool_call':
    case 'tool_call_update':
      return { ...s, items: upsertTool(s.items, seq, u) };
    case 'plan':
      return {
        ...s,
        plan: Array.isArray(u.entries) ? (u.entries as PlanEntry[]) : s.plan,
      };
    case 'usage_update': {
      const used = typeof u.used === 'number' ? u.used : (s.usage?.used ?? 0);
      const size = typeof u.size === 'number' ? u.size : (s.usage?.size ?? 0);
      const cost = isObj(u.cost) ? u.cost : undefined;
      return {
        ...s,
        usage: {
          used,
          size,
          costAmount:
            typeof cost?.amount === 'number'
              ? cost.amount
              : s.usage?.costAmount,
          costCurrency:
            typeof cost?.currency === 'string'
              ? cost.currency
              : s.usage?.costCurrency,
        },
      };
    }
    default:
      return s;
  }
}

/** Pure reducer: folds one event into the render model. Events at or below
 * lastSeq are ignored so reconnect replays never duplicate content. */
export function applyEvent(s: ChatState, ev: SessionEvent): ChatState {
  if (ev.seq <= s.lastSeq) return s;
  const d: Obj = isObj(ev.data) ? ev.data : {};
  let next: ChatState = s;
  switch (ev.type) {
    case 'prompt': {
      const user = isObj(d.user) ? str(d.user.name) : undefined;
      next = {
        ...s,
        items: [
          ...s.items,
          {
            kind: 'user',
            seq: ev.seq,
            text: str(d.text),
            user,
            ...(Array.isArray(d.images) && d.images.length
              ? {
                  images: d.images.filter(isObj).map((im) => ({
                    mimeType: str(im.mimeType),
                    data: str(im.data),
                  })),
                }
              : {}),
            chunked: false,
          },
        ],
      };
      break;
    }
    case 'update':
      next = applyUpdate(s, ev.seq, d);
      break;
    case 'permission': {
      const tc = isObj(d.toolCall) ? d.toolCall : {};
      next = {
        ...s,
        items: [
          ...s.items,
          {
            kind: 'permission',
            seq: ev.seq,
            requestId: str(d.requestId),
            title: str(tc.title, str(tc.toolCallId, 'Permission requested')),
            options: Array.isArray(d.options)
              ? (d.options as PermissionOption[])
              : [],
          },
        ],
      };
      break;
    }
    case 'permission_resolved': {
      const rid = str(d.requestId);
      next = {
        ...s,
        items: s.items.map((i) =>
          i.kind === 'permission' && i.requestId === rid
            ? {
                ...i,
                resolved: { optionId: str(d.optionId), by: str(d.by) },
              }
            : i,
        ),
      };
      break;
    }
    case 'turn_end':
      next = {
        ...s,
        items: [
          ...s.items,
          { kind: 'turn_end', seq: ev.seq, stopReason: str(d.stopReason) },
        ],
      };
      break;
    case 'error':
    case 'notice':
      next = {
        ...s,
        items: [
          ...s.items,
          { kind: ev.type, seq: ev.seq, message: str(d.message) },
        ],
      };
      break;
    case 'subthread': {
      const sessionId = str(d.sessionId);
      if (!sessionId) break;
      const rawStatus = str(d.status, 'running');
      const status: SubthreadItem['status'] =
        rawStatus === 'completed' ||
        rawStatus === 'failed' ||
        rawStatus === 'idle'
          ? rawStatus
          : 'running';
      const idx = s.items.findIndex(
        (i) => i.kind === 'subthread' && i.sessionId === sessionId,
      );
      if (idx >= 0) {
        const prev = s.items[idx] as SubthreadItem;
        const updated: SubthreadItem = {
          ...prev,
          title: str(d.title) || prev.title,
          harness: str(d.harness) || prev.harness,
          model: str(d.model) || prev.model,
          cwd: str(d.cwd) || prev.cwd,
          branch: str(d.branch) || prev.branch,
          status,
          summary: str(d.summary) || prev.summary,
        };
        const items = s.items.slice();
        items[idx] = updated;
        next = { ...s, items };
      } else {
        const entry: SubthreadItem = {
          kind: 'subthread',
          seq: ev.seq,
          sessionId,
          agentId: str(d.agentId),
          title: str(d.title, 'Sub-thread'),
          harness: str(d.harness),
          ...(str(d.model) && { model: str(d.model) }),
          ...(str(d.cwd) && { cwd: str(d.cwd) }),
          ...(str(d.branch) && { branch: str(d.branch) }),
          status,
          ...(str(d.summary) && { summary: str(d.summary) }),
        };
        next = { ...s, items: [...s.items, entry] };
      }
      break;
    }
  }
  return { ...next, lastSeq: ev.seq };
}

export function chatReducer(
  s: ChatState,
  action: { type: 'event'; event: SessionEvent } | { type: 'reset' },
): ChatState {
  return action.type === 'reset'
    ? initialChatState
    : applyEvent(s, action.event);
}

export type ViewerActivity = 'idle' | 'typing' | 'viewing_diff';

export interface Viewer {
  id: string;
  name: string;
  avatarUrl: string;
  role: string;
  activity?: ViewerActivity;
}

export type StreamMessage =
  | { type: 'status'; running: boolean }
  | { type: 'presence'; viewers: Viewer[] }
  | {
      type: 'queue_updated';
      sessionId: string;
      messages: QueuedMessage[];
      count: number;
    }
  | {
      type: 'guest_request';
      sessionId: string;
      request: components['schemas']['GuestRequest'];
    }
  | {
      type: 'review_comments_updated';
      sessionId: string;
      comments: components['schemas']['SessionReviewComment'][];
    }
  | { type: 'event'; event: SessionEvent };

/** Parses a stream text frame; null for anything unrecognised. */
export function parseStreamMessage(text: string): StreamMessage | null {
  try {
    const m: unknown = JSON.parse(text);
    if (!isObj(m)) return null;
    if (m.type === 'status' && typeof m.running === 'boolean')
      return { type: 'status', running: m.running };
    if (m.type === 'presence' && Array.isArray(m.viewers)) {
      const viewers = m.viewers.filter(isObj).map((v) => {
        const rawAct = str(v.activity);
        const activity: ViewerActivity | undefined =
          rawAct === 'typing' || rawAct === 'viewing_diff' || rawAct === 'idle'
            ? rawAct
            : undefined;
        return {
          id: str(v.id),
          name: str(v.name),
          avatarUrl: str(v.avatarUrl),
          role: str(v.role),
          ...(activity ? { activity } : {}),
        };
      });
      return { type: 'presence', viewers: viewers.filter((v) => v.id) };
    }
    if (m.type === 'guest_request' && isObj(m.request)) {
      return {
        type: 'guest_request',
        sessionId: str(m.sessionId),
        request: m.request as unknown as components['schemas']['GuestRequest'],
      };
    }
    if (m.type === 'review_comments_updated' && Array.isArray(m.comments)) {
      return {
        type: 'review_comments_updated',
        sessionId: str(m.sessionId),
        comments:
          m.comments as unknown as components['schemas']['SessionReviewComment'][],
      };
    }
    if (m.type === 'queue_updated' && Array.isArray(m.messages)) {
      const messages: QueuedMessage[] = m.messages.filter(isObj).map((q) => ({
        id: str(q.id),
        sessionId: str(q.sessionId, str(m.sessionId)),
        userId: str(q.userId),
        userName: str(q.userName),
        text: str(q.text),
        steer: Boolean(q.steer),
        createdAt: str(q.createdAt),
        ...(Array.isArray(q.images) && q.images.length
          ? {
              images: q.images.filter(isObj).map((im) => ({
                mimeType: str(im.mimeType) as NonNullable<
                  QueuedMessage['images']
                >[number]['mimeType'],
                data: str(im.data),
              })),
            }
          : {}),
      }));
      return {
        type: 'queue_updated',
        sessionId: str(m.sessionId),
        messages,
        count: typeof m.count === 'number' ? m.count : messages.length,
      };
    }
    if (typeof m.seq === 'number' && typeof m.type === 'string')
      return { type: 'event', event: m as unknown as SessionEvent };
  } catch {
    // not JSON
  }
  return null;
}

export function sessionStreamUrl(
  loc: { protocol: string; host: string },
  sessionId: string,
  after: number,
): string {
  const scheme = loc.protocol === 'https:' ? 'wss' : 'ws';
  return `${scheme}://${loc.host}/api/v1/sessions/${encodeURIComponent(sessionId)}/stream?after=${after}`;
}

export function guestStreamUrl(
  loc: { protocol: string; host: string },
  token: string,
  after: number,
): string {
  const scheme = loc.protocol === 'https:' ? 'wss' : 'ws';
  return `${scheme}://${loc.host}/api/v1/guest/${encodeURIComponent(token)}/stream?after=${after}`;
}

/** Reconnect delay: 500ms doubling up to 10s. */
export const backoffMs = (attempt: number) =>
  Math.min(10_000, 500 * 2 ** attempt);

export type ChatGroup =
  { kind: 'work'; items: ChatItem[] } | { kind: 'item'; item: ChatItem };

const isWork = (i: ChatItem) => i.kind === 'tool' || i.kind === 'thought';

/** Folds runs of two or more consecutive tool calls and thoughts into one
 * "work" group so the conversation reads as messages, not as a log. */
export function groupItems(items: ChatItem[]): ChatGroup[] {
  const out: ChatGroup[] = [];
  let run: ChatItem[] = [];
  const flush = () => {
    if (run.length === 1) out.push({ kind: 'item', item: run[0] });
    else if (run.length > 1) out.push({ kind: 'work', items: run });
    run = [];
  };
  for (const i of items) {
    if (isWork(i)) run.push(i);
    else {
      flush();
      out.push({ kind: 'item', item: i });
    }
  }
  flush();
  return out;
}
