import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ExternalLink,
  Pencil,
  Plus,
  Search,
  Store,
  Terminal,
  Trash2,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  acpRegistryQuery,
  customHarnessesQuery,
  harnessesQuery,
  type AcpRegistryAgent,
  type CustomHarness,
  type CustomHarnessInput,
  type CustomHarnessPatch,
} from '@/api/queries';
import { HarnessIcon } from '@/components/HarnessIcon';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';

function parseLines(text: string): string[] {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean);
}

function parseEnvLines(text: string): {
  env: Record<string, string>;
  error?: string;
} {
  const env: Record<string, string> = {};
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (!line) continue;
    const eq = line.indexOf('=');
    if (eq <= 0) {
      return { env, error: `Invalid env line "${line}": expected KEY=VALUE` };
    }
    const key = line.slice(0, eq).trim();
    const val = line.slice(eq + 1);
    env[key] = val;
  }
  return { env };
}

function formatEnvLines(env: Record<string, string> | undefined): string {
  if (!env) return '';
  return Object.entries(env)
    .map(([k, v]) => `${k}=${v}`)
    .join('\n');
}

export function AcpStoreSection() {
  const qc = useQueryClient();
  const custom = useQuery(customHarnessesQuery);
  const [storeOpen, setStoreOpen] = useState(false);
  const [localOpen, setLocalOpen] = useState(false);
  const [editing, setEditing] = useState<CustomHarness | null>(null);
  const [removeError, setRemoveError] = useState<Error | null>(null);

  const invalidate = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: customHarnessesQuery.queryKey }),
      qc.invalidateQueries({ queryKey: harnessesQuery.queryKey }),
      qc.invalidateQueries({ queryKey: ['acp-registry'] }),
    ]);

  const remove = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/me/harnesses/{harnessId}', {
          params: { path: { harnessId: id } },
        }),
      ),
    onMutate: () => setRemoveError(null),
    onError: (err) =>
      setRemoveError(err instanceof Error ? err : new Error(String(err))),
    onSuccess: invalidate,
  });

  const items = custom.data ?? [];

  return (
    <section
      aria-label="ACP Store and custom harnesses"
      className="grid gap-4 border-t pt-6"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid gap-1">
          <h2 className="text-lg font-semibold">
            ACP Store &amp; Custom Harnesses
          </h2>
          <p className="text-sm text-muted-foreground">
            Install additional coding agents from the official ACP Registry or
            add your own local ACP command. Registry agents install on demand
            into each agent&apos;s persistent home directory—no image rebuild
            required.
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" onClick={() => setStoreOpen(true)}>
            <Store className="mr-1.5 size-4" aria-hidden />
            Browse ACP Store
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setLocalOpen(true)}
          >
            <Terminal className="mr-1.5 size-4" aria-hidden />
            Add local ACP
          </Button>
        </div>
      </div>

      {custom.isPending && <Loading />}
      <ErrorText error={removeError} />

      {!custom.isPending && items.length === 0 ? (
        <div className="rounded-lg border border-dashed p-5 text-sm text-muted-foreground">
          No custom harnesses added yet. Browse the ACP Store to add community
          ACP agents (such as Goose, Cline, Amp, or Qwen Code), or configure a
          custom ACP command already installed in your sandbox.
        </div>
      ) : (
        <div className="grid gap-3">
          {items.map((h) => (
            <div
              key={h.id}
              className="flex flex-wrap items-start gap-3 rounded-lg border p-4"
            >
              <HarnessIcon
                harness={h.id}
                iconUrl={h.iconUrl}
                className="mt-0.5 size-5"
              />
              <div className="grid min-w-0 flex-1 gap-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-semibold">{h.name}</span>
                  <Badge variant="outline">
                    {h.kind === 'registry' ? 'ACP Store' : 'Local ACP'}
                  </Badge>
                  {h.registryVersion && (
                    <span className="font-mono text-xs text-muted-foreground">
                      v{h.registryVersion}
                    </span>
                  )}
                  {h.distribution && (
                    <Badge variant="secondary">{h.distribution}</Badge>
                  )}
                </div>
                {h.description && (
                  <p className="text-sm text-muted-foreground">
                    {h.description}
                  </p>
                )}
                {h.command && (
                  <code className="truncate font-mono text-xs text-muted-foreground">
                    {[h.command, ...h.args].join(' ')}
                  </code>
                )}
              </div>
              <div className="flex shrink-0 gap-1">
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={`Edit ${h.name}`}
                  onClick={() => setEditing(h)}
                >
                  <Pencil className="size-3.5" aria-hidden />
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={`Remove ${h.name}`}
                  disabled={remove.isPending}
                  onClick={() => {
                    if (
                      confirm(
                        `Remove ${h.name}? Agents using it must be switched to another harness first.`,
                      )
                    ) {
                      remove.mutate(h.id);
                    }
                  }}
                >
                  <Trash2 className="size-3.5" aria-hidden />
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}

      {storeOpen && (
        <AcpStoreDialog
          onClose={() => setStoreOpen(false)}
          onAdded={invalidate}
        />
      )}
      {localOpen && (
        <CustomHarnessFormDialog
          onClose={() => setLocalOpen(false)}
          onSaved={invalidate}
        />
      )}
      {editing && (
        <CustomHarnessFormDialog
          existing={editing}
          onClose={() => setEditing(null)}
          onSaved={invalidate}
        />
      )}
    </section>
  );
}

function AcpStoreDialog({
  onClose,
  onAdded,
}: {
  onClose: () => void;
  onAdded: () => Promise<unknown>;
}) {
  const [query, setQuery] = useState('');
  const [addingId, setAddingId] = useState<string | null>(null);
  const results = useQuery(acpRegistryQuery(query.trim(), 50));

  const add = useMutation({
    mutationFn: (agent: AcpRegistryAgent) =>
      unwrap(
        api.POST('/me/harnesses', {
          body: {
            kind: 'registry',
            registryId: agent.id,
          },
        }),
      ),
    onMutate: (agent) => setAddingId(agent.id),
    onSettled: () => setAddingId(null),
    onSuccess: () => onAdded(),
  });

  const agents = results.data?.agents ?? [];

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="flex max-h-[85vh] max-w-2xl flex-col gap-4">
        <DialogHeader>
          <DialogTitle>ACP Store</DialogTitle>
          <DialogDescription>
            Official Agent Client Protocol Registry. Added harnesses appear in
            your agent settings and model picker rail.
          </DialogDescription>
        </DialogHeader>

        <div className="relative">
          <Search
            className="pointer-events-none absolute top-2.5 left-3 size-4 text-muted-foreground"
            aria-hidden
          />
          <Input
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search ACP Registry agents…"
            aria-label="Search ACP Registry"
            className="pl-9"
          />
        </div>

        <ErrorText error={results.error ?? add.error} />

        <div className="min-h-0 flex-1 overflow-y-auto pr-1">
          {results.isPending ? (
            <Loading />
          ) : agents.length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              No matching ACP agents found.
            </p>
          ) : (
            <div className="grid gap-2.5">
              {agents.map((a) => (
                <div
                  key={a.id}
                  className="flex items-start gap-3 rounded-lg border p-3"
                >
                  <HarnessIcon
                    harness={a.builtinId ?? a.id}
                    iconUrl={a.icon}
                    className="mt-0.5 size-6"
                  />
                  <div className="grid min-w-0 flex-1 gap-1">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="text-sm font-semibold">{a.name}</span>
                      <span className="font-mono text-xs text-muted-foreground">
                        v{a.version}
                      </span>
                      {a.builtinId && (
                        <Badge variant="secondary">Built-in</Badge>
                      )}
                      {a.addedId && <Badge>Added</Badge>}
                    </div>
                    <p className="text-xs text-muted-foreground">
                      {a.description}
                    </p>
                    <div className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
                      {a.distributions.map((d) => (
                        <span
                          key={d}
                          className="rounded bg-muted px-1.5 py-0.5 font-mono"
                        >
                          {d}
                        </span>
                      ))}
                      {a.authors && a.authors.length > 0 && (
                        <span>by {a.authors.join(', ')}</span>
                      )}
                      {a.license && <span>· {a.license}</span>}
                      {(a.website ?? a.repository) && (
                        <a
                          href={a.website ?? a.repository}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="inline-flex items-center gap-0.5 underline hover:text-foreground"
                        >
                          Docs
                          <ExternalLink className="size-2.5" aria-hidden />
                        </a>
                      )}
                    </div>
                  </div>
                  <Button
                    size="sm"
                    variant={a.addedId ? 'outline' : 'default'}
                    disabled={add.isPending || Boolean(a.addedId)}
                    onClick={() => add.mutate(a)}
                  >
                    {addingId === a.id ? (
                      'Adding…'
                    ) : a.addedId ? (
                      'Added'
                    ) : (
                      <>
                        <Plus className="mr-1 size-3.5" aria-hidden />
                        Add
                      </>
                    )}
                  </Button>
                </div>
              ))}
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function CustomHarnessFormDialog({
  existing,
  onClose,
  onSaved,
}: {
  existing?: CustomHarness;
  onClose: () => void;
  onSaved: () => Promise<unknown>;
}) {
  const isEdit = Boolean(existing);
  const isRegistry = existing?.kind === 'registry';

  const [name, setName] = useState(existing?.name ?? '');
  const [description, setDescription] = useState(existing?.description ?? '');
  const [command, setCommand] = useState(existing?.command ?? '');
  const [distribution, setDistribution] = useState<
    'auto' | 'binary' | 'npx' | 'uvx'
  >((existing?.distribution as 'binary' | 'npx' | 'uvx' | undefined) || 'auto');
  const [argsText, setArgsText] = useState((existing?.args ?? []).join('\n'));
  const [envText, setEnvText] = useState(formatEnvLines(existing?.env));
  const [modelsText, setModelsText] = useState(
    (existing?.customModels ?? []).join('\n'),
  );
  const [authMethod, setAuthMethod] = useState(existing?.authMethod ?? '');
  const [validationError, setValidationError] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: async () => {
      const parsedEnv = parseEnvLines(envText);
      if (parsedEnv.error) {
        throw new Error(parsedEnv.error);
      }
      const args = parseLines(argsText);
      const customModels = parseLines(modelsText);
      if (isEdit && existing) {
        const body: CustomHarnessPatch = {
          name: name.trim(),
          description: description.trim(),
          command: command.trim(),
          args,
          env: parsedEnv.env,
          customModels,
          authMethod: authMethod.trim(),
          ...(isRegistry ? { distribution } : {}),
        };
        return unwrap(
          api.PATCH('/me/harnesses/{harnessId}', {
            params: { path: { harnessId: existing.id } },
            body,
          }),
        );
      }
      const body: CustomHarnessInput = {
        kind: 'local',
        name: name.trim(),
        description: description.trim(),
        command: command.trim(),
        args,
        env: parsedEnv.env,
        customModels,
        authMethod: authMethod.trim(),
      };
      return unwrap(api.POST('/me/harnesses', { body }));
    },
    onMutate: () => setValidationError(null),
    onError: (err) =>
      setValidationError(err instanceof Error ? err.message : String(err)),
    onSuccess: async () => {
      await onSaved();
      onClose();
    },
  });

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {isEdit ? `Edit ${existing?.name}` : 'Add local ACP command'}
          </DialogTitle>
          <DialogDescription>
            {isRegistry
              ? 'Customize launch arguments, environment variables, distribution, or custom models for this ACP Store harness.'
              : 'Launch any ACP-compatible executable installed inside your agent sandboxes.'}
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <label className="grid gap-1 text-sm">
            Display name
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="My ACP Agent"
              required
            />
          </label>
          <label className="grid gap-1 text-sm">
            Description
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Optional short description"
            />
          </label>
          {isRegistry ? (
            <label className="grid gap-1 text-sm">
              Distribution
              <Select
                value={distribution}
                onChange={(e) =>
                  setDistribution(
                    e.target.value as 'auto' | 'binary' | 'npx' | 'uvx',
                  )
                }
              >
                <option value="auto">Auto (prefer binary → npx → uvx)</option>
                <option value="binary">Native binary</option>
                <option value="npx">npx package</option>
                <option value="uvx">uvx package</option>
              </Select>
            </label>
          ) : null}
          <label className="grid gap-1 text-sm">
            {isRegistry ? 'Command override (optional)' : 'Command executable'}
            <Input
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              placeholder={
                isRegistry
                  ? 'Leave blank to use registry installer'
                  : 'my-acp-agent'
              }
              required={!isRegistry}
            />
          </label>
          <label className="grid gap-1 text-sm">
            Extra arguments (one per line)
            <textarea
              className="min-h-16 w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              value={argsText}
              onChange={(e) => setArgsText(e.target.value)}
              placeholder="--acp"
              spellCheck={false}
            />
          </label>
          <label className="grid gap-1 text-sm">
            Environment variables (KEY=VALUE, one per line)
            <textarea
              className="min-h-16 w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              value={envText}
              onChange={(e) => setEnvText(e.target.value)}
              placeholder="LOG_LEVEL=info"
              spellCheck={false}
            />
          </label>
          <label className="grid gap-1 text-sm">
            Custom model IDs (one per line)
            <textarea
              className="min-h-16 w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              value={modelsText}
              onChange={(e) => setModelsText(e.target.value)}
              placeholder="model-id-1"
              spellCheck={false}
            />
          </label>
          <label className="grid gap-1 text-sm">
            Auto-authenticate method ID (optional)
            <Input
              value={authMethod}
              onChange={(e) => setAuthMethod(e.target.value)}
              placeholder="e.g. api-key"
            />
          </label>
          {validationError && (
            <p role="alert" className="text-sm text-destructive">
              {validationError}
            </p>
          )}
          <div className="flex justify-end gap-2 pt-1">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {isEdit ? 'Save changes' : 'Add harness'}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
