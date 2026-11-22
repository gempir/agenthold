import { useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Play, RotateCw, ScrollText, Square, Trash2 } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { servicesQuery, type Service } from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { appendLog, serviceLogsUrl, streamError } from '@/lib/services';
import { cn } from '@/lib/utils';

const stateColor: Record<string, string> = {
  running:
    'border-transparent bg-emerald-500/15 text-emerald-700 dark:text-emerald-400',
  backoff:
    'border-transparent bg-amber-500/15 text-amber-700 dark:text-amber-400',
  failed: 'border-transparent bg-red-500/15 text-red-700 dark:text-red-400',
};

/** One-line description of a service's live status. */
export function serviceStatusText(s: Service, machineRunning: boolean) {
  if (!s.status) return machineRunning ? 'not started' : 'down (machine off)';
  const st = s.status;
  const parts: string[] = [st.state];
  if (st.restarts > 0) parts.push(`${st.restarts} restarts`);
  if (st.signal) parts.push(`last ended by ${st.signal}`);
  else if (st.exitCode != null && st.state !== 'running')
    parts.push(`exit ${st.exitCode}`);
  if (st.error) parts.push(st.error);
  return parts.join(' · ');
}

/** Services supervised in a machine: status, restart/stop, live logs. */
export function ServicesTab({
  agentId,
  isOwner,
  allowAdd = true,
}: {
  agentId: string;
  isOwner: boolean;
  allowAdd?: boolean;
}) {
  const qc = useQueryClient();
  const q = useQuery(servicesQuery(agentId));
  const [logsOf, setLogsOf] = useState<string>();
  const refresh = () =>
    qc.invalidateQueries({ queryKey: servicesQuery(agentId).queryKey });
  const act = useMutation({
    mutationFn: ({
      name,
      op,
    }: {
      name: string;
      op: 'start' | 'stop' | 'restart' | 'delete';
    }) => {
      const params = { path: { agentId, serviceName: name } };
      switch (op) {
        case 'start':
          return unwrap(
            api.POST('/agents/{agentId}/services/{serviceName}/start', {
              params,
            }),
          );
        case 'stop':
          return unwrap(
            api.POST('/agents/{agentId}/services/{serviceName}/stop', {
              params,
            }),
          );
        case 'restart':
          return unwrap(
            api.POST('/agents/{agentId}/services/{serviceName}/restart', {
              params,
            }),
          );
        case 'delete':
          return unwrap(
            api.DELETE('/agents/{agentId}/services/{serviceName}', { params }),
          );
      }
    },
    onSettled: refresh,
  });

  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorText error={q.error} />;
  const { services, running } = q.data;
  return (
    <div className="grid gap-6">
      <section className="grid gap-2">
        {!running && services.length > 0 && (
          <p className="text-sm text-muted-foreground">
            The machine is not running, so its services are down. Enabled
            services start when it boots.
          </p>
        )}
        {q.data.error && (
          <p role="alert" className="text-sm text-destructive">
            {q.data.error}
          </p>
        )}
        {services.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No services. A service keeps a command running in the machine (a dev
            server, a worker) across turns and restarts it when it exits. Agents
            add one with start_service.
          </p>
        ) : (
          <ul aria-label="Services" className="divide-y rounded-lg border">
            {services.map((s) => (
              <li key={s.name} className="grid gap-2 p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{s.name}</span>
                  <Badge
                    className={cn(s.status && stateColor[s.status.state])}
                    aria-label={`${s.name} status`}
                  >
                    {serviceStatusText(s, running)}
                  </Badge>
                  <span className="flex-1" />
                  {running && (
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Logs of ${s.name}`}
                      aria-pressed={logsOf === s.name}
                      onClick={() =>
                        setLogsOf(logsOf === s.name ? undefined : s.name)
                      }
                    >
                      <ScrollText className="size-4" />
                    </Button>
                  )}
                  {isOwner && (
                    <>
                      {running && s.enabled && (
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Restart ${s.name}`}
                          disabled={act.isPending}
                          onClick={() =>
                            act.mutate({ name: s.name, op: 'restart' })
                          }
                        >
                          <RotateCw className="size-4" />
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={`${s.enabled ? 'Stop' : 'Start'} ${s.name}`}
                        disabled={act.isPending}
                        onClick={() =>
                          act.mutate({
                            name: s.name,
                            op: s.enabled ? 'stop' : 'start',
                          })
                        }
                      >
                        {s.enabled ? (
                          <Square className="size-4" />
                        ) : (
                          <Play className="size-4" />
                        )}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={`Remove ${s.name}`}
                        disabled={act.isPending}
                        onClick={() =>
                          act.mutate({ name: s.name, op: 'delete' })
                        }
                      >
                        <Trash2 className="size-4" />
                      </Button>
                    </>
                  )}
                </div>
                <code className="truncate text-xs text-muted-foreground">
                  {s.workdir ? `${s.workdir}$ ` : '~$ '}
                  {s.command}
                </code>
                {logsOf === s.name && running && (
                  <ServiceLogs agentId={agentId} name={s.name} />
                )}
              </li>
            ))}
          </ul>
        )}
        <ErrorText error={act.error} />
      </section>
      {isOwner && allowAdd && (
        <AddServiceForm agentId={agentId} onDone={refresh} />
      )}
    </div>
  );
}

