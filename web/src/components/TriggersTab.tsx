import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import type { components } from '@/api/schema';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

type Schedule = components['schemas']['Schedule'];
type Webhook = components['schemas']['Webhook'];

const browserTz = () => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
};

function ScheduleForm({
  initial,
  busy,
  error,
  onSubmit,
}: {
  initial?: Schedule;
  busy: boolean;
  error: unknown;
  onSubmit: (v: components['schemas']['ScheduleInput']) => void;
}) {
  const [name, setName] = useState(initial?.name ?? '');
  const [cron, setCron] = useState(initial?.cron ?? '0 9 * * 1-5');
  const [tz, setTz] = useState(initial?.timezone ?? browserTz());
  const [prompt, setPrompt] = useState(initial?.prompt ?? '');
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  return (
    <form
      className="grid gap-3 rounded-lg border p-4"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit({ name, cron, timezone: tz, prompt, enabled });
      }}
    >
      <div className="grid grid-cols-3 gap-3">
        <label className="grid gap-1 text-sm">
          Name
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Daily summary"
            required
          />
        </label>
        <label className="grid gap-1 text-sm">
          Cron (min hour day month weekday)
          <Input
            value={cron}
            onChange={(e) => setCron(e.target.value)}
            className="font-mono"
            required
          />
        </label>
        <label className="grid gap-1 text-sm">
          Time zone
          <Input value={tz} onChange={(e) => setTz(e.target.value)} />
        </label>
      </div>
      <label className="grid gap-1 text-sm">
        Prompt
        <textarea
          aria-label="Prompt"
          className="min-h-20 rounded-md border bg-background p-2 text-sm"
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          required
        />
      </label>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        Enabled
      </label>
      <ErrorText error={error} />
      <div>
        <Button type="submit" disabled={busy}>
          Save schedule
        </Button>
      </div>
    </form>
  );
}

function WebhookForm({
  initial,
  busy,
  error,
  onSubmit,
}: {
  initial?: Webhook;
  busy: boolean;
  error: unknown;
  onSubmit: (v: components['schemas']['WebhookInput']) => void;
}) {
  const [name, setName] = useState(initial?.name ?? '');
  const [tpl, setTpl] = useState(
    initial?.promptTemplate ?? 'Handle this event:\n{{payload}}',
  );
  const [secret, setSecret] = useState('');
  const [header, setHeader] = useState(
    initial?.signatureHeader ?? 'X-Hub-Signature-256',
  );
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  return (
    <form
      className="grid gap-3 rounded-lg border p-4"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit({
          name,
          promptTemplate: tpl,
          signatureHeader: header,
          enabled,
          ...(secret && { signingSecret: secret }),
        });
      }}
    >
      <label className="grid gap-1 text-sm">
        Name
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="GitHub issues"
          required
        />
      </label>
      <label className="grid gap-1 text-sm">
        Prompt template (<code>{'{{payload}}'}</code> marks where the request
        body goes)
        <textarea
          aria-label="Prompt template"
          className="min-h-20 rounded-md border bg-background p-2 text-sm"
          value={tpl}
          onChange={(e) => setTpl(e.target.value)}
          required
        />
      </label>
      <div className="grid grid-cols-2 gap-3">
        <label className="grid gap-1 text-sm">
          Signing secret{' '}
          {initial?.signed
            ? '(set; leave empty to keep)'
            : '(optional, HMAC-SHA256)'}
          <Input
            type="password"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
          />
        </label>
        <label className="grid gap-1 text-sm">
          Signature header
          <Input value={header} onChange={(e) => setHeader(e.target.value)} />
        </label>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        Enabled
      </label>
      <p className="text-xs text-muted-foreground">
        Request bodies are passed to the agent clearly marked as untrusted data.
      </p>
      <ErrorText error={error} />
      <div>
        <Button type="submit" disabled={busy}>
          Save webhook
        </Button>
      </div>
    </form>
  );
}

