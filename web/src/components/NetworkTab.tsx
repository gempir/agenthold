import { useState } from 'react';
import {
  queryOptions,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import { KeyRound } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import type { components } from '@/api/schema';
import { presetsQuery, secretsQuery } from '@/components/SecretsPanel';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { formatSize } from '@/lib/files';

type NetworkPolicy = components['schemas']['NetworkPolicy'];

const policyQuery = (agentId: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'network-policy'],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/network-policy', {
          params: { path: { agentId } },
        }),
      ),
    retry: false,
  });

const attachedQuery = (agentId: string) =>
  queryOptions({
    queryKey: ['agents', agentId, 'secrets'],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/secrets', {
          params: { path: { agentId } },
        }),
      ),
    retry: false,
  });

const PAGE = 50;

export function NetworkTab({
  agentId,
  workspaceId,
  admin,
}: {
  agentId: string;
  workspaceId: string;
  admin: boolean;
}) {
  return (
    <div className="flex flex-col gap-8">
      <PolicySection agentId={agentId} admin={admin} />
      <AttachedSecrets
        agentId={agentId}
        workspaceId={workspaceId}
        admin={admin}
      />
      <RequestLog agentId={agentId} />
      <p className="text-sm text-muted-foreground">
        <a
          className="underline"
          href="/api/v1/egress/ca.pem"
          download="agenthold-ca.pem"
        >
          Download the egress CA certificate
        </a>
        . It is trusted inside sandboxes automatically; you only need it to
        inspect traffic elsewhere.
      </p>
    </div>
  );
}

function PolicySection({
  agentId,
  admin,
}: {
  agentId: string;
  admin: boolean;
}) {
  const q = useQuery(policyQuery(agentId));
  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorText error={q.error} />;
  return (
    <PolicyForm
      key={`${q.data.mode}:${q.data.allow.join('\n')}`}
      agentId={agentId}
      admin={admin}
      policy={q.data}
    />
  );
}

function PolicyForm({
  agentId,
  admin,
  policy,
}: {
  agentId: string;
  admin: boolean;
  policy: NetworkPolicy;
}) {
  const qc = useQueryClient();
  const [mode, setMode] = useState(policy.mode);
  const [text, setText] = useState(policy.allow.join('\n'));
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/agents/{agentId}/network-policy', {
          params: { path: { agentId } },
          body: {
            mode,
            allow: text
              .split('\n')
              .map((l) => l.trim())
              .filter(Boolean),
          },
        }),
      ),
    onSuccess: (p) => qc.setQueryData(['agents', agentId, 'network-policy'], p),
  });

  return (
    <form
      className="grid gap-3 rounded-lg border p-4"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <h2 className="text-sm font-semibold">Network policy</h2>
      <fieldset className="grid gap-1 text-sm" disabled={!admin}>
        <label className="flex items-center gap-2">
          <input
            type="radio"
            name="mode"
            checked={mode === 'open'}
            onChange={() => setMode('open')}
          />
          Open (any public address)
        </label>
        <label className="flex items-center gap-2">
          <input
            type="radio"
            name="mode"
            checked={mode === 'allowlist'}
            onChange={() => setMode('allowlist')}
          />
          Allowlist
        </label>
      </fieldset>
      {mode === 'allowlist' && (
        <label className="grid gap-1 text-sm">
          Allowed destinations (one per line)
          <textarea
            className="min-h-28 rounded-md border border-input bg-background p-2 font-mono text-sm disabled:opacity-50"
            disabled={!admin}
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={'github.com\n*.npmjs.org\ngithub.com:22'}
          />
          <span className="text-xs text-muted-foreground">
            Entries like <code>github.com</code>, <code>*.npmjs.org</code>,{' '}
            <code>github.com:22</code>, <code>host:*</code> or{' '}
            <code>1.2.3.0/24:443</code>. Without a port, 80 and 443 are allowed.
          </span>
        </label>
      )}
      <ErrorText error={save.error} />
      {admin ? (
        <div className="flex items-center gap-3">
          <Button type="submit" disabled={save.isPending}>
            Save policy
          </Button>
          {save.isSuccess && (
            <span className="text-xs text-muted-foreground">
              Saved. Takes effect within seconds.
            </span>
          )}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">
          Only agent admins can change the policy.
        </p>
      )}
    </form>
  );
}

