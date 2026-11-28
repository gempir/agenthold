import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { CheckCircle2, ExternalLink, Plug } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  executorOverviewQuery,
  orgToolsQuery,
  type ExecutorAuthMethod,
  type ExecutorConnectionSession,
} from '@/api/queries';
import { ErrorText } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';

/** The methods a connection session offers, with the browser sign-in
 * (OAuth) preferred when the session carries a URL for it. */
function sessionMethods(s: ExecutorConnectionSession): ExecutorAuthMethod[] {
  return s.methods ?? [];
}

function preferredMethod(s: ExecutorConnectionSession): string {
  const methods = sessionMethods(s);
  const oauth = methods.find((m) => m.kind !== 'secrets');
  if (oauth && s.browserUrl) return oauth.name;
  return (methods.find((m) => m.kind === 'secrets') ?? methods[0])?.name ?? '';
}

/**
 * Finishes an Executor connection session inline: a token form for
 * API-key/secret methods, or a sign-in link for browser (OAuth) methods.
 */
export function ConnectSessionForm({
  session,
  name,
  onDone,
  onCancel,
}: {
  session: ExecutorConnectionSession;
  name: string;
  onDone: () => void;
  onCancel?: () => void;
}) {
  const methods = sessionMethods(session);
  const [selected, setSelected] = useState(() => preferredMethod(session));
  const [fields, setFields] = useState<Record<string, string>>({});
  const active = methods.find((m) => m.name === selected) ?? methods[0];
  const submit = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/me/executor/connections/submit', {
          body: {
            connectionId: session.connectionId,
            token: session.token,
            method: active?.name ?? '',
            fields,
          },
        }),
      ),
    onSuccess: onDone,
  });
  return (
    <div className="grid gap-3 text-sm" aria-label={`Connect ${name}`}>
      {methods.length > 1 && (
        <label className="grid gap-1">
          Sign in with
          <Select
            aria-label="Authentication method"
            value={active?.name ?? ''}
            onChange={(e) => {
              setSelected(e.target.value);
              setFields({});
            }}
          >
            {methods.map((m) => (
              <option key={m.name} value={m.name}>
                {m.label || m.name}
              </option>
            ))}
          </Select>
        </label>
      )}
      {active?.kind === 'secrets' ? (
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            submit.mutate();
          }}
        >
          {active.guide && (
            <p className="rounded-md bg-muted/40 p-2.5 text-xs text-muted-foreground">
              {active.guide}
            </p>
          )}
          {(active.fields ?? []).map((f) => (
            <label key={f.name} className="grid gap-1">
              <span>
                {f.title || f.name}
                {f.required ? ' *' : ''}
              </span>
              <Input
                type={f.secret === false ? 'text' : 'password'}
                value={fields[f.name] ?? ''}
                onChange={(e) =>
                  setFields((prev) => ({ ...prev, [f.name]: e.target.value }))
                }
                required={f.required}
                autoComplete="off"
              />
              {f.description && (
                <span className="text-xs text-muted-foreground">
                  {f.description}
                </span>
              )}
            </label>
          ))}
          <ErrorText error={submit.error} />
          <div className="flex flex-wrap gap-2">
            <Button type="submit" size="sm" disabled={submit.isPending}>
              Save connection
            </Button>
            {onCancel && (
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={onCancel}
              >
                Cancel
              </Button>
            )}
          </div>
        </form>
      ) : session.browserUrl ? (
        <div className="grid gap-2">
          <p className="text-muted-foreground">
            Finish signing in to {name} in the window that opened, then come
            back here.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button asChild size="sm" variant="outline">
              <a href={session.browserUrl} target="_blank" rel="noreferrer">
                Open sign-in
                <ExternalLink className="size-3.5" aria-hidden />
              </a>
            </Button>
            <Button size="sm" onClick={onDone}>
              I finished connecting
            </Button>
          </div>
        </div>
      ) : (
        <p className="text-muted-foreground">
          {name} needs a browser sign-in, which this server can't offer yet. Ask
          an administrator to enable HTTPS for integrations.
        </p>
      )}
    </div>
  );
}

/**
 * One-click Connect for a user_oauth organization tool: installs the app if
 * needed, then opens the sign-in (or an inline token form) right away.
 */
export function OrgToolConnectButton({
  toolId,
  name,
  connected,
  onConnected,
}: {
  toolId: string;
  name: string;
  connected: boolean;
  onConnected?: () => void;
}) {
  const qc = useQueryClient();
  const [session, setSession] = useState<ExecutorConnectionSession | null>(
    null,
  );
  const [done, setDone] = useState(false);
  const refresh = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: orgToolsQuery.queryKey }),
      qc.invalidateQueries({ queryKey: executorOverviewQuery.queryKey }),
    ]);
  const finish = () => {
    setSession(null);
    setDone(true);
    void refresh();
    onConnected?.();
  };
  const connect = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/me/org-tools/{toolId}/connect', {
          params: { path: { toolId } },
        }),
      ),
    onSuccess: (res) => {
      if (!res.session) {
        if (res.connected) finish();
        else void refresh();
        return;
      }
      const s = res.session;
      const onlyBrowser =
        s.browserUrl && !sessionMethods(s).some((m) => m.kind === 'secrets');
      if (onlyBrowser) window.open(s.browserUrl, '_blank', 'noopener');
      setSession(s);
    },
  });

  if ((connected || done) && !session) {
    return (
      <Badge className="gap-1 bg-emerald-500/15 text-emerald-700 dark:text-emerald-400">
        <CheckCircle2 className="size-3" aria-hidden />
        Connected
      </Badge>
    );
  }
  if (session) {
    return (
      <div className="w-full rounded-md border bg-muted/20 p-3">
        <ConnectSessionForm
          session={session}
          name={name}
          onDone={finish}
          onCancel={() => setSession(null)}
        />
      </div>
    );
  }
  return (
    <div className="grid gap-1">
      <Button
        size="sm"
        disabled={connect.isPending}
        onClick={() => connect.mutate()}
      >
        <Plug className="size-3.5" aria-hidden />
        Connect {name}
      </Button>
      <ErrorText error={connect.error} />
    </div>
  );
}
