import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Building2, CheckCircle2, Plug } from 'lucide-react';
import { orgToolsQuery } from '@/api/queries';
import { OrgToolConnectButton } from '@/components/OrgToolConnect';
import type { AccountConnectItem } from '@/lib/session';

/**
 * An agent's request_account_connect call, inline in the thread: one click
 * connects the organization tool without leaving the conversation.
 */
export function AccountConnectCard({
  item,
  canUse,
}: {
  item: AccountConnectItem;
  canUse: boolean;
}) {
  const [justConnected, setJustConnected] = useState(false);
  // Pick up connections made elsewhere (settings, another thread).
  const tools = useQuery({ ...orgToolsQuery, enabled: !item.connected });
  const live = tools.data?.find((t) => t.id === item.toolId);
  const connected = item.connected || justConnected || Boolean(live?.connected);
  const perUser = item.authMode === 'user_oauth';

  if (connected || !perUser) {
    return (
      <p
        role="status"
        className="flex items-center gap-2 py-1 text-sm text-muted-foreground"
      >
        {connected ? (
          <CheckCircle2
            className="size-3.5 shrink-0 text-emerald-500"
            aria-hidden
          />
        ) : (
          <Building2 className="size-3.5 shrink-0" aria-hidden />
        )}
        <span className="min-w-0 truncate">
          {connected
            ? `${item.name} connected`
            : `${item.name} is provided by your organization`}
        </span>
        {!connected && (
          <Link
            to="/settings/integrations"
            className="shrink-0 underline underline-offset-2"
          >
            Integrations
          </Link>
        )}
      </p>
    );
  }

  return (
    <div
      role="group"
      aria-label={`Connect ${item.name}`}
      className="grid gap-3 rounded-xl border border-sky-500/40 bg-sky-500/5 p-3 text-sm"
    >
      <div className="flex items-start gap-2">
        <Plug className="mt-0.5 size-4 shrink-0 text-sky-500" aria-hidden />
        <div className="grid min-w-0 gap-1">
          <p>
            The agent wants to use <strong>{item.name}</strong>
          </p>
          {item.reason && (
            <p className="text-muted-foreground">{item.reason}</p>
          )}
          {item.description && !item.reason && (
            <p className="text-muted-foreground">{item.description}</p>
          )}
        </div>
      </div>
      {canUse ? (
        <div className="flex flex-wrap items-center gap-2 pl-6">
          <OrgToolConnectButton
            toolId={item.toolId}
            name={item.name}
            connected={false}
            onConnected={() => setJustConnected(true)}
          />
          <span className="text-xs text-muted-foreground">
            Connect once; every thread can use it.
          </span>
        </div>
      ) : (
        <p className="pl-6 text-xs text-muted-foreground">
          Waiting for the thread owner to connect it.
        </p>
      )}
    </div>
  );
}
