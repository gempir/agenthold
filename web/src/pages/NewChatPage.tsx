import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Bot, ChevronDown, Plus } from 'lucide-react';
import { meQuery } from '@/api/queries';
import { AgentStateDot } from '@/components/AgentStateBadge';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Menu, MenuItem, MenuSeparator } from '@/components/ui/menu';
import { AgentNotice } from '@/components/agent/AgentNotice';
import {
  EMPTY_GIT_DRAFT,
  GitMeta,
  gitStartArgs,
  useGitDraft,
} from '@/components/agent/GitMeta';
import { HarnessMeta } from '@/components/agent/HarnessMeta';
import { DraftChat } from '@/components/chat/DraftChat';
import { Crumb, TopBar } from '@/components/layout/TopBar';
import { useShell } from '@/components/layout/shell';
import { useChatIndex } from '@/hooks/useChatIndex';
import { useStartChat } from '@/hooks/useStartChat';
import { accessOf } from '@/pages/AgentPage';

function greeting(name?: string) {
  const h = new Date().getHours();
  const part =
    h < 5 ? 'evening' : h < 12 ? 'morning' : h < 18 ? 'afternoon' : 'evening';
  const first = name?.split(/\s+/)[0];
  return `Good ${part}${first ? `, ${first}` : ''}`;
}

/** A blank conversation with an agent picker in the composer. */
export function NewChatPage() {
  const shell = useShell();
  const me = useQuery(meQuery);
  const index = useChatIndex();
  const start = useStartChat();
  const [picked, setPicked] = useState<string>();
  const [gitDraft, setGitDraft] = useGitDraft();

  const candidates = index.agents.filter((x) => accessOf(x.agent).canUse);
  const chosen = candidates.find((x) => x.agent.id === picked) ?? candidates[0];

  let body;
  if (index.loading) body = <Loading />;
  else if (index.agents.length === 0)
    body = (
      <div className="flex flex-1 flex-col items-center justify-center gap-4 px-6 text-center">
        <span className="flex size-12 items-center justify-center rounded-2xl border bg-card">
          <Bot className="size-6 text-muted-foreground" aria-hidden />
        </span>
        <div>
          <h1 className="text-2xl font-medium tracking-tight">
            {greeting(me.data?.user.name)}
          </h1>
          <p className="mt-2 max-w-sm text-sm text-muted-foreground">
            Agents are persistent assistants, each with its own computer. Create
            one to start chatting.
          </p>
        </div>
        <Button onClick={shell.openNewAgent}>
          <Plus className="size-4" />
          Create an agent
        </Button>
        <ErrorText error={index.error} />
      </div>
    );
  else if (!chosen)
    body = (
      <div className="flex flex-1 items-center justify-center p-6 text-sm text-muted-foreground">
        You can view these agents but not start threads with them.
      </div>
    );
  else
    body = (
      <DraftChat
        heading={greeting(me.data?.user.name)}
        sub="What should we work on?"
        notice={<AgentNotice agent={chosen.agent} canUse />}
        placeholder={`Message ${chosen.agent.name}`}
        pending={start.isPending}
        error={start.error}
        onSend={(text) =>
          start.mutate({
            agentId: chosen.agent.id,
            text,
            ...gitStartArgs(gitDraft),
          })
        }
        meta={
          <>
            <Menu
              trigger={({ open, toggle }) => (
                <button
                  type="button"
                  aria-label="Agent"
                  aria-haspopup="menu"
                  aria-expanded={open}
                  onClick={toggle}
                  className="flex items-center gap-1.5 rounded-md px-1.5 py-1 text-foreground hover:bg-accent"
                >
                  <AgentStateDot state={chosen.agent.state} />
                  <span className="max-w-40 truncate">{chosen.agent.name}</span>
                  <ChevronDown className="size-3.5 text-muted-foreground" />
                </button>
              )}
              className="bottom-full top-auto mb-1"
            >
              {(close) => (
                <>
                  {candidates.map(({ agent }) => (
                    <MenuItem
                      key={agent.id}
                      active={agent.id === chosen.agent.id}
                      onClick={() => {
                        setPicked(agent.id);
                        setGitDraft(EMPTY_GIT_DRAFT);
                        close();
                      }}
                    >
                      <AgentStateDot state={agent.state} />
                      <span className="min-w-0 flex-1 truncate">
                        {agent.name}
                      </span>
                      {agent.role === 'shared' && (
                        <span className="text-xs text-muted-foreground">
                          {agent.owner.name}
                        </span>
                      )}
                    </MenuItem>
                  ))}
                  <MenuSeparator />
                  <MenuItem
                    onClick={() => {
                      close();
                      shell.openNewAgent();
                    }}
                  >
                    <Plus className="size-4 text-muted-foreground" />
                    New agent
                  </MenuItem>
                </>
              )}
            </Menu>
            <HarnessMeta agent={chosen.agent} />
            <GitMeta
              agent={chosen.agent}
              draft={gitDraft}
              onDraft={setGitDraft}
            />
          </>
        }
      />
    );

  return (
    <>
      <TopBar>
        <Crumb current>New thread</Crumb>
      </TopBar>
      {body}
    </>
  );
}
