import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Check, Copy } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import type { components } from '@/api/schema';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { queryOptions } from '@tanstack/react-query';

export type Secret = components['schemas']['Secret'];
export type SecretRule = components['schemas']['SecretRule'];
export type SecretPreset = components['schemas']['SecretPreset'];

export const secretsQuery = (workspaceId: string) =>
  queryOptions({
    queryKey: ['workspaces', workspaceId, 'secrets'],
    queryFn: () =>
      unwrap(
        api.GET('/workspaces/{workspaceId}/secrets', {
          params: { path: { workspaceId } },
        }),
      ),
    retry: false,
  });

export const presetsQuery = queryOptions({
  queryKey: ['secrets', 'presets'],
  queryFn: () => unwrap(api.GET('/secrets/presets')),
  retry: false,
});

const DEFAULT_TEMPLATE = '{{.Value}}';

function parseHosts(s: string): string[] {
  return s
    .split(',')
    .map((h) => h.trim())
    .filter(Boolean);
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      aria-label="Copy placeholder"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setCopied(true);
        } catch {
          setCopied(false);
        }
      }}
    >
      {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
    </Button>
  );
}

export function SecretsPanel({ workspaceId }: { workspaceId: string }) {
  const qc = useQueryClient();
  const secrets = useQuery(secretsQuery(workspaceId));
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Secret | null>(null);
  const [deleting, setDeleting] = useState<Secret | null>(null);

  const refresh = () =>
    qc.invalidateQueries({ queryKey: ['workspaces', workspaceId, 'secrets'] });

  const del = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/secrets/{secretId}', {
          params: { path: { secretId: id } },
        }),
      ),
    onSuccess: async () => {
      setDeleting(null);
      await refresh();
    },
  });

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <p className="min-w-0 flex-1 text-sm text-muted-foreground">
          Secret values never enter sandboxes; the egress proxy injects them
          into requests to the listed hosts, and sandboxes only see a
          placeholder.
        </p>
        <Button onClick={() => setCreating(true)}>New secret</Button>
      </div>
      {secrets.isPending && <Loading />}
      <ErrorText error={secrets.error} />
      {secrets.data?.length === 0 && (
        <p className="text-sm text-muted-foreground">No secrets yet.</p>
      )}
      {!!secrets.data?.length && (
        <ul className="divide-y rounded-lg border">
          {secrets.data.map((s) => (
            <li key={s.id} className="flex flex-col gap-2 p-3 text-sm">
              <div className="flex flex-wrap items-center gap-3">
                <span className="font-medium">{s.name}</span>
                <span className="text-xs text-muted-foreground">
                  owner {s.owner.name}
                </span>
                <span className="flex-1" />
                {s.canManage && (
                  <>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setEditing(s)}
                    >
                      Edit
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setDeleting(s)}
                    >
                      Delete
                    </Button>
                  </>
                )}
              </div>
              <div className="text-xs text-muted-foreground">
                Hosts: {s.rule.hosts.join(', ')} &middot; Header:{' '}
                <code>{s.rule.header}</code>
              </div>
              <div className="flex items-center gap-2">
                <code className="min-w-0 flex-1 break-all rounded bg-muted px-2 py-1 font-mono text-xs">
                  {s.placeholder}
                </code>
                <CopyButton text={s.placeholder} />
              </div>
            </li>
          ))}
        </ul>
      )}

      {creating && (
        <SecretDialog
          workspaceId={workspaceId}
          onClose={() => setCreating(false)}
          onSaved={refresh}
        />
      )}
      {editing && (
        <SecretDialog
          workspaceId={workspaceId}
          secret={editing}
          onClose={() => setEditing(null)}
          onSaved={refresh}
        />
      )}

      <Dialog open={!!deleting} onOpenChange={(o) => !o && setDeleting(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {deleting?.name}?</DialogTitle>
            <DialogDescription>
              The secret is detached from all agents. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <ErrorText error={del.error} />
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleting(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={del.isPending}
              onClick={() => deleting && del.mutate(deleting.id)}
            >
              Delete secret
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}

function SecretDialog({
  workspaceId,
  secret,
  onClose,
  onSaved,
}: {
  workspaceId: string;
  secret?: Secret;
  onClose: () => void;
  onSaved: () => Promise<unknown>;
}) {
  const presets = useQuery(presetsQuery);
  const [preset, setPreset] = useState(''); // '' = custom
  const [name, setName] = useState(secret?.name ?? '');
  const [value, setValue] = useState('');
  const [hosts, setHosts] = useState(secret?.rule.hosts.join(', ') ?? '');
  const [header, setHeader] = useState(secret?.rule.header ?? '');
  const [template, setTemplate] = useState(
    secret?.rule.template ?? DEFAULT_TEMPLATE,
  );
  const custom = secret ? true : preset === '';
  const rule = (): SecretRule => ({
    hosts: parseHosts(hosts),
    header: header.trim(),
    template: template || DEFAULT_TEMPLATE,
  });

  const save = useMutation({
    mutationFn: () =>
      secret
        ? unwrap(
            api.PATCH('/secrets/{secretId}', {
              params: { path: { secretId: secret.id } },
              body: {
                name: name.trim(),
                ...(value ? { value } : {}),
                rule: rule(),
              },
            }),
          )
        : unwrap(
            api.POST('/workspaces/{workspaceId}/secrets', {
              params: { path: { workspaceId } },
              body: {
                name: name.trim(),
                value,
                ...(custom ? { rule: rule() } : { preset }),
              },
            }),
          ),
    onSuccess: async () => {
      await onSaved();
      onClose();
    },
  });

  function choosePreset(id: string) {
    setPreset(id);
    const p = presets.data?.find((x) => x.id === id);
    if (p) setName(p.name);
  }

  const valid =
    name.trim() &&
    (secret || value) &&
    (!custom || (parseHosts(hosts).length > 0 && header.trim()));

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {secret ? `Edit ${secret.name}` : 'New secret'}
          </DialogTitle>
          <DialogDescription>
            The value is stored encrypted and never shown again.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          {!secret && (
            <label className="grid gap-1 text-sm">
              Preset
              <Select
                value={preset}
                onChange={(e) => choosePreset(e.target.value)}
              >
                <option value="">Custom</option>
                {presets.data?.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </Select>
            </label>
          )}
          <label className="grid gap-1 text-sm">
            Name
            <Input
              required
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <label className="grid gap-1 text-sm">
            Value
            <Input
              type="password"
              autoComplete="off"
              placeholder={
                secret ? 'Leave empty to keep the current value' : ''
              }
              value={value}
              onChange={(e) => setValue(e.target.value)}
            />
          </label>
          {custom && (
            <>
              <label className="grid gap-1 text-sm">
                Hosts (comma separated)
                <Input
                  placeholder="api.example.com, *.example.org"
                  value={hosts}
                  onChange={(e) => setHosts(e.target.value)}
                />
              </label>
              <label className="grid gap-1 text-sm">
                Header
                <Input
                  placeholder="Authorization"
                  value={header}
                  onChange={(e) => setHeader(e.target.value)}
                />
              </label>
              <label className="grid gap-1 text-sm">
                Template
                <Input
                  className="font-mono"
                  value={template}
                  onChange={(e) => setTemplate(e.target.value)}
                />
              </label>
            </>
          )}
          <ErrorText error={save.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!valid || save.isPending}>
              {secret ? 'Save' : 'Create secret'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
