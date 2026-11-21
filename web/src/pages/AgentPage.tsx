import { lazy, Suspense, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Bot, PanelRight, Settings2, SquarePen } from 'lucide-react';
import { agentQuery, sessionsQuery, type Agent } from '@/api/queries';
import { AgentStateDot } from '@/components/AgentStateBadge';
import { Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { AgentLoadError } from '@/components/agent/AgentLoadError';
import { AgentNotice } from '@/components/agent/AgentNotice';
import {
  AgentPanel,
  panelTabNeedsUse,
  type PanelTab,
} from '@/components/agent/AgentPanel';
import { HarnessMeta } from '@/components/agent/HarnessMeta';
import { DraftChat } from '@/components/chat/DraftChat';
import { Crumb, CrumbSep, TopBar } from '@/components/layout/TopBar';
import { readPref, writePref } from '@/components/layout/shell';
import { useStartChat } from '@/hooks/useStartChat';
import type { Choice } from '@/lib/models';

const ChatView = lazy(() => import('@/components/chat/ChatView'));

/** Owners and shared users use an agent; only owners configure it. */
export const accessOf = (a: Agent) => ({
  canUse: a.role === 'owner' || a.role === 'shared',
  isOwner: a.role === 'owner',
});

/** The chat with one agent: a session, or a fresh draft when none is open. */
export function AgentPage({
  agentId,
  sessionId,
  panel,
  onPanel,
}: {
  agentId: string;
  sessionId?: string;
  panel?: PanelTab;
  onPanel: (p: PanelTab | undefined) => void;
}) {
  const q = useQuery(agentQuery(agentId));
  const sessions = useQuery(sessionsQuery(agentId));
  const [wide, setWideState] = useState(
    () => readPref('panelWide', '') === '1',
  );
  const start = useStartChat();
  const [draftModel, setDraftModel] = useState<Choice>({
    model: '',
    options: {},
  });

  if (q.isPending)
    return (
      <>
        <TopBar />
        <Loading />
      </>
    );
  if (q.error)
    return (
      <>
        <TopBar />
        <AgentLoadError error={q.error} />
      </>
    );

  const a = q.data;
  const access = accessOf(a);
  const selected = sessions.data?.find((s) => s.id === sessionId);
  const notice = <AgentNotice agent={a} canUse={access.canUse} />;

  return (
    <div className="flex min-h-0 flex-1">
      <div className="flex min-w-0 flex-1 flex-col">
        <TopBar
          actions={
            <>
              {access.canUse && sessionId && (
                <Button
                  asChild
                  variant="ghost"
                  size="icon-sm"
                  className="text-muted-foreground"
                >
                  <Link
                    to="/a/$agentId"
                    params={{ agentId }}
                    search={{ panel }}
                    aria-label="New chat"
                    title="New chat"
                  >
                    <SquarePen className="size-4" />
                  </Link>
                </Button>
              )}
              <Button
                variant="ghost"
                size="icon-sm"
                className={
                  panel ? 'bg-accent text-foreground' : 'text-muted-foreground'
                }
                aria-label="Agent panel"
                aria-pressed={!!panel}
                title="Agent panel"
                onClick={() => onPanel(panel ? undefined : 'overview')}
              >
                <PanelRight className="size-4" />
              </Button>
              <Button
                asChild
                variant="ghost"
                size="icon-sm"
                className="text-muted-foreground"
              >
                <Link
                  to="/a/$agentId/settings"
                  params={{ agentId }}
                  aria-label="Agent settings"
                  title="Agent settings"
                >
                  <Settings2 className="size-4" />
                </Link>
              </Button>
            </>
          }
        >
          <Crumb current={!selected} className="max-w-[45%] shrink-0">
            <AgentStateDot state={a.state} />
            <Link
              to="/a/$agentId"
              params={{ agentId }}
              className="truncate hover:underline"
            >
              {a.name}
            </Link>
          </Crumb>
          {selected && (
            <>
              <CrumbSep />
              <Crumb current className="min-w-0">
                <span className="truncate">
                  {selected.title || 'New session'}
                </span>
              </Crumb>
            </>
          )}
        </TopBar>

        {selected?.origin && (
          <p
            role="note"
            className="mx-auto mt-1 flex max-w-3xl items-center gap-2 rounded-full border px-3 py-1 text-xs text-muted-foreground"
          >
            <Bot className="size-3.5" aria-hidden />
            Started by agent{' '}
            <strong className="font-medium text-foreground">
              {selected.origin.agentName}
            </strong>
            <Link
              to="/a/$agentId"
              params={{ agentId: selected.origin.agentId }}
              search={{ s: selected.origin.sessionId }}
              className="underline underline-offset-2 hover:text-foreground"
            >
              Open its session
            </Link>
          </p>
        )}

        {sessionId ? (
          <Suspense fallback={<Loading />}>
            <ChatView
              key={sessionId}
              sessionId={sessionId}
              onOpenActivity={
                access.canUse ? () => onPanel('activity') : undefined
              }
              canUse={access.canUse}
              agentState={a.state}
              initialRunning={selected?.running}
              composerMeta={
                <HarnessMeta
                  agent={a}
                  session={selected}
                  canUse={access.canUse}
                />
              }
              notice={notice}
            />
          </Suspense>
        ) : (
          <DraftChat
            heading={
              <>
                Start a chat with{' '}
                <span className="text-muted-foreground">{a.name}</span>
              </>
            }
            sub={
              sessions.data && sessions.data.length > 0
                ? `${sessions.data.length} earlier ${sessions.data.length === 1 ? 'chat' : 'chats'} in the sidebar and the agent panel.`
                : 'Each chat is its own session with the agent; files persist across them.'
            }
            meta={
              <HarnessMeta
                agent={a}
                draft={draftModel}
                onDraft={setDraftModel}
                canUse={access.canUse}
              />
            }
            notice={notice}
            readOnly={!access.canUse}
            disabled={a.state === 'unreachable'}
            placeholder={
              a.state === 'sleeping'
                ? 'Send a message to wake the agent…'
                : `Message ${a.name}`
            }
            pending={start.isPending}
            error={start.error}
            onSend={(text) =>
              start.mutate({ agentId, text, model: draftModel })
            }
          />
        )}
      </div>
      {panel && (
        <AgentPanel
          agent={a}
          access={access}
          tab={panelTabNeedsUse(panel) && !access.canUse ? 'overview' : panel}
          sessionId={sessionId}
          wide={wide}
          onTab={onPanel}
          onWide={(w) => {
            setWideState(w);
            writePref('panelWide', w ? '1' : '');
          }}
          onClose={() => onPanel(undefined)}
        />
      )}
    </div>
  );
}
