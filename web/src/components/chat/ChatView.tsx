import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import {
  ArrowDown,
  ArrowUpRight,
  Check,
  ChevronRight,
  Circle,
  CircleAlert,
  GitBranch,
  ListTodo,
  Loader2,
  MessageSquare,
  ShieldCheck,
  ShieldQuestion,
  X,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { ErrorText } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { setDraft, useDraft } from '@/lib/drafts';
import {
  Composer,
  DEFAULT_COMPOSER_PLACEHOLDER,
  type ComposerSubmitOptions,
} from '@/components/chat/Composer';
import { parseSlashCommand } from '@/components/chat/ComposerMenu';
import {
  QuoteSelectionPopover,
  RewindAction,
} from '@/components/chat/MessageActions';
import { QueuedMessages } from '@/components/chat/QueuedMessages';
import { Lightbox } from '@/components/chat/Lightbox';
import {
  toImagePayload,
  uploadFileAttachments,
  useAttachments,
  type Attachment,
  type FileAttachment,
} from '@/lib/attachments';
import {
  serializeComposerContext,
  useComposerContext,
  type ComposerContextChip,
} from '@/lib/composerContext';
import { pushStash } from '@/lib/composerStash';
import { ToolCard } from '@/components/chat/ToolCard';
import { useSessionStream } from '@/components/chat/useSessionStream';
import { ActivityLink } from '@/components/chat/LiveLog';
import {
  harnessesQuery,
  meQuery,
  sessionQueueQuery,
  type QueuedMessage,
  type SandboxState,
  type Session,
} from '@/api/queries';
import { Avatar } from '@/components/Avatar';
import { HarnessIcon } from '@/components/HarnessIcon';
import {
  groupItems,
  type ChatItem,
  type ContextUsage,
  type PlanEntry,
  type SubthreadItem,
  type Viewer,
} from '@/lib/session';
import { threadLinkHandlers } from '@/components/agent/panelTabs';
import type { OpenFile } from '@/components/chat/Markdown';
import { elapsed, useNow } from '@/lib/time';
import { cn } from '@/lib/utils';

const Markdown = lazy(() => import('@/components/chat/Markdown'));

/** A quiet expandable row: the shape every secondary chat element takes. */
function Disclosure({
  label,
  icon,
  open,
  onToggle,
  children,
}: {
  label: ReactNode;
  icon?: ReactNode;
  open: boolean;
  onToggle: () => void;
  children: ReactNode;
}) {
  return (
    <div className="text-sm">
      <button
        type="button"
        aria-expanded={open}
        onClick={onToggle}
        className="group flex items-center gap-2 rounded-md py-1 text-left text-muted-foreground hover:text-foreground"
      >
        {icon}
        <span>{label}</span>
        <ChevronRight
          className={cn(
            'size-3.5 opacity-60 transition-transform group-hover:opacity-100',
            open && 'rotate-90',
          )}
          aria-hidden
        />
      </button>
      {open && <div className="mt-1 ml-1.5 border-l pl-4">{children}</div>}
    </div>
  );
}

function Thought({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  return (
    <Disclosure
      label="Thinking"
      open={open}
      onToggle={() => setOpen((o) => !o)}
    >
      <p className="py-1 text-sm whitespace-pre-wrap text-muted-foreground">
        {text}
      </p>
    </Disclosure>
  );
}

function workSummary(items: ChatItem[]): string {
  const tools = items.filter((i) => i.kind === 'tool').length;
  const thoughts = items.length - tools;
  const parts = [];
  if (tools) parts.push(`${tools} tool call${tools === 1 ? '' : 's'}`);
  if (thoughts) parts.push('thinking');
  return parts.join(', ');
}

/** Consecutive tool calls and thoughts folded into one "Worked" row. */
function WorkGroup({ items, live }: { items: ChatItem[]; live: boolean }) {
  // null follows the default: open while the agent is still on this step.
  const [choice, setChoice] = useState<boolean | null>(null);
  const open = choice ?? live;
  const failed = items.some((i) => i.kind === 'tool' && i.status === 'failed');
  return (
    <Disclosure
      label={
        <>
          {live ? 'Working' : 'Worked'}
          <span className="text-muted-foreground/70">
            {' '}
            · {workSummary(items)}
          </span>
          {failed && <span className="text-destructive"> · failed</span>}
        </>
      }
      icon={
        live ? (
          <Loader2 className="size-3.5 animate-spin text-sky-400" aria-hidden />
        ) : undefined
      }
      open={open}
      onToggle={() => setChoice(!open)}
    >
      <div className="grid gap-0.5 py-0.5">
        {items.map((it, i) =>
          it.kind === 'tool' ? (
            <ToolCard key={`${it.id}-${i}`} tool={it} />
          ) : it.kind === 'thought' ? (
            <Thought key={`t-${it.seq}-${i}`} text={it.text} />
          ) : null,
        )}
      </div>
    </Disclosure>
  );
}

function Plan({ entries }: { entries: PlanEntry[] }) {
  const [open, setOpen] = useState(true);
  const done = entries.filter((e) => e.status === 'completed').length;
  return (
    <Disclosure
      label={`Plan · ${done}/${entries.length} done`}
      icon={<ListTodo className="size-3.5" aria-hidden />}
      open={open}
      onToggle={() => setOpen((o) => !o)}
    >
      <ul aria-label="Plan" className="grid gap-1 py-1 text-sm">
        {entries.map((e, i) => (
          <li key={i} className="flex items-start gap-2">
            {e.status === 'completed' ? (
              <Check
                className="mt-0.5 size-4 shrink-0 text-emerald-500"
                aria-hidden
              />
            ) : e.status === 'in_progress' ? (
              <Loader2
                className="mt-0.5 size-4 shrink-0 animate-spin text-sky-400"
                aria-hidden
              />
            ) : (
              <Circle
                className="mt-0.5 size-4 shrink-0 text-muted-foreground"
                aria-hidden
              />
            )}
            <span
              className={
                e.status === 'completed'
                  ? 'text-muted-foreground line-through'
                  : ''
              }
            >
              {e.content}
            </span>
          </li>
        ))}
      </ul>
    </Disclosure>
  );
}

function Permission({
  item,
  sessionId,
  canUse,
}: {
  item: Extract<ChatItem, { kind: 'permission' }>;
  sessionId: string;
  canUse: boolean;
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
      <p className="flex items-center gap-2 py-1 text-sm text-muted-foreground">
        <ShieldCheck className="size-3.5 shrink-0" aria-hidden />
        <span className="min-w-0 truncate">
          Permission for {item.title}:{' '}
          {item.resolved.by === 'auto' ? `auto-approved (${name})` : name}
        </span>
      </p>
    );
  }
  return (
    <div
      role="group"
      aria-label="Permission request"
      className="grid gap-3 rounded-xl border border-amber-500/40 bg-amber-500/5 p-3 text-sm"
    >
      <p className="flex items-start gap-2">
        <ShieldQuestion
          className="mt-0.5 size-4 shrink-0 text-amber-500"
          aria-hidden
        />
        <span>
          Permission requested: <strong>{item.title}</strong>
        </span>
      </p>
      {canUse ? (
        <div className="flex flex-wrap gap-2 pl-6">
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
        <p className="pl-6 text-muted-foreground">
          Waiting for someone who can use this agent.
        </p>
      )}
      <ErrorText error={answer.error} />
    </div>
  );
}

/** The images sent with a message; a click opens them in the lightbox. */
function MessageImages({
  images,
}: {
  images: { mimeType: string; data: string }[];
}) {
  const [viewing, setViewing] = useState<number | null>(null);
  const shown = images.map((im, i) => ({
    src: `data:${im.mimeType};base64,${im.data}`,
    alt: images.length > 1 ? `Attached image ${i + 1}` : 'Attached image',
  }));
  return (
    <>
      <div className="flex flex-wrap justify-end gap-2">
        {shown.map((im, i) => (
          <button
            key={i}
            type="button"
            aria-label={`View ${im.alt.toLowerCase()}`}
            onClick={() => setViewing(i)}
            className="block cursor-zoom-in rounded-xl focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          >
            <img
              src={im.src}
              alt={im.alt}
              className="max-h-64 max-w-full rounded-xl border object-contain"
            />
          </button>
        ))}
      </div>
      <Lightbox
        images={shown}
        index={viewing}
        onIndexChange={setViewing}
        onClose={() => setViewing(null)}
      />
    </>
  );
}

/** Opening threads from the chat: beside it, or in the main view. */
type ThreadOpeners = {
  onOpenSubthread?: (sessionId: string) => void;
  onOpenSubthreadInMain?: (sessionId: string) => void;
};

function SubthreadCard({
  item,
  live,
  onOpenSubthread,
  onOpenSubthreadInMain,
}: {
  item: SubthreadItem;
  live?: Session;
} & ThreadOpeners) {
  const harnesses = useQuery(harnessesQuery);
  const harnessId = live?.harness || item.harness;
  const harnessMeta = harnesses.data?.find((h) => h.id === harnessId);
  const harnessName = harnessMeta?.name ?? harnessId;
  const title = live?.title || item.title;
  const isRunning = live ? live.running : item.status === 'running';
  const status: SubthreadItem['status'] = isRunning
    ? 'running'
    : item.status === 'failed'
      ? 'failed'
      : item.status === 'idle' && !live
        ? 'idle'
        : 'completed';
  const branch = live?.git?.branch || item.branch;

  return (
    <div
      data-testid="subthread-card"
      className="rounded-xl border bg-card/60 p-3 transition-colors hover:border-foreground/20"
    >
      <div className="flex flex-wrap items-center gap-2">
        <HarnessIcon
          harness={harnessId}
          iconUrl={harnessMeta?.iconUrl}
          className="size-4 shrink-0"
        />
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-foreground">
          {title}
        </span>
        {status === 'running' ? (
          <span className="flex shrink-0 items-center gap-1 rounded-full bg-sky-500/15 px-2 py-0.5 text-xs font-medium text-sky-600 dark:text-sky-400">
            <Loader2 className="size-3 animate-spin" aria-hidden />
            Working
          </span>
        ) : status === 'failed' ? (
          <span className="flex shrink-0 items-center gap-1 rounded-full bg-destructive/15 px-2 py-0.5 text-xs font-medium text-destructive">
            <CircleAlert className="size-3" aria-hidden />
            Failed
          </span>
        ) : status === 'completed' ? (
          <span className="flex shrink-0 items-center gap-1 rounded-full bg-emerald-500/15 px-2 py-0.5 text-xs font-medium text-emerald-600 dark:text-emerald-400">
            <Check className="size-3" aria-hidden />
            Done
          </span>
        ) : null}
        <Link
          to="/a/$agentId"
          params={{ agentId: item.agentId }}
          search={{ s: item.sessionId }}
          title={
            onOpenSubthread
              ? 'Open beside this thread (⌘-click: open in the main view)'
              : undefined
          }
          {...threadLinkHandlers(
            item.sessionId,
            onOpenSubthread,
            onOpenSubthreadInMain,
          )}
          className="inline-flex shrink-0 items-center gap-1 rounded-lg border bg-background px-2.5 py-1 text-xs font-medium text-foreground shadow-xs transition-colors hover:bg-accent"
        >
          {status === 'running' ? 'Watch live' : 'Jump in'}
          <ArrowUpRight className="size-3.5" aria-hidden />
        </Link>
      </div>
      <div className="mt-1.5 flex flex-wrap items-center gap-x-2.5 gap-y-1 text-xs text-muted-foreground">
        {harnessName && <span>{harnessName} sub-thread</span>}
        {item.model && <span className="font-mono">{item.model}</span>}
        {branch ? (
          <span className="flex items-center gap-1 font-mono">
            <GitBranch className="size-3" aria-hidden />
            {branch}
          </span>
        ) : (
          <span>Shared worktree</span>
        )}
      </div>
      {item.summary && status !== 'running' && (
        <p className="mt-2 line-clamp-2 text-xs leading-relaxed text-muted-foreground">
          {item.summary}
        </p>
      )}
    </div>
  );
}

function Item({
  item,
  sessionId,
  canUse,
  running,
  hasWorktree,
  onRewound,
  myName,
  subthreads,
  onOpenFile,
  ...openers
}: {
  item: ChatItem;
  sessionId: string;
  canUse: boolean;
  running: boolean;
  hasWorktree: boolean;
  onRewound: (promptText: string) => void;
  myName?: string;
  subthreads?: Session[];
  onOpenFile?: OpenFile;
} & ThreadOpeners) {
  switch (item.kind) {
    case 'subthread':
      return (
        <SubthreadCard
          item={item}
          live={subthreads?.find((s) => s.id === item.sessionId)}
          {...openers}
        />
      );
    case 'user':
      return (
        <div className="group/user-msg ml-auto flex max-w-[85%] flex-col items-end gap-1">
          {item.user && item.user !== myName && (
            <span className="px-1 text-xs text-muted-foreground">
              {item.user}
            </span>
          )}
          {item.images?.length ? <MessageImages images={item.images} /> : null}
          {item.text && (
            <p className="rounded-2xl rounded-br-md bg-secondary px-4 py-2.5 text-[15px] leading-6 whitespace-pre-wrap text-secondary-foreground">
              {item.text}
            </p>
          )}
          {canUse && !running && item.seq > 0 && (
            <RewindAction
              sessionId={sessionId}
              seq={item.seq}
              fallbackText={item.text}
              hasWorktree={hasWorktree}
              onRewound={onRewound}
            />
          )}
        </div>
      );
    case 'agent':
      return (
        <div data-assistant-message="true" className="text-[15px] leading-7">
          <Suspense
            fallback={<p className="whitespace-pre-wrap">{item.text}</p>}
          >
            <Markdown text={item.text} onOpenFile={onOpenFile} />
          </Suspense>
        </div>
      );
    case 'thought':
      return <Thought text={item.text} />;
    case 'tool':
      return <ToolCard tool={item} />;
    case 'permission':
      return <Permission item={item} sessionId={sessionId} canUse={canUse} />;
    case 'turn_end':
      return item.stopReason === 'end_turn' || !item.stopReason ? null : (
        <p className="text-xs text-muted-foreground">
          Turn ended: {item.stopReason}
        </p>
      );
    case 'error':
      return (
        <p
          role="alert"
          className="flex items-start gap-2 rounded-xl border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive"
        >
          <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          {item.message}
        </p>
      );
    case 'notice':
      return (
        <p className="flex items-center gap-3 text-xs text-muted-foreground before:h-px before:flex-1 before:bg-border after:h-px after:flex-1 after:bg-border">
          {item.message}
        </p>
      );
  }
}

function Presence({ viewers }: { viewers: Viewer[] }) {
  const me = useQuery(meQuery);
  const myId = me.data?.user.id;
  if (viewers.length === 0) return null;
  return (
    <ul aria-label="Viewers" className="flex items-center -space-x-1.5">
      {viewers.map((v) => {
        const label = v.id === myId ? `${v.name} (you)` : v.name;
        return (
          <li
            key={v.id}
            title={label}
            aria-label={label}
            className="rounded-full ring-2 ring-background"
          >
            <Avatar name={v.name} url={v.avatarUrl} size="sm" />
          </li>
        );
      })}
    </ul>
  );
}

/** "Working 12s", counted from when this component mounted. */
function WorkingStatus({ label }: { label: string }) {
  const [start] = useState(() => Date.now());
  const now = useNow(1000);
  return (
    <p
      role="status"
      className="flex items-center gap-2 text-sm text-muted-foreground"
    >
      <Loader2 className="size-3.5 animate-spin text-sky-400" aria-hidden />
      {label}
      <span className="tabular-nums text-muted-foreground/70">
        {elapsed(now - start)}
      </span>
    </p>
  );
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

function UsagePill({ usage }: { usage: ContextUsage | null }) {
  if (!usage || (usage.used <= 0 && usage.size <= 0)) return null;
  const pct =
    usage.size > 0
      ? Math.min(100, Math.round((usage.used / usage.size) * 100))
      : null;
  const label =
    usage.size > 0
      ? `${formatTokens(usage.used)} / ${formatTokens(usage.size)}`
      : `${formatTokens(usage.used)} tokens`;
  return (
    <span
      title={
        pct !== null
          ? `Context window: ${usage.used.toLocaleString()} / ${usage.size.toLocaleString()} tokens (${pct}%)`
          : `Tokens used: ${usage.used.toLocaleString()}`
      }
      className="shrink-0 tabular-nums text-xs text-muted-foreground"
    >
      {label}
    </span>
  );
}

function SessionComposer({
  sessionId,
  agentId,
  cwd,
  running,
  sleeping,
  offline,
  meta,
  usage,
  queuedMessages,
  onQueueMutated,
  sentHistory,
  onSent,
}: {
  sessionId: string;
  agentId?: string;
  cwd?: string;
  running: boolean;
  sleeping: boolean;
  offline: boolean;
  meta?: ReactNode;
  usage?: ContextUsage | null;
  queuedMessages: QueuedMessage[];
  onQueueMutated: () => void;
  sentHistory: string[];
  onSent: () => void;
}) {
  const qc = useQueryClient();
  const [text, setText] = useDraft(sessionId);
  const att = useAttachments();
  const ctx = useComposerContext(sessionId);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState<Error | null>(null);
  const [sideChat, setSideChat] = useState<Session | null>(null);
  const [commandNote, setCommandNote] = useState<string | null>(null);
  const path = { params: { path: { sessionId } } };

  const invalidateQueue = () => {
    onQueueMutated();
    return qc.invalidateQueries({
      queryKey: sessionQueueQuery(sessionId).queryKey,
    });
  };

  const send = useMutation({
    mutationFn: (v: { text: string; images: Attachment[] }) =>
      unwrap(
        api.POST('/sessions/{sessionId}/prompt', {
          ...path,
          body: {
            text: v.text,
            ...(v.images.length && { images: toImagePayload(v.images) }),
          },
        }),
      ),
    onSuccess: () => {
      setText('');
      att.clear();
      ctx.clear();
      onSent();
    },
  });

  const enqueue = useMutation({
    mutationFn: (v: { text: string; images: Attachment[]; steer: boolean }) =>
      unwrap(
        api.POST('/sessions/{sessionId}/queue', {
          ...path,
          body: {
            text: v.text,
            steer: v.steer,
            ...(v.images.length && { images: toImagePayload(v.images) }),
          },
        }),
      ),
    onSuccess: () => {
      setText('');
      att.clear();
      ctx.clear();
      void invalidateQueue();
      onSent();
    },
  });

  const sendQueuedNow = useMutation({
    mutationFn: (messageId: string) =>
      unwrap(
        api.POST('/sessions/{sessionId}/queue/{messageId}/send-now', {
          params: { path: { sessionId, messageId } },
        }),
      ),
    onSuccess: () => void invalidateQueue(),
  });

  const deleteQueued = useMutation({
    mutationFn: (messageId: string) =>
      unwrap(
        api.DELETE('/sessions/{sessionId}/queue/{messageId}', {
          params: { path: { sessionId, messageId } },
        }),
      ),
    onSuccess: () => void invalidateQueue(),
  });

  const startSide = useMutation({
    mutationFn: (question: string) =>
      unwrap(
        api.POST('/sessions/{sessionId}/side-chats', {
          ...path,
          body: {
            ...(question && {
              text: question,
              title: question.slice(0, 60),
            }),
          },
        }),
      ),
    onSuccess: (created) => {
      setText('');
      att.clear();
      ctx.clear();
      setSideChat(created);
      if (agentId) {
        void qc.invalidateQueries({
          queryKey: ['agents', agentId, 'sessions'],
        });
      }
    },
  });

  const setPlanMode = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/sessions/{sessionId}/settings', {
          ...path,
          body: { permissionMode: 'plan' },
        }),
      ),
    onSuccess: () => {
      setCommandNote('Switched permission mode to Plan');
      if (agentId) {
        void qc.invalidateQueries({
          queryKey: ['agents', agentId, 'sessions'],
        });
      }
    },
  });

  const cancel = useMutation({
    mutationFn: () => unwrap(api.POST('/sessions/{sessionId}/cancel', path)),
  });

  const openModelPicker = () => {
    document
      .querySelector<HTMLButtonElement>('[data-model-picker-trigger="true"]')
      ?.click();
  };

  const handleSlashMenuPick = (command: string) => {
    setCommandNote(null);
    switch (command) {
      case 'clear':
        setText('');
        att.clear();
        ctx.clear();
        setUploadError(null);
        return;
      case 'model':
        openModelPicker();
        return;
      case 'plan':
        setPlanMode.mutate();
        return;
      case 'compact': {
        const promptText =
          'Please summarize and compact the conversation context so we can continue cleanly.';
        if (running) {
          enqueue.mutate({ text: promptText, images: [], steer: false });
        } else {
          send.mutate({ text: promptText, images: [] });
        }
        return;
      }
    }
  };

  const handleEditQueued = (msg: QueuedMessage) => {
    if (text.trim()) pushStash(text, sessionId);
    setText(msg.text);
    deleteQueued.mutate(msg.id);
  };

  const handleEditLatestQueued = () => {
    const latest = queuedMessages[queuedMessages.length - 1];
    if (latest) handleEditQueued(latest);
  };

  const handleInlineLargePaste = (f: FileAttachment) => {
    if (!f.pasteText) return;
    setText(text ? `${text}\n${f.pasteText}` : f.pasteText);
    att.removeFile(f.id);
  };

  const handleSubmit = async (opts?: ComposerSubmitOptions) => {
    setUploadError(null);
    setCommandNote(null);
    const trimmed = text.trim();
    const slash = parseSlashCommand(trimmed);
    if (slash) {
      if (slash.command === 'clear') {
        handleSlashMenuPick('clear');
        return;
      }
      if (slash.command === 'model') {
        setText('');
        openModelPicker();
        return;
      }
      if (slash.command === 'plan') {
        setText('');
        setPlanMode.mutate();
        return;
      }
      if (slash.command === 'side' || slash.command === 'btw') {
        startSide.mutate(slash.question);
        return;
      }
    }

    let promptBody = trimmed;
    if (slash?.command === 'compact') {
      promptBody = slash.note
        ? `Please summarize and compact the conversation context. Focus on: ${slash.note}`
        : 'Please summarize and compact the conversation context so we can continue cleanly.';
    } else if (slash?.command === 'goal' && slash.criteria) {
      promptBody = `Goal: ${slash.criteria}\n\nWork thoroughly and verify all completion criteria before finishing.`;
    }

    let uploadedChips: ComposerContextChip[] = [];
    if (att.files.length > 0 && agentId) {
      setUploading(true);
      try {
        const uploaded = await uploadFileAttachments(agentId, att.files);
        uploadedChips = uploaded.map((u) => ({
          id: `uploaded:${u.id}`,
          kind: 'file',
          path: u.path,
          label: u.name,
          size: u.size,
        }));
      } catch (e) {
        setUploadError(
          e instanceof Error ? e : new Error('Could not upload attachment'),
        );
        return;
      } finally {
        setUploading(false);
      }
    }

    const finalText = serializeComposerContext(
      [...ctx.chips, ...uploadedChips],
      promptBody,
    );
    if (!finalText && att.items.length === 0) return;

    const mode = opts?.mode ?? (running ? 'queue' : 'send');
    if (running || mode === 'queue' || mode === 'steer') {
      enqueue.mutate({
        text: finalText,
        images: att.items,
        steer: mode === 'steer',
      });
    } else {
      send.mutate({ text: finalText, images: att.items });
    }
  };

  const err =
    uploadError ??
    send.error ??
    enqueue.error ??
    sendQueuedNow.error ??
    deleteQueued.error ??
    startSide.error ??
    setPlanMode.error ??
    cancel.error;

  return (
    <div className="grid gap-2">
      <ErrorText error={err} />
      {commandNote && (
        <div
          role="status"
          className="flex items-center justify-between gap-2 rounded-xl border bg-card/80 px-3 py-1.5 text-xs text-muted-foreground"
        >
          <span>{commandNote}</span>
          <button
            type="button"
            aria-label="Dismiss notice"
            onClick={() => setCommandNote(null)}
            className="rounded p-0.5 hover:text-foreground"
          >
            <X className="size-3.5" />
          </button>
        </div>
      )}
      {sideChat && (
        <div
          role="status"
          data-testid="side-chat-banner"
          className="flex items-center justify-between gap-2 rounded-xl border bg-card/80 px-3 py-1.5 text-xs"
        >
          <span className="flex min-w-0 items-center gap-1.5 text-foreground">
            <MessageSquare
              className="size-3.5 shrink-0 text-sky-500"
              aria-hidden
            />
            <span className="truncate">
              Side chat started: {sideChat.title || 'Side chat'}
            </span>
          </span>
          <div className="flex shrink-0 items-center gap-1.5">
            {sideChat.agentId && (
              <Link
                to="/a/$agentId"
                params={{ agentId: sideChat.agentId }}
                search={{ s: sideChat.id }}
                className="inline-flex items-center gap-1 rounded-md border bg-background px-2 py-0.5 font-medium text-foreground hover:bg-accent"
              >
                Open side chat
                <ArrowUpRight className="size-3" aria-hidden />
              </Link>
            )}
            <button
              type="button"
              aria-label="Dismiss side chat notice"
              onClick={() => setSideChat(null)}
              className="rounded p-0.5 text-muted-foreground hover:text-foreground"
            >
              <X className="size-3.5" />
            </button>
          </div>
        </div>
      )}
      <QueuedMessages
        messages={queuedMessages}
        busy={sendQueuedNow.isPending || deleteQueued.isPending}
        onSendNow={(id) => sendQueuedNow.mutate(id)}
        onEdit={handleEditQueued}
        onRemove={(id) => deleteQueued.mutate(id)}
      />
      <Composer
        value={text}
        onChange={setText}
        running={running}
        busy={
          uploading ||
          send.isPending ||
          enqueue.isPending ||
          startSide.isPending ||
          cancel.isPending
        }
        disabled={offline}
        agentId={agentId}
        cwd={cwd}
        sessionId={sessionId}
        attachments={att.items}
        fileAttachments={att.files}
        contextChips={ctx.chips}
        attachError={att.error}
        onAttach={att.add}
        onRemoveAttachment={att.remove}
        onRemoveFileAttachment={att.removeFile}
        onLargePaste={(pasted) => void att.addLargePaste(pasted)}
        onInlineLargePaste={handleInlineLargePaste}
        onAddContextChip={ctx.add}
        onRemoveContextChip={ctx.remove}
        onSlashCommand={handleSlashMenuPick}
        onEditLatestQueued={
          queuedMessages.length > 0 ? handleEditLatestQueued : undefined
        }
        sentHistory={sentHistory}
        onSubmit={(opts) => void handleSubmit(opts)}
        onStop={() => cancel.mutate()}
        meta={
          meta || usage ? (
            <div className="flex min-w-0 items-center gap-2">
              {meta}
              <UsagePill usage={usage ?? null} />
            </div>
          ) : undefined
        }
        placeholder={
          sleeping
            ? 'Send a message to wake the agent…'
            : DEFAULT_COMPOSER_PLACEHOLDER
        }
      />
    </div>
  );
}