/** Live output of a service (the last lines, then whatever it writes).
 * Render it with a key per service: it follows one stream for its life. */
export function ServiceLogs({
  agentId,
  name,
}: {
  agentId: string;
  name: string;
}) {
  const [text, setText] = useState('');
  const [error, setError] = useState<string>();
  const box = useRef<HTMLPreElement>(null);
  useEffect(() => {
    const ws = new WebSocket(
      serviceLogsUrl(globalThis.location, agentId, name),
    );
    ws.onmessage = (ev) => {
      const data = String(ev.data);
      const err = streamError(data);
      if (err) setError(err);
      else setText((t) => appendLog(t, data));
    };
    return () => ws.close();
  }, [agentId, name]);
  useEffect(() => {
    const el = box.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [text]);
  return (
    <>
      <pre
        ref={box}
        aria-label={`Log of ${name}`}
        className="max-h-80 overflow-auto rounded-md bg-[#0b0b0c] p-3 font-mono text-xs whitespace-pre-wrap text-zinc-200"
      >
        {text || 'Waiting for output…'}
      </pre>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
    </>
  );
}

function AddServiceForm({
  agentId,
  onDone,
}: {
  agentId: string;
  onDone: () => Promise<unknown>;
}) {
  const [name, setName] = useState('');
  const [command, setCommand] = useState('');
  const [workdir, setWorkdir] = useState('');
  const put = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/agents/{agentId}/services/{serviceName}', {
          params: { path: { agentId, serviceName: name.trim() } },
          body: {
            command,
            ...(workdir.trim() && { workdir: workdir.trim() }),
          },
        }),
      ),
    onSuccess: async () => {
      setName('');
      setCommand('');
      setWorkdir('');
      await onDone();
    },
  });
  return (
    <form
      className="grid gap-3 rounded-lg border p-4"
      onSubmit={(e) => {
        e.preventDefault();
        put.mutate();
      }}
    >
      <h2 className="text-sm font-semibold">Add a service</h2>
      <div className="grid gap-3 sm:grid-cols-[10rem_1fr]">
        <label className="grid gap-1 text-sm">
          Name
          <Input
            value={name}
            onChange={(e) => setName(e.target.value.toLowerCase())}
            placeholder="web"
            required
          />
        </label>
        <label className="grid gap-1 text-sm">
          Command
          <Input
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            placeholder="bun run dev --port 5173"
            required
          />
        </label>
      </div>
      <label className="grid gap-1 text-sm">
        Working directory (optional, relative to the home)
        <Input value={workdir} onChange={(e) => setWorkdir(e.target.value)} />
      </label>
      <div>
        <Button
          type="submit"
          disabled={put.isPending || !name.trim() || !command.trim()}
        >
          Add and start
        </Button>
      </div>
      <ErrorText error={put.error} />
    </form>
  );
}
