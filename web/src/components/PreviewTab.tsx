import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Copy, ExternalLink, Trash2 } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  exposuresQuery,
  type Exposure,
  type ExposureAccess,
} from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';

const ACCESS: { value: ExposureAccess; label: string }[] = [
  { value: 'private', label: 'Private: you and people it is shared with' },
  { value: 'authenticated', label: 'Signed in: anyone with an account here' },
  { value: 'public', label: 'Public: anyone with the link' },
];

/** Ports of the machine served over HTTP at preview URLs. */
export function PreviewTab({
  agentId,
  isOwner,
}: {
  agentId: string;
  isOwner: boolean;
}) {
  const qc = useQueryClient();
  const q = useQuery(exposuresQuery(agentId));
  const refresh = () =>
    qc.invalidateQueries({ queryKey: exposuresQuery(agentId).queryKey });

  const update = useMutation({
    mutationFn: ({
      id,
      ...body
    }: {
      id: string;
      access?: ExposureAccess;
      wakeOnRequest?: boolean;
    }) =>
      unwrap(
        api.PATCH('/agents/{agentId}/exposures/{exposureId}', {
          params: { path: { agentId, exposureId: id } },
          body,
        }),
      ),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/agents/{agentId}/exposures/{exposureId}', {
          params: { path: { agentId, exposureId: id } },
        }),
      ),
    onSuccess: refresh,
  });

  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorText error={q.error} />;
  if (!q.data.enabled)
    return (
      <p className="text-sm text-muted-foreground">
        Preview URLs are not set up on this server. An administrator turns them
        on with <code>preview.host_suffix</code> and a wildcard DNS record.
      </p>
    );
  const { exposures } = q.data;

  return (
    <div className="grid gap-6">
      <section className="grid gap-2">
        <h2 className="text-sm font-semibold">Exposed ports</h2>
        {exposures.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No ports are exposed. Start a server in the machine, then expose its
            port here, or ask the agent to call expose_port.
          </p>
        ) : (
          <ul aria-label="Exposed ports" className="divide-y rounded-lg border">
            {exposures.map((e) => (
              <ExposureRow
                key={e.id}
                exposure={e}
                isOwner={isOwner}
                busy={update.isPending || remove.isPending}
                onAccess={(access) => update.mutate({ id: e.id, access })}
                onWake={(wakeOnRequest) =>
                  update.mutate({ id: e.id, wakeOnRequest })
                }
                onRemove={() => remove.mutate(e.id)}
              />
            ))}
          </ul>
        )}
        <ErrorText error={update.error ?? remove.error} />
      </section>
      {isOwner && <ExposeForm agentId={agentId} onDone={refresh} />}
    </div>
  );
}

function ExposureRow({
  exposure: e,
  isOwner,
  busy,
  onAccess,
  onWake,
  onRemove,
}: {
  exposure: Exposure;
  isOwner: boolean;
  busy: boolean;
  onAccess: (a: ExposureAccess) => void;
  onWake: (wake: boolean) => void;
  onRemove: () => void;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <li className="flex flex-wrap items-center gap-3 p-3">
      <Badge variant="outline">:{e.port}</Badge>
      <a
        href={e.url}
        target="_blank"
        rel="noreferrer"
        className="min-w-0 flex-1 truncate text-sm font-medium hover:underline"
      >
        {e.url}
      </a>
      <Button asChild variant="ghost" size="icon-sm">
        <a
          href={e.url}
          target="_blank"
          rel="noreferrer"
          aria-label={`Open ${e.name}`}
        >
          <ExternalLink className="size-4" />
        </a>
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={copied ? 'Copied' : `Copy link to ${e.name}`}
        onClick={() => {
          void navigator.clipboard?.writeText(e.url).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          });
        }}
      >
        <Copy className="size-4" />
      </Button>
      {isOwner ? (
        <>
          <Select
            aria-label={`Access to ${e.name}`}
            value={e.access}
            disabled={busy}
            onChange={(ev) => onAccess(ev.target.value as ExposureAccess)}
          >
            {ACCESS.map((a) => (
              <option key={a.value} value={a.value}>
                {a.label.split(':')[0]}
              </option>
            ))}
          </Select>
          {e.access !== 'private' && (
            <label className="flex items-center gap-1 text-xs text-muted-foreground">
              <input
                type="checkbox"
                checked={e.wakeOnRequest}
                disabled={busy}
                onChange={(ev) => onWake(ev.target.checked)}
              />
              Visitors wake it
            </label>
          )}
          <Button
            variant="ghost"
            size="icon-sm"
            disabled={busy}
            aria-label={`Remove ${e.name}`}
            onClick={onRemove}
          >
            <Trash2 className="size-4" />
          </Button>
        </>
      ) : (
        <Badge>{e.access}</Badge>
      )}
    </li>
  );
}

function ExposeForm({
  agentId,
  onDone,
}: {
  agentId: string;
  onDone: () => Promise<unknown>;
}) {
  const [port, setPort] = useState('');
  const [name, setName] = useState('');
  const [access, setAccess] = useState<ExposureAccess>('private');
  const [wake, setWake] = useState(false);
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/agents/{agentId}/exposures', {
          params: { path: { agentId } },
          body: {
            port: Number(port),
            access,
            ...(access !== 'private' && wake && { wakeOnRequest: true }),
            ...(name.trim() && { name: name.trim() }),
          },
        }),
      ),
    onSuccess: async () => {
      setPort('');
      setName('');
      await onDone();
    },
  });
  return (
    <form
      className="grid gap-3 rounded-lg border p-4"
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate();
      }}
    >
      <h2 className="text-sm font-semibold">Expose a port</h2>
      <div className="grid gap-3 sm:grid-cols-[8rem_1fr]">
        <label className="grid gap-1 text-sm">
          Port
          <Input
            inputMode="numeric"
            required
            placeholder="3000"
            value={port}
            onChange={(e) => setPort(e.target.value.replace(/\D/g, ''))}
          />
        </label>
        <label className="grid gap-1 text-sm">
          Name (optional)
          <Input
            placeholder="my-app"
            value={name}
            onChange={(e) => setName(e.target.value.toLowerCase())}
          />
        </label>
      </div>
      <label className="grid gap-1 text-sm">
        Who can open it
        <Select
          value={access}
          onChange={(e) => setAccess(e.target.value as ExposureAccess)}
        >
          {ACCESS.map((a) => (
            <option key={a.value} value={a.value}>
              {a.label}
            </option>
          ))}
        </Select>
      </label>
      {access !== 'private' && (
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={wake}
            onChange={(e) => setWake(e.target.checked)}
          />
          Visitors without access to the machine may wake it from sleep
        </label>
      )}
      <p className="text-xs text-muted-foreground">
        Requests reach whatever listens on that port inside the machine.
        Signed-in visitors arrive with X-Agenthold-User, X-Agenthold-Email and a
        signed X-Agenthold-Identity header.
      </p>
      <ErrorText error={create.error} />
      <div>
        <Button type="submit" disabled={!port || create.isPending}>
          Expose
        </Button>
      </div>
    </form>
  );
}