export default function ChatView({
  sessionId,
  agentId,
  session,
  subthreads,
  onOpenActivity,
  canUse,
  initialRunning = false,
  agentState,
  composerMeta,
  composerFooter,
  notice,
  compact,
  onOpenFile,
  onOpenSubthread,
  onOpenSubthreadInMain,
}: {
  sessionId: string;
  agentId?: string;
  /** Session metadata (used for worktree rewind option and cwd file search). */
  session?: Session;
  /** Child sub-threads of this session, for live running state and titles. */
  subthreads?: Session[];
  /** Opens the agent's Activity tab: offered while the agent wakes or starts, and after an error. */
  onOpenActivity?: () => void;
  canUse: boolean;
  agentState?: SandboxState;
  initialRunning?: boolean;
  /** Harness/model line shown inside the composer. */
  composerMeta?: ReactNode;
  /** A quiet line under the composer, e.g. the thread's git branch. */
  composerFooter?: ReactNode;
  /** Agent state banner shown right above the composer. */
  notice?: ReactNode;
  /** Narrow layout, e.g. in the side panel. */
  compact?: boolean;
  /** Makes file links in replies open in the file viewer. */
  onOpenFile?: OpenFile;
} & ThreadOpeners) {
  const qc = useQueryClient();
  const me = useQuery(meQuery);
  const [liveQueue, setLiveQueue] = useState<{
    sessionId: string;
    messages: QueuedMessage[];
  } | null>(null);
  const handleQueueUpdated = useCallback(
    (messages: QueuedMessage[]) => {
      setLiveQueue({ sessionId, messages });
      void qc.cancelQueries({
        queryKey: sessionQueueQuery(sessionId).queryKey,
      });
      qc.setQueryData(sessionQueueQuery(sessionId).queryKey, messages);
    },
    [qc, sessionId],
  );
  const { state, running, connected, viewers, reload } = useSessionStream(
    sessionId,
    initialRunning,
    handleQueueUpdated,
  );
  const queueQ = useQuery({
    ...sessionQueueQuery(sessionId),
    enabled: canUse,
  });
  const queuedMessages =
    liveQueue?.sessionId === sessionId
      ? liveQueue.messages
      : (queueQ.data ?? []);
  const subthreadCount = state.items.filter(
    (i) => i.kind === 'subthread',
  ).length;
  useEffect(() => {
    if (agentId && subthreadCount > 0) {
      void qc.invalidateQueries({ queryKey: ['agents', agentId, 'sessions'] });
    }
  }, [agentId, subthreadCount, qc]);
  const containerRef = useRef<HTMLDivElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const [atBottom, setAtBottom] = useState(true);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [state, running]);

  const toBottom = () => {
    const el = scroller.current;
    stick.current = true;
    setAtBottom(true);
    el?.scrollTo({ top: el.scrollHeight, behavior: 'smooth' });
  };

  const hasWorktree =
    Boolean(session?.project && !session.project.direct) ||
    Boolean(session?.git?.branch);

  const handleRewound = useCallback(
    (promptText: string) => {
      setDraft(sessionId, promptText);
      reload();
      if (agentId) {
        void qc.invalidateQueries({
          queryKey: ['agents', agentId, 'sessions'],
        });
      }
    },
    [sessionId, reload, agentId, qc],
  );

  const sentHistory = useMemo(
    () =>
      state.items
        .filter((i) => i.kind === 'user' && i.text.trim().length > 0)
        .map((i) => (i as Extract<ChatItem, { kind: 'user' }>).text),
    [state.items],
  );

  const groups = groupItems(state.items);
  const last = state.items[state.items.length - 1];
  const awaitingPermission =
    last?.kind === 'permission' && !last.resolved && running;

  return (
    <div ref={containerRef} className="relative flex min-h-0 flex-1 flex-col">
      <QuoteSelectionPopover
        sessionId={sessionId}
        containerRef={containerRef}
        enabled={canUse}
      />
      <div
        className={cn(
          'pointer-events-none absolute top-2 right-4 z-10 hidden md:block',
          compact && 'md:hidden',
        )}
      >
        <div className="pointer-events-auto">
          <Presence viewers={viewers} />
        </div>
      </div>
      <div
        ref={scroller}
        data-testid="chat-scroll"
        onScroll={(e) => {
          const el = e.currentTarget;
          const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
          stick.current = bottom;
          if (bottom !== atBottom) setAtBottom(bottom);
        }}
        className="min-h-0 flex-1 overflow-y-auto"
      >
        <div
          className={cn(
            'mx-auto flex w-full max-w-3xl flex-col gap-5 px-4 pt-8 pb-10 md:px-8',
            compact && 'px-3 pt-4 pb-6 md:px-4',
          )}
        >
          {state.items.length === 0 && !running && (
            <p className="py-16 text-center text-sm text-muted-foreground">
              {connected ? 'No messages yet.' : 'Loading conversation…'}
            </p>
          )}
          {groups.map((g, i) =>
            g.kind === 'work' ? (
              <WorkGroup
                key={`w-${g.items[0].seq}-${i}`}
                items={g.items}
                live={running && i === groups.length - 1}
              />
            ) : (
              <Item
                key={`${g.item.kind}-${g.item.seq}-${i}`}
                item={g.item}
                sessionId={sessionId}
                canUse={canUse}
                running={running}
                hasWorktree={hasWorktree}
                onRewound={handleRewound}
                myName={me.data?.user.name}
                subthreads={subthreads}
                onOpenFile={onOpenFile}
                onOpenSubthread={onOpenSubthread}
                onOpenSubthreadInMain={onOpenSubthreadInMain}
              />
            ),
          )}
          {state.plan && state.plan.length > 0 && <Plan entries={state.plan} />}
          {!running && onOpenActivity && last?.kind === 'error' && (
            <ActivityLink onOpen={onOpenActivity} label="Show what happened" />
          )}
          {running && (
            <WorkingStatus
              label={
                agentState === 'sleeping' || agentState === 'starting'
                  ? 'Waking the agent…'
                  : awaitingPermission
                    ? 'Waiting for approval'
                    : 'Working'
              }
            />
          )}
        </div>
      </div>

      <div
        className={cn(
          'relative mx-auto w-full max-w-3xl shrink-0 px-3 pb-3 md:px-8 md:pb-5',
          compact && 'md:px-3 md:pb-3',
        )}
      >
        {!atBottom && (
          <button
            type="button"
            onClick={toBottom}
            aria-label="Scroll to latest"
            className="absolute -top-12 left-1/2 flex size-8 -translate-x-1/2 items-center justify-center rounded-full border bg-card text-muted-foreground shadow-md hover:text-foreground"
          >
            <ArrowDown className="size-4" />
          </button>
        )}
        {!connected && state.items.length > 0 && (
          <p className="mb-2 flex items-center justify-center gap-2 text-xs text-muted-foreground">
            <Loader2 className="size-3 animate-spin" aria-hidden />
            Reconnecting…
          </p>
        )}
        {notice}
        {canUse ? (
          <SessionComposer
            sessionId={sessionId}
            agentId={agentId}
            cwd={session?.cwd}
            running={running}
            sleeping={agentState === 'sleeping'}
            offline={agentState === 'unreachable'}
            meta={composerMeta}
            usage={state.usage}
            queuedMessages={queuedMessages}
            onQueueMutated={() => setLiveQueue(null)}
            sentHistory={sentHistory}
            onSent={toBottom}
          />
        ) : (
          <p className="rounded-2xl border border-dashed px-4 py-3 text-center text-sm text-muted-foreground">
            You have read-only access to this session.
          </p>
        )}
        {composerFooter && (
          <div className="mt-1.5 empty:hidden">{composerFooter}</div>
        )}
      </div>
    </div>
  );
}