/** Schedules and webhooks of an agent (admins manage them, operators can run schedules). */
export function TriggersTab({
  agentId,
  admin,
  operator,
  onOpenSession,
}: {
  agentId: string;
  admin: boolean;
  operator: boolean;
  onOpenSession: (id: string) => void;
}) {
  const qc = useQueryClient();
  const schedules = useQuery({
    queryKey: ['agents', agentId, 'schedules'],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/schedules', {
          params: { path: { agentId } },
        }),
      ),
  });
  const webhooks = useQuery({
    queryKey: ['agents', agentId, 'webhooks'],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/webhooks', {
          params: { path: { agentId } },
        }),
      ),
    enabled: admin,
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ['agents', agentId] });
  const [form, setForm] = useState<string>(''); // 'new-schedule' | 'new-webhook' | id
  const [hookUrl, setHookUrl] = useState('');

  const saveSchedule = useMutation({
    mutationFn: ({
      id,
      body,
    }: {
      id?: string;
      body: components['schemas']['ScheduleInput'];
    }) =>
      id
        ? unwrap(
            api.PUT('/schedules/{scheduleId}', {
              params: { path: { scheduleId: id } },
              body,
            }),
          )
        : unwrap(
            api.POST('/agents/{agentId}/schedules', {
              params: { path: { agentId } },
              body,
            }),
          ),
    onSuccess: async () => {
      setForm('');
      await refresh();
    },
  });
  const delSchedule = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/schedules/{scheduleId}', {
          params: { path: { scheduleId: id } },
        }),
      ),
    onSuccess: refresh,
  });
  const runSchedule = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.POST('/schedules/{scheduleId}/run', {
          params: { path: { scheduleId: id } },
        }),
      ),
    onSuccess: refresh,
  });
  const saveWebhook = useMutation({
    mutationFn: async ({
      id,
      body,
    }: {
      id?: string;
      body: components['schemas']['WebhookInput'];
    }) => {
      if (id) {
        await unwrap(
          api.PUT('/webhooks/{webhookId}', {
            params: { path: { webhookId: id } },
            body,
          }),
        );
        return '';
      }
      const w = await unwrap(
        api.POST('/agents/{agentId}/webhooks', {
          params: { path: { agentId } },
          body,
        }),
      );
      return w.url;
    },
    onSuccess: async (url) => {
      setForm('');
      if (url) setHookUrl(url);
      await refresh();
    },
  });
  const delWebhook = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/webhooks/{webhookId}', {
          params: { path: { webhookId: id } },
        }),
      ),
    onSuccess: refresh,
  });

  return (
    <div className="flex flex-col gap-8">
      <p className="text-sm text-muted-foreground">
        Schedules and webhooks prompt their own session as the person who
        created them, and wake the agent if it sleeps.
      </p>
      <section className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <h2 className="text-base font-semibold">Schedules</h2>
          {admin && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => setForm('new-schedule')}
            >
              Add schedule
            </Button>
          )}
        </div>
        {form === 'new-schedule' && (
          <ScheduleForm
            busy={saveSchedule.isPending}
            error={saveSchedule.error}
            onSubmit={(body) => saveSchedule.mutate({ body })}
          />
        )}
        {schedules.isPending && <Loading />}
        <ErrorText
          error={schedules.error ?? runSchedule.error ?? delSchedule.error}
        />
        <ul aria-label="Schedules" className="divide-y rounded-lg border">
          {schedules.data?.length === 0 && (
            <li className="p-3 text-sm text-muted-foreground">No schedules.</li>
          )}
          {schedules.data?.map((s) => (
            <li key={s.id} className="flex flex-col gap-2 p-3 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium">{s.name}</span>
                <code className="text-xs">{s.cron}</code>
                <span className="text-xs text-muted-foreground">
                  {s.timezone}
                </span>
                {!s.enabled && <Badge variant="outline">disabled</Badge>}
                <span className="text-xs text-muted-foreground">
                  next {new Date(s.nextRunAt).toLocaleString()}
                  {s.lastRunAt &&
                    ` · last ${new Date(s.lastRunAt).toLocaleString()}`}
                </span>
                <span className="ml-auto flex gap-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => onOpenSession(s.sessionId)}
                  >
                    Open session
                  </Button>
                  {operator && (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => runSchedule.mutate(s.id)}
                    >
                      Run now
                    </Button>
                  )}
                  {admin && (
                    <>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => setForm(s.id)}
                      >
                        Edit
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() =>
                          confirm(`Delete ${s.name}?`) &&
                          delSchedule.mutate(s.id)
                        }
                      >
                        Delete
                      </Button>
                    </>
                  )}
                </span>
              </div>
              {s.lastError && (
                <span className="text-xs text-destructive">
                  Last run: {s.lastError}
                </span>
              )}
              {form === s.id && (
                <ScheduleForm
                  initial={s}
                  busy={saveSchedule.isPending}
                  error={saveSchedule.error}
                  onSubmit={(body) => saveSchedule.mutate({ id: s.id, body })}
                />
              )}
            </li>
          ))}
        </ul>
      </section>

      {admin && (
        <section className="flex flex-col gap-3">
          <div className="flex items-center justify-between">
            <h2 className="text-base font-semibold">Webhooks</h2>
            <Button
              size="sm"
              variant="outline"
              onClick={() => setForm('new-webhook')}
            >
              Add webhook
            </Button>
          </div>
          {hookUrl && (
            <div
              className="rounded-lg border border-amber-500/50 bg-amber-500/10 p-3 text-sm"
              role="status"
            >
              Copy the delivery URL now; it is not shown again:
              <code className="mt-2 block break-all rounded bg-background p-2 font-mono text-xs">
                {hookUrl}
              </code>
            </div>
          )}
          {form === 'new-webhook' && (
            <WebhookForm
              busy={saveWebhook.isPending}
              error={saveWebhook.error}
              onSubmit={(body) => saveWebhook.mutate({ body })}
            />
          )}
          <ErrorText error={webhooks.error ?? delWebhook.error} />
          <ul aria-label="Webhooks" className="divide-y rounded-lg border">
            {webhooks.data?.length === 0 && (
              <li className="p-3 text-sm text-muted-foreground">
                No webhooks.
              </li>
            )}
            {webhooks.data?.map((w) => (
              <li key={w.id} className="flex flex-col gap-2 p-3 text-sm">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{w.name}</span>
                  {w.signed && <Badge variant="outline">signed</Badge>}
                  {!w.enabled && <Badge variant="outline">disabled</Badge>}
                  <span className="text-xs text-muted-foreground">
                    {w.lastDeliveryAt
                      ? `last delivery ${new Date(w.lastDeliveryAt).toLocaleString()}`
                      : 'no deliveries yet'}
                  </span>
                  <span className="ml-auto flex gap-1">
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => onOpenSession(w.sessionId)}
                    >
                      Open session
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => setForm(w.id)}
                    >
                      Edit
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() =>
                        confirm(`Delete ${w.name}?`) && delWebhook.mutate(w.id)
                      }
                    >
                      Delete
                    </Button>
                  </span>
                </div>
                {w.lastError && (
                  <span className="text-xs text-destructive">
                    Last delivery: {w.lastError}
                  </span>
                )}
                {form === w.id && (
                  <WebhookForm
                    initial={w}
                    busy={saveWebhook.isPending}
                    error={saveWebhook.error}
                    onSubmit={(body) => saveWebhook.mutate({ id: w.id, body })}
                  />
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}
