import { lazy, Suspense, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import {
  Bot,
  Check,
  FolderGit2,
  GitBranch,
  PanelRight,
  Settings2,
  Undo2,
} from 'lucide-react';
import { agentQuery, sessionsQuery, type Agent } from '@/api/queries';
import { AgentStateDot } from '@/components/AgentStateBadge';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { AgentLoadError } from '@/components/agent/AgentLoadError';
import { AgentNotice } from '@/components/agent/AgentNotice';
import {
  AgentPanel,
  panelTabNeedsUse,
  type PanelTab,
} from '@/components/agent/AgentPanel';
import {
  GitMeta,
  SessionGitLine,
  gitStartArgs,
  useGitDraft,
} from '@/components/agent/GitMeta';
import { HarnessMeta } from '@/components/agent/HarnessMeta';
import { agentDraftKey } from '@/lib/drafts';
import { DraftChat } from '@/components/chat/DraftChat';
import { Crumb, CrumbSep, TopBar } from '@/components/layout/TopBar';
import { readPref, writePref } from '@/components/layout/shell';
import { useSettle } from '@/hooks/useSettle';
import { useStartChat } from '@/hooks/useStartChat';
import { useDocumentTitle } from '@/hooks/useDocumentTitle';
import type { Choice } from '@/lib/models';

const ChatView = lazy(() => import('@/components/chat/ChatView'));

/** Owners and shared users use an agent; only owners configure it. */
export const accessOf = (a: Agent) => ({
  canUse: a.role === 'owner' || a.role === 'shared',
  isOwner: a.role === 'owner',
});

/** A thread with one agent (a session in the API), or a fresh draft when
 * none is open. */
export function AgentPage({
  agentId,
  sessionId,
  panel,
  cwd,
  onPanel,
}: {
  agentId: string;
  sessionId?: string;
  panel?: PanelTab;
  /** Preselects this directory (a repository) in the draft composer. */
  cwd?: string;
  onPanel: (p: PanelTab | undefined) => void;
}) {
  const q = useQuery(agentQuery(agentId));
  const sessions = useQuery(sessionsQuery(agentId));
  const [wide, setWideState] = useState(
    () => readPref('panelWide', '') === '1',
  );
  const start = useStartChat();
  const settle = useSettle();
  const [gitDraft, setGitDraft] = useGitDraft(cwd);
  const [draftModel, setDraftModel] = useState<Choice>({
    model: '',
    options: {},
  });
  // The tab shows the open chat's title, then the agent.
  const active = sessions.data?.find((s) => s.id === sessionId);
  useDocumentTitle(
    sessionId ? active?.title || 'New thread' : undefined,
    q.data?.name,
  );

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
  const selected = active;
  const notice = <AgentNotice agent={a} canUse={access.canUse} />;

  return (
    <div className="flex min-h-0 flex-1">
      <div className="flex min-w-0 flex-1 flex-col">
        <TopBar
          actions={
            <>
              {access.canUse && selected && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-muted-foreground"
                  disabled={settle.isPending}
                  title={
                    selected.settledAt ? 'Unsettle thread' : 'Settle thread'
                  }
                  onClick={() =>
                    settle.mutate({
                      session: selected,
                      settle: !selected.settledAt,
                    })
                  }
                >
                  {selected.settledAt ? (
                    <Undo2 className="size-3.5" aria-hidden />
                  ) : (
                    <Check className="size-3.5" aria-hidden />
                  )}
                  <span className="max-sm:sr-only">
                    {selected.settledAt ? 'Unsettle' : 'Settle'}
                  </span>
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
                  {selected.title || 'New thread'}
                </span>
                {selected.settledAt && (
                  <span className="shrink-0 rounded-full border px-1.5 text-[11px] font-normal text-muted-foreground">
                    Settled
                  </span>
                )}
              </Crumb>
              {(selected.git?.project || selected.git?.branch) && (
                <span
                  data-testid="topbar-git"
                  title={[selected.git.project, selected.git.branch]
                    .filter(Boolean)
                    .join(' · ')}
                  className="ml-2 hidden min-w-0 shrink items-center gap-1 rounded-md border px-1.5 py-0.5 text-xs text-muted-foreground md:flex"
                >
                  <FolderGit2 className="size-3 shrink-0" aria-hidden />
                  <span className="max-w-28 truncate">
                    {selected.git.project}
                  </span>
                  {selected.git.branch && (
                    <>
                      <GitBranch className="size-3 shrink-0" aria-hidden />
                      <span className="max-w-28 truncate font-mono">
                        {selected.git.branch}
                      </span>
                    </>
                  )}
                </span>
              )}
            </>
          )}
        </TopBar>

        <ErrorText error={settle.error} />
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
              Open its thread
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
                <>
                  <HarnessMeta
                    agent={a}
                    session={selected}
                    canUse={access.canUse}
                  />
                </>
              }
              composerFooter={
                selected && (
                  <SessionGitLine
                    agent={a}
                    session={selected}
                    canUse={access.canUse}
                  />
                )
              }
              notice={notice}
            />
          </Suspense>
        ) : (
          <DraftChat
            draftKey={agentDraftKey(a.id)}
            heading={
              <>
                Start a thread with{' '}
                <span className="text-muted-foreground">{a.name}</span>
              </>
            }
            sub={
              sessions.data && sessions.data.length > 0
                ? `${sessions.data.length} earlier ${sessions.data.length === 1 ? 'thread' : 'threads'} in the sidebar and the agent panel.`
                : 'Each thread is its own conversation with the agent; files persist across them.'
            }
            meta={
              <>
                <HarnessMeta
                  agent={a}
                  draft={draftModel}
                  onDraft={setDraftModel}
                  canUse={access.canUse}
                />
                <GitMeta
                  agent={a}
                  draft={gitDraft}
                  onDraft={setGitDraft}
                  canUse={access.canUse}
                />
              </>
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
            onSend={(text, images) =>
              start.mutate({
                agentId,
                text,
                images,
                model: draftModel,
                ...gitStartArgs(gitDraft),
              })
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
