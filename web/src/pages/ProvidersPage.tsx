import { useState } from 'react';
import { Building2 } from 'lucide-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import {
  meQuery,
  orgProvidersQuery,
  providerLoginsQuery,
  type OrgProviderLogin,
  type OrgProviderLoginInput,
  type ProviderLoginInfo,
} from '@/api/queries';
import { AcpStoreSection } from '@/components/AcpStoreSection';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';

/** Harness logins kept on your account and used by your agents. */
export function ProvidersPage() {
  const logins = useQuery(providerLoginsQuery);
  const org = useQuery(orgProvidersQuery);
  const orgProvided = new Set(
    (org.data ?? [])
      .filter((p) => p.configured && p.enabled)
      .map((p) => p.provider),
  );
  return (
    <div className="flex max-w-3xl flex-col gap-6">
      <header>
        <h1 className="text-xl font-semibold">Providers &amp; Harnesses</h1>
        <p className="text-sm text-muted-foreground">
          Sign in to Claude Code, Codex, and Antigravity once, or install
          additional agents from the ACP Store. Every agent that uses your
          logins gets them when it starts, and switching an agent between
          harnesses continues conversations automatically with context handoff.
          Agents only see placeholders: the egress proxy adds the real
          credential to their requests.
        </p>
      </header>
      <OrgProviderPools
        pools={org.data}
        error={org.error}
        names={Object.fromEntries(
          (logins.data ?? []).map((p) => [p.provider, p.name]),
        )}
      />
      {logins.isPending && <Loading />}
      <ErrorText error={logins.error} />
      {logins.data?.map((p) => (
        <Provider
          key={p.provider}
          info={p}
          orgProvided={orgProvided.has(p.provider)}
        />
      ))}
      <AcpStoreSection />
    </div>
  );
}

function Provider({
  info,
  orgProvided,
}: {
  info: ProviderLoginInfo;
  orgProvided: boolean;
}) {
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
        {!login && orgProvided ? (
          <OrgBadge />
        ) : !login ? (
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
            {login ? 'Reconnect' : orgProvided ? 'Use my own' : 'Connect'}
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
      {!login && orgProvided && (
        <p className="text-sm text-muted-foreground">
          Your organization provides {info.name}, so you can chat right away.
          Connect your own login to use it instead.
        </p>
      )}
      {login && (
        <p className="text-sm text-muted-foreground">
          {orgProvided && 'Overrides the organization login · '}
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

const ORG_PROVIDER_NAMES: Record<string, string> = {
  'claude-code': 'Claude Code',
  codex: 'Codex',
  antigravity: 'Antigravity',
  opencode: 'OpenCode',
};

const ORG_KIND_LABEL: Record<OrgProviderLoginInput['kind'], string> = {
  api_key: 'API key',
  oauth_token: 'OAuth token',
  gateway: 'Gateway',
};

function OrgBadge() {
  return (
    <Badge className="gap-1 bg-sky-500/15 text-sky-700 dark:text-sky-400">
      <Building2 className="size-3" aria-hidden />
      Provided by organization
    </Badge>
  );
}

/**
 * Provider logins the organization shares with every user, so new users can
 * chat without personal keys. Admins configure them here.
 */
function OrgProviderPools({
  pools,
  error,
  names,
}: {
  pools?: OrgProviderLogin[];
  error: unknown;
  names: Record<string, string>;
}) {
  const admin = useQuery(meQuery).data?.admin ?? false;
  if (!pools && !error) return null;
  const visible = (pools ?? []).filter(
    (p) => admin || (p.configured && p.enabled),
  );
  if (!admin && visible.length === 0 && !error) return null;
  return (
    <section
      aria-label="Organization provider pools"
      className="grid gap-3 rounded-lg border p-4"
    >
      <div>
        <h2 className="text-sm font-semibold">Organization provider pools</h2>
        <p className="text-sm text-muted-foreground">
          {admin
            ? 'Shared logins every user gets without setting up their own. A personal login always wins over the organization one.'
            : 'These work for you right away, no personal keys needed. Your own login below overrides them.'}
        </p>
      </div>
      <ErrorText error={error} />
      <ul className="divide-y rounded-md border">
        {visible.map((p) => (
          <OrgPool
            key={p.provider}
            pool={p}
            name={
              names[p.provider] ?? ORG_PROVIDER_NAMES[p.provider] ?? p.provider
            }
            admin={admin}
          />
        ))}
      </ul>
    </section>
  );
}

function OrgPool({
  pool,
  name,
  admin,
}: {
  pool: OrgProviderLogin;
  name: string;
  admin: boolean;
}) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE('/org/providers/{provider}', {
          params: { path: { provider: pool.provider } },
        }),
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: orgProvidersQuery.queryKey }),
  });
  const active = pool.configured && pool.enabled;
  return (
    <li aria-label={`${name} organization pool`} className="grid gap-2 p-3">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="font-medium">{name}</span>
        {active ? (
          <OrgBadge />
        ) : pool.configured ? (
          <Badge variant="outline">paused</Badge>
        ) : (
          <Badge variant="outline">not provided</Badge>
        )}
        {pool.configured && pool.kind && (
          <span className="text-xs text-muted-foreground">
            {ORG_KIND_LABEL[pool.kind]}
            {pool.label && ` · ${pool.label}`}
          </span>
        )}
        {admin && (
          <div className="ml-auto flex gap-2">
            <Button size="sm" variant="outline" onClick={() => setOpen(!open)}>
              {pool.configured ? 'Update' : 'Configure'}
            </Button>
            {pool.configured && (
              <Button
                size="sm"
                variant="ghost"
                disabled={remove.isPending}
                onClick={() => {
                  if (
                    confirm(
                      `Remove the organization ${name} login? Users without their own login lose access.`,
                    )
                  )
                    remove.mutate();
                }}
              >
                Remove
              </Button>
            )}
          </div>
        )}
      </div>
      <ErrorText error={remove.error} />
      {open && (
        <OrgPoolForm pool={pool} name={name} onDone={() => setOpen(false)} />
      )}
    </li>
  );
}

