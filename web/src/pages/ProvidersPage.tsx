import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import { providerLoginsQuery, type ProviderLoginInfo } from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';

/** Harness logins kept on your account and used by your agents. */
export function ProvidersPage() {
  const logins = useQuery(providerLoginsQuery);
  return (
    <div className="flex max-w-3xl flex-col gap-6">
      <header>
        <h1 className="text-xl font-semibold">Provider logins</h1>
        <p className="text-sm text-muted-foreground">
          Sign in to Claude Code and Codex once. Every agent that uses your
          logins gets them when it starts, and switching an agent between
          harnesses needs no new sign-in. Agents only see placeholders: the
          egress proxy adds the real credential to their requests. Anyone who
          can prompt such an agent uses your login through it.
        </p>
      </header>
      {logins.isPending && <Loading />}
      <ErrorText error={logins.error} />
      {logins.data?.map((p) => (
        <Provider key={p.provider} info={p} />
      ))}
    </div>
  );
}

function Provider({ info }: { info: ProviderLoginInfo }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const login = info.login;
  const disconnect = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE('/me/provider-logins/{provider}', {
          params: { path: { provider: info.provider } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
  });
  return (
    <section
      aria-label={info.name}
      className="grid gap-3 rounded-lg border p-4"
    >
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-semibold">{info.name}</h2>
        {!login ? (
          <Badge variant="outline">not connected</Badge>
        ) : login.status === 'error' ? (
          <Badge className="bg-amber-500/15 text-amber-700">
            connect again
          </Badge>
        ) : (
          <Badge>connected</Badge>
        )}
        <div className="ml-auto flex gap-2">
          <Button size="sm" variant="outline" onClick={() => setOpen(!open)}>
            {login ? 'Reconnect' : 'Connect'}
          </Button>
          {login && (
            <Button
              size="sm"
              variant="ghost"
              disabled={disconnect.isPending}
              onClick={() => {
                if (
                  confirm(`Disconnect ${info.name}? Agents using it lose it.`)
                )
                  disconnect.mutate();
              }}
            >
              Disconnect
            </Button>
          )}
        </div>
      </div>
      {login && (
        <p className="text-sm text-muted-foreground">
          {login.label}
          {login.expiresAt &&
            ` · valid until ${new Date(login.expiresAt).toLocaleDateString()}`}
          {login.lastUsedAt
            ? ` · used ${new Date(login.lastUsedAt).toLocaleString()}`
            : ' · not used yet'}
          {login.statusMessage && ` · ${login.statusMessage}`}
        </p>
      )}
      <ErrorText error={disconnect.error} />
      {open && <ConnectForm info={info} onDone={() => setOpen(false)} />}
      <div className="text-sm">
        {info.agents.length === 0 ? (
          <span className="text-muted-foreground">
            No {info.name} agent uses your logins.
          </span>
        ) : (
          <>
            <span className="text-muted-foreground">Used by </span>
            {info.agents.map((a, i) => (
              <span key={a.id}>
                {i > 0 && ', '}
                <Link
                  to="/a/$agentId"
                  params={{ agentId: a.id }}
                  className="underline"
                >
                  {a.name}
                </Link>
              </span>
            ))}
          </>
        )}
      </div>
    </section>
  );
}

function ConnectForm({
  info,
  onDone,
}: {
  info: ProviderLoginInfo;
  onDone: () => void;
}) {
  const qc = useQueryClient();
  const [kindId, setKindId] = useState(info.kinds[0]?.id ?? '');
  const [credential, setCredential] = useState('');
  const kind = info.kinds.find((k) => k.id === kindId);
  const connect = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/me/provider-logins/{provider}', {
          params: { path: { provider: info.provider } },
          body: { kind: kindId, credential },
        }),
      ),
    onSuccess: async () => {
      setCredential('');
      onDone();
      await qc.invalidateQueries({ queryKey: ['me'] });
      await qc.invalidateQueries({ queryKey: ['agents'] });
    },
  });
  const field = `credential-${info.provider}`;
  return (
    <form
      className="grid gap-3 border-t pt-3"
      onSubmit={(e) => {
        e.preventDefault();
        connect.mutate();
      }}
    >
      <label className="grid gap-1 text-sm">
        Sign in with
        <Select value={kindId} onChange={(e) => setKindId(e.target.value)}>
          {info.kinds.map((k) => (
            <option key={k.id} value={k.id}>
              {k.name}
            </option>
          ))}
        </Select>
      </label>
      {kind && (
        <p className="text-sm text-muted-foreground">{kind.description}</p>
      )}
      {kind?.command && (
        <div className="grid gap-1 text-sm">
          Run on your computer:
          <code className="block break-all rounded bg-muted p-2 font-mono text-xs">
            {kind.command}
          </code>
        </div>
      )}
      <label className="grid gap-1 text-sm" htmlFor={field}>
        {kind?.inputLabel ?? 'Credential'}
        {kind?.multiline ? (
          <textarea
            id={field}
            className="min-h-32 w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            value={credential}
            onChange={(e) => setCredential(e.target.value)}
            spellCheck={false}
            autoComplete="off"
            required
          />
        ) : (
          <Input
            id={field}
            type="password"
            value={credential}
            onChange={(e) => setCredential(e.target.value)}
            autoComplete="off"
            required
          />
        )}
      </label>
      <p className="text-xs text-muted-foreground">
        Stored encrypted on this server and never shown again.
      </p>
      <ErrorText error={connect.error} />
      <div>
        <Button type="submit" disabled={connect.isPending || !credential}>
          Save login
        </Button>
      </div>
    </form>
  );
}
