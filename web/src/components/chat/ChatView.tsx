import { lazy, Suspense, useLayoutEffect, useRef, useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { Check, ChevronRight, Circle, Loader2 } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { ErrorText } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { ToolCard } from '@/components/chat/ToolCard';
import { useSessionStream } from '@/components/chat/useSessionStream';
import { meQuery, type SandboxState } from '@/api/queries';
import { Avatar } from '@/components/MembersList';
import type { ChatItem, PlanEntry, Viewer } from '@/lib/session';

const Markdown = lazy(() => import('@/components/chat/Markdown'));

function Thought({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="text-sm text-muted-foreground">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex items-center gap-1 italic"
      >
        <ChevronRight
          className={`size-3 transition-transform ${open ? 'rotate-90' : ''}`}
          aria-hidden
        />
        Thinking
      </button>
      {open && <p className="mt-1 whitespace-pre-wrap">{text}</p>}
    </div>
  );
}

function Plan({ entries }: { entries: PlanEntry[] }) {
  return (
    <ul
      aria-label="Plan"
      className="grid gap-1 rounded-lg border bg-card p-3 text-sm"
    >
      {entries.map((e, i) => (
        <li key={i} className="flex items-start gap-2">
          {e.status === 'completed' ? (
            <Check className="mt-0.5 size-4 text-emerald-600" aria-hidden />
          ) : e.status === 'in_progress' ? (
            <Loader2 className="mt-0.5 size-4 animate-spin" aria-hidden />
          ) : (
            <Circle
              className="mt-0.5 size-4 text-muted-foreground"
              aria-hidden
            />
          )}
          <span className={e.status === 'completed' ? 'line-through' : ''}>
            {e.content}
          </span>
        </li>
      ))}
    </ul>
  );
}

function Permission({
  item,
  sessionId,
  operator,
}: {
  item: Extract<ChatItem, { kind: 'permission' }>;
  sessionId: string;
  operator: boolean;
}) {
  const answer = useMutation({
    mutationFn: (optionId: string) =>
      unwrap(
        api.POST('/sessions/{sessionId}/permissions/{requestId}', {
          params: { path: { sessionId, requestId: item.requestId } },
          body: { optionId },
        }),
      ),
  });
  if (item.resolved) {
    const opt = item.options.find(
      (o) => o.optionId === item.resolved?.optionId,
    );
    const name = opt?.name ?? item.resolved.optionId;
    return (
      <p className="text-xs text-muted-foreground">
        Permission for {item.title}:{' '}
        {item.resolved.by === 'auto' ? `auto-approved (${name})` : name}
      </p>
    );
  }
  return (
    <div
      role="group"
      aria-label="Permission request"
      className="grid gap-2 rounded-lg border border-amber-500 bg-amber-500/10 p-3 text-sm"
    >
      <p>
        Permission requested: <strong>{item.title}</strong>
      </p>
      {operator ? (
        <div className="flex flex-wrap gap-2">
          {item.options.map((o) => (
            <Button
              key={o.optionId}
              size="sm"
              variant={o.kind?.startsWith('reject') ? 'outline' : 'default'}
              disabled={answer.isPending}
              onClick={() => answer.mutate(o.optionId)}
            >
              {o.name}
            </Button>
          ))}
        </div>
      ) : (
        <p className="text-muted-foreground">Waiting for an operator.</p>
      )}
      <ErrorText error={answer.error} />
    </div>
  );
}

function Item({
  item,
  sessionId,
  operator,
}: {
  item: ChatItem;
  sessionId: string;
  operator: boolean;
}) {
  switch (item.kind) {
    case 'user':
      return (
        <div className="ml-auto max-w-[85%] rounded-lg bg-primary px-3 py-2 text-sm text-primary-foreground">
          {item.user && <div className="text-xs opacity-70">{item.user}</div>}
          <p className="whitespace-pre-wrap">{item.text}</p>
        </div>
      );
    case 'agent':
      return (
        <div className="max-w-[90%] text-sm">
          <Suspense
            fallback={<p className="whitespace-pre-wrap">{item.text}</p>}
          >
            <Markdown text={item.text} />
          </Suspense>
        </div>
      );
    case 'thought':
      return <Thought text={item.text} />;
    case 'tool':
      return <ToolCard tool={item} />;
    case 'permission':
      return (
        <Permission item={item} sessionId={sessionId} operator={operator} />
      );
    case 'turn_end':
      return item.stopReason === 'end_turn' || !item.stopReason ? null : (
        <p className="text-xs text-muted-foreground">
          Turn ended: {item.stopReason}
        </p>
      );
    case 'error':
      return (
        <p role="alert" className="text-center text-sm text-destructive">
          {item.message}
        </p>
      );
    case 'notice':
      return (
        <p className="text-center text-xs text-muted-foreground">
          {item.message}
        </p>
      );
  }
}

function Composer({
  sessionId,
  running,
  sleeping,
}: {
  sessionId: string;
  running: boolean;
  sleeping: boolean;
}) {
  const [text, setText] = useState('');
  const path = { params: { path: { sessionId } } };
  const send = useMutation({
    mutationFn: (t: string) =>
      unwrap(
        api.POST('/sessions/{sessionId}/prompt', {
          ...path,
          body: { text: t },
        }),
      ),
    onSuccess: () => setText(''),
  });
  const cancel = useMutation({
    mutationFn: () => unwrap(api.POST('/sessions/{sessionId}/cancel', path)),
  });
  const submit = () => {
    const t = text.trim();
    if (t && !running && !send.isPending) send.mutate(t);
  };
  return (
    <div className="grid gap-2">
      <ErrorText error={send.error ?? cancel.error} />
      <div className="flex items-end gap-2">
        <textarea
          aria-label="Message"
          rows={2}
          value={text}
          disabled={running}
          placeholder={
            running
              ? 'Agent is working...'
              : sleeping
                ? 'Send a message to wake the agent…'
                : 'Message the agent'
          }
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (
              e.key === 'Enter' &&
              !e.shiftKey &&
              !e.nativeEvent.isComposing
            ) {
              e.preventDefault();
              submit();
            }
          }}
          className="min-h-[3rem] flex-1 resize-y rounded-md border border-input bg-background px-3 py-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
        />
        {running ? (
          <Button
            variant="outline"
            disabled={cancel.isPending}
            onClick={() => cancel.mutate()}
          >
            Cancel
          </Button>
        ) : (
          <Button disabled={!text.trim() || send.isPending} onClick={submit}>
            Send
          </Button>
        )}
      </div>
    </div>
  );
}