function OrgPoolForm({
  pool,
  name,
  onDone,
}: {
  pool: OrgProviderLogin;
  name: string;
  onDone: () => void;
}) {
  const qc = useQueryClient();
  const [kind, setKind] = useState<OrgProviderLoginInput['kind']>(
    pool.kind ?? 'api_key',
  );
  const [label, setLabel] = useState(pool.label ?? '');
  const [credential, setCredential] = useState('');
  const [baseUrl, setBaseUrl] = useState('');
  const [header, setHeader] = useState('');
  const [enabled, setEnabled] = useState(pool.configured ? pool.enabled : true);
  const save = useMutation({
    mutationFn: () => {
      const body: OrgProviderLoginInput = {
        kind,
        enabled,
        ...(label.trim() && { label: label.trim() }),
        ...(credential && { credential }),
        ...(kind === 'gateway' &&
          baseUrl.trim() && { baseUrl: baseUrl.trim() }),
        ...(kind === 'gateway' && header.trim() && { header: header.trim() }),
      };
      return unwrap(
        api.PUT('/org/providers/{provider}', {
          params: { path: { provider: pool.provider } },
          body,
        }),
      );
    },
    onSuccess: async () => {
      setCredential('');
      await qc.invalidateQueries({ queryKey: orgProvidersQuery.queryKey });
      onDone();
    },
  });
  const id = `org-${pool.provider}`;
  return (
    <form
      aria-label={`Organization ${name} login`}
      className="grid gap-3 border-t pt-3 text-sm"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <label className="grid gap-1">
        Credential type
        <Select
          value={kind}
          onChange={(e) =>
            setKind(e.target.value as OrgProviderLoginInput['kind'])
          }
        >
          <option value="api_key">API key</option>
          <option value="oauth_token">OAuth token</option>
          <option value="gateway">Gateway</option>
        </Select>
      </label>
      <label className="grid gap-1" htmlFor={`${id}-label`}>
        Label (optional)
        <Input
          id={`${id}-label`}
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="Team key"
        />
      </label>
      {kind === 'gateway' && (
        <>
          <label className="grid gap-1" htmlFor={`${id}-url`}>
            Gateway URL
            <Input
              id={`${id}-url`}
              type="url"
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
              placeholder="https://gateway.example.com"
              required={!pool.configured}
            />
          </label>
          <label className="grid gap-1" htmlFor={`${id}-header`}>
            Auth header (optional)
            <Input
              id={`${id}-header`}
              value={header}
              onChange={(e) => setHeader(e.target.value)}
              placeholder="Authorization"
            />
          </label>
        </>
      )}
      <label className="grid gap-1" htmlFor={`${id}-cred`}>
        {kind === 'oauth_token' ? 'Token' : 'API key'}
        <Input
          id={`${id}-cred`}
          type="password"
          value={credential}
          onChange={(e) => setCredential(e.target.value)}
          placeholder={
            pool.configured ? 'Leave empty to keep the saved one' : ''
          }
          autoComplete="off"
          required={!pool.configured}
        />
      </label>
      <label className="flex items-center gap-2">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        Provide to every user
      </label>
      <p className="text-xs text-muted-foreground">
        Stored encrypted on this server and never shown again.
      </p>
      <ErrorText error={save.error} />
      <div>
        <Button type="submit" size="sm" disabled={save.isPending}>
          Save organization login
        </Button>
      </div>
    </form>
  );
}