function AttachedSecrets({
  agentId,
  workspaceId,
  admin,
}: {
  agentId: string;
  workspaceId: string;
  admin: boolean;
}) {
  const qc = useQueryClient();
  const attached = useQuery(attachedQuery(agentId));
  const all = useQuery({ ...secretsQuery(workspaceId), enabled: admin });
  const presets = useQuery({ ...presetsQuery, enabled: admin });
  const [secretId, setSecretId] = useState('');
  const [envVar, setEnvVar] = useState('');

  const refresh = async () => {
    await qc.invalidateQueries({ queryKey: ['agents', agentId] });
  };

  const attach = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/agents/{agentId}/secrets/{secretId}', {
          params: { path: { agentId, secretId } },
          body: { envVar: envVar.trim() },
        }),
      ),
    onSuccess: async () => {
      setSecretId('');
      setEnvVar('');
      await refresh();
    },
  });
  const detach = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/agents/{agentId}/secrets/{secretId}', {
          params: { path: { agentId, secretId: id } },
        }),
      ),
    onSuccess: refresh,
  });

  const attachedIds = new Set(attached.data?.map((a) => a.secret.id));
  const available = all.data?.filter((s) => !attachedIds.has(s.id)) ?? [];

  function pick(id: string) {
    setSecretId(id);
    const s = available.find((x) => x.id === id);
    const p = presets.data?.find((x) => x.name === s?.name);
    setEnvVar(p?.envVar ?? '');
  }

  return (
    <section className="grid gap-3">
      <h2 className="text-sm font-semibold">Secrets attached to this agent</h2>
      {attached.isPending && <Loading />}
      <ErrorText error={attached.error} />
      {attached.data?.length === 0 && (
        <p className="text-sm text-muted-foreground">No secrets attached.</p>
      )}
      {!!attached.data?.length && (
        <ul className="divide-y rounded-lg border">
          {attached.data.map((a) => (
            <li
              key={a.secret.id}
              className="flex flex-wrap items-center gap-3 p-3 text-sm"
            >
              <span className="font-medium">{a.secret.name}</span>
              <span className="text-xs text-muted-foreground">
                {a.secret.rule.hosts.join(', ')}
              </span>
              {a.envVar && <code className="text-xs">{a.envVar}</code>}
              <span className="flex-1" />
              {admin && (
                <Button
                  variant="outline"
                  size="sm"
                  disabled={detach.isPending}
                  onClick={() => detach.mutate(a.secret.id)}
                >
                  Detach
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      <p className="text-xs text-muted-foreground">
        Changing environment variables requires a rebuild.
      </p>
      <ErrorText error={detach.error} />
      {admin && (
        <form
          className="flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            attach.mutate();
          }}
        >
          <label className="grid gap-1 text-xs text-muted-foreground">
            Secret
            <Select
              aria-label="Secret to attach"
              value={secretId}
              onChange={(e) => pick(e.target.value)}
            >
              <option value="">Select a secret</option>
              {available.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </Select>
          </label>
          <label className="grid gap-1 text-xs text-muted-foreground">
            Environment variable
            <Input
              aria-label="Environment variable"
              className="font-mono"
              value={envVar}
              onChange={(e) => setEnvVar(e.target.value)}
            />
          </label>
          <Button type="submit" disabled={!secretId || attach.isPending}>
            Attach
          </Button>
        </form>
      )}
      <ErrorText error={attach.error} />
    </section>
  );
}

function RequestLog({ agentId }: { agentId: string }) {
  const q = useInfiniteQuery({
    queryKey: ['agents', agentId, 'egress-log'],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/agents/{agentId}/egress-log', {
          params: {
            path: { agentId },
            query: { limit: PAGE, ...(pageParam ? { before: pageParam } : {}) },
          },
        }),
      ),
    initialPageParam: 0,
    getNextPageParam: (last) =>
      last.length >= PAGE ? last[last.length - 1].id : undefined,
    refetchInterval: 5000,
    retry: false,
  });
  const events = q.data?.pages.flat() ?? [];

  return (
    <section className="grid gap-3">
      <div className="flex items-center gap-3">
        <h2 className="flex-1 text-sm font-semibold">Request log</h2>
        <Button
          variant="outline"
          size="sm"
          disabled={q.isFetching}
          onClick={() => q.refetch()}
        >
          Refresh
        </Button>
      </div>
      {q.isPending && <Loading />}
      <ErrorText error={q.error} />
      {q.data && events.length === 0 && (
        <p className="text-sm text-muted-foreground">No requests yet.</p>
      )}
      {events.length > 0 && (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-left text-xs">
            <thead className="text-muted-foreground">
              <tr>
                <th className="p-2">Time</th>
                <th className="p-2">Kind</th>
                <th className="p-2">Destination</th>
                <th className="p-2">Request</th>
                <th className="p-2">Status</th>
                <th className="p-2">Result</th>
                <th className="p-2">Up / down</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {events.map((e) => (
                <tr key={e.id}>
                  <td className="whitespace-nowrap p-2">
                    {new Date(e.at).toLocaleTimeString()}
                  </td>
                  <td className="p-2">{e.kind}</td>
                  <td className="p-2 font-mono">
                    {e.host}:{e.port}
                  </td>
                  <td className="max-w-64 truncate p-2 font-mono">
                    {e.method} {e.path}
                  </td>
                  <td className="p-2">{e.status || ''}</td>
                  <td className="p-2">
                    <span className="inline-flex items-center gap-1">
                      <Badge
                        variant={e.allowed ? 'secondary' : 'outline'}
                        className={
                          e.allowed ? '' : 'border-destructive text-destructive'
                        }
                        title={e.reason || undefined}
                      >
                        {e.allowed ? 'allowed' : 'denied'}
                      </Badge>
                      {e.injected && (
                        <KeyRound
                          className="size-3.5"
                          aria-label="Secret injected"
                        />
                      )}
                    </span>
                    {!e.allowed && e.reason && (
                      <div className="text-destructive">{e.reason}</div>
                    )}
                  </td>
                  <td className="whitespace-nowrap p-2">
                    {formatSize(e.bytesUp)} / {formatSize(e.bytesDown)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {q.hasNextPage && (
        <div>
          <Button
            variant="outline"
            size="sm"
            disabled={q.isFetchingNextPage}
            onClick={() => q.fetchNextPage()}
          >
            Load more
          </Button>
        </div>
      )}
    </section>
  );
}