function Presence({ viewers }: { viewers: Viewer[] }) {
  const me = useQuery(meQuery);
  const myId = me.data?.user.id;
  if (viewers.length === 0) return null;
  return (
    <ul
      aria-label="Viewers"
      className="flex flex-wrap items-center justify-end gap-1"
    >
      {viewers.map((v) => {
        const label = v.id === myId ? `${v.name} (you)` : v.name;
        return (
          <li key={v.id} title={label} aria-label={label}>
            <Avatar name={v.name} url={v.avatarUrl} size="sm" />
          </li>
        );
      })}
    </ul>
  );
}

export default function ChatView({
  sessionId,
  operator,
  initialRunning = false,
  agentState,
}: {
  sessionId: string;
  operator: boolean;
  agentState?: SandboxState;
  initialRunning?: boolean;
}) {
  const { state, running, connected, viewers } = useSessionStream(
    sessionId,
    initialRunning,
  );
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [state, running]);

  return (
    <div className="flex h-[65vh] min-h-[400px] flex-col gap-2">
      <Presence viewers={viewers} />
      <div
        ref={scroller}
        data-testid="chat-scroll"
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        }}
        className="flex flex-1 flex-col gap-3 overflow-y-auto rounded-lg border p-3"
      >
        {state.items.length === 0 && (
          <p className="text-sm text-muted-foreground">No messages yet.</p>
        )}
        {state.items.map((item, i) => (
          <Item
            key={`${item.kind}-${item.seq}-${i}`}
            item={item}
            sessionId={sessionId}
            operator={operator}
          />
        ))}
        {state.plan && state.plan.length > 0 && <Plan entries={state.plan} />}
        {running && (
          <p
            role="status"
            className="flex items-center gap-2 text-sm text-muted-foreground"
          >
            <Loader2 className="size-4 animate-spin" aria-hidden />
            {agentState === 'sleeping' || agentState === 'starting'
              ? 'Waking the agent…'
              : 'working...'}
          </p>
        )}
      </div>
      {!connected && (
        <p className="text-xs text-muted-foreground">Connecting...</p>
      )}
      {operator ? (
        <Composer
          sessionId={sessionId}
          running={running}
          sleeping={agentState === 'sleeping'}
        />
      ) : (
        <p className="text-sm text-muted-foreground">
          You have read-only access to this session.
        </p>
      )}
    </div>
  );
}
