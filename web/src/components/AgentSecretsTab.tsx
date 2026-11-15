import { useState } from 'react';
import {
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import { presetsQuery, secretsQuery } from '@/components/SecretsPanel';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';

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

/** The agent's secrets: what the egress proxy injects into its requests. */
export function AgentSecretsTab({
  agentId,
  isOwner,
}: {
  agentId: string;
  isOwner: boolean;
}) {
  return (
    <div className="flex flex-col gap-8">
      <AttachedSecrets agentId={agentId} isOwner={isOwner} />
      <p className="text-sm text-muted-foreground">
        Agents reach the internet through the egress proxy, which replaces
        secret placeholders with their values on the wire.{' '}
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

function AttachedSecrets({
  agentId,
  isOwner,
}: {
  agentId: string;
  isOwner: boolean;
}) {
  const qc = useQueryClient();
  const attached = useQuery(attachedQuery(agentId));
  const all = useQuery({ ...secretsQuery, enabled: isOwner });
  const presets = useQuery({ ...presetsQuery, enabled: isOwner });
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
              {isOwner && (
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
        Changing environment variables requires a system reset.
      </p>
      <ErrorText error={detach.error} />
      {isOwner && (
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
