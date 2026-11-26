import { useEffect, useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  CheckCircle2,
  ExternalLink,
  HeartPulse,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Trash2,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  executorCatalogQuery,
  executorOverviewQuery,
  executorStatusQuery,
  type ExecutorAccount,
  type ExecutorApp,
  type ExecutorAuthMethod,
  type ExecutorConnectionSession,
  type ExecutorRequirement,
} from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { SettingsHeader } from '@/components/layout/SettingsLayout';
import { Badge } from '@/components/ui/badge';
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

/** Account-level Executor page: manage connected external accounts and apps. */
export function ExecutorAccountsPage() {
  const qc = useQueryClient();
  const statusQ = useQuery(executorStatusQuery);
  const canLoadOverview = Boolean(
    statusQ.data?.available && statusQ.data?.enabled,
  );
  const overviewQ = useQuery({
    ...executorOverviewQuery,
    enabled: canLoadOverview,
  });

  const [addOpen, setAddOpen] = useState(false);
  const [connectTarget, setConnectTarget] = useState<{
    app: ExecutorApp;
    req: ExecutorRequirement;
  } | null>(null);
  const [editingAccount, setEditingAccount] = useState<ExecutorAccount | null>(
    null,
  );
  const [healthByAccount, setHealthByAccount] = useState<
    Record<string, { status: string; message?: string }>
  >({});

  const invalidateAll = async () => {
    await Promise.all([
      qc.invalidateQueries({ queryKey: executorStatusQuery.queryKey }),
      qc.invalidateQueries({ queryKey: executorOverviewQuery.queryKey }),
    ]);
  };

  const startExec = useMutation({
    mutationFn: () => unwrap(api.POST('/me/executor/start')),
    onSuccess: invalidateAll,
  });

  const restartExec = useMutation({
    mutationFn: () => unwrap(api.POST('/me/executor/restart')),
    onSuccess: invalidateAll,
  });

  const deleteApp = useMutation({
    mutationFn: (appId: string) =>
      unwrap(
        api.DELETE('/me/executor/apps/{appId}', {
          params: { path: { appId } },
        }),
      ),
    onSuccess: invalidateAll,
  });

  const linkAccount = useMutation({
    mutationFn: (p: {
      appId: string;
      scope: 'user' | 'app';
      requirement: string;
      accountId: string;
    }) =>
      unwrap(
        api.PUT(
          '/me/executor/apps/{appId}/accounts/{scope}/{requirement}/{accountId}',
          {
            params: {
              path: {
                appId: p.appId,
                scope: p.scope,
                requirement: p.requirement,
                accountId: p.accountId,
              },
            },
          },
        ),
      ),
    onSuccess: invalidateAll,
  });

  const unlinkAccount = useMutation({
    mutationFn: (p: {
      appId: string;
      scope: 'user' | 'app';
      requirement: string;
      accountId: string;
    }) =>
      unwrap(
        api.DELETE(
          '/me/executor/apps/{appId}/accounts/{scope}/{requirement}/{accountId}',
          {
            params: {
              path: {
                appId: p.appId,
                scope: p.scope,
                requirement: p.requirement,
                accountId: p.accountId,
              },
            },
          },
        ),
      ),
    onSuccess: invalidateAll,
  });

  const deleteAccount = useMutation({
    mutationFn: (accountId: string) =>
      unwrap(
        api.DELETE('/me/executor/accounts/{accountId}', {
          params: { path: { accountId } },
        }),
      ),
    onSuccess: invalidateAll,
  });

  const checkHealth = useMutation({
    mutationFn: (accountId: string) =>
      unwrap(
        api.POST('/me/executor/accounts/{accountId}/health', {
          params: { path: { accountId } },
        }),
      ),
    onSuccess: (res, accountId) => {
      setHealthByAccount((prev) => ({
        ...prev,
        [accountId]: { status: res.status, message: res.message },
      }));
      void qc.invalidateQueries({ queryKey: executorOverviewQuery.queryKey });
    },
  });

  const status = overviewQ.data?.status ?? statusQ.data;
  const accounts = overviewQ.data?.accounts ?? [];
  const apps = overviewQ.data?.apps ?? [];

  return (
    <>
      <SettingsHeader
        title="Accounts"
        description="Connect external apps and accounts once with Executor. Every enabled agent uses them over MCP without credentials ever entering its sandbox."
      />
      {statusQ.isPending && <Loading />}
      <ErrorText error={statusQ.error} />
      {status && (
        <div className="grid max-w-4xl gap-6">
          <section
            aria-label="Executor runtime"
            className="flex flex-col gap-3 rounded-lg border p-4"
          >
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="text-sm font-semibold">Executor runtime</h2>
                <StatusBadge
                  enabled={status.enabled}
                  available={status.available}
                  running={status.running}
                />
                {status.version && (
                  <code className="text-xs text-muted-foreground">
                    {status.version}
                  </code>
                )}
              </div>
              <div className="flex flex-wrap items-center gap-2">
                {status.available && status.enabled && !status.running && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={startExec.isPending}
                    onClick={() => startExec.mutate()}
                  >
                    <Play className="size-3.5" aria-hidden />
                    Start
                  </Button>
                )}
                {status.available && status.enabled && status.running && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={restartExec.isPending}
                    onClick={() => restartExec.mutate()}
                  >
                    <RefreshCw className="size-3.5" aria-hidden />
                    Restart
                  </Button>
                )}
                {status.dashboardUrl && status.available && status.enabled && (
                  <Button asChild size="sm" variant="outline">
                    <a
                      href={status.dashboardUrl}
                      target="_blank"
                      rel="noreferrer"
                    >
                      Open Executor dashboard
                      <ExternalLink className="size-3.5" aria-hidden />
                    </a>
                  </Button>
                )}
              </div>
            </div>
            {status.reason && (
              <p className="text-sm text-muted-foreground">{status.reason}</p>
            )}
            {status.available && status.enabled && !status.oauthReady && (
              <p className="text-xs text-muted-foreground">
                API-key and token accounts can be connected directly below.
                Configure an HTTPS preview domain (
                <code>preview.host_suffix</code>) or{' '}
                <code>executor.public_url</code> to enable OAuth2 provider
                sign-in and the full Executor dashboard.
              </p>
            )}
            <ErrorText error={startExec.error ?? restartExec.error} />
          </section>

          {canLoadOverview && (
            <>
              {overviewQ.isPending && (
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Loading />
                  <span>Starting your isolated Executor instance…</span>
                </div>
              )}
              <ErrorText
                error={
                  overviewQ.error ??
                  deleteApp.error ??
                  linkAccount.error ??
                  unlinkAccount.error ??
                  deleteAccount.error ??
                  checkHealth.error
                }
              />

              {overviewQ.data && (
                <>
                  <section
                    aria-label="Connected accounts"
                    className="flex flex-col gap-3"
                  >
                    <div className="flex items-center justify-between">
                      <div>
                        <h2 className="text-base font-semibold">
                          Connected accounts
                        </h2>
                        <p className="text-sm text-muted-foreground">
                          Saved credentials live encrypted in your private
                          Executor vault on the controller.
                        </p>
                      </div>
                    </div>
                    <ul
                      aria-label="Saved accounts"
                      className="divide-y rounded-lg border"
                    >
                      {accounts.length === 0 && (
                        <li className="p-4 text-sm text-muted-foreground">
                          No accounts connected yet. Connect one from an
                          installed app below.
                        </li>
                      )}
                      {accounts.map((acc) => {
                        const health = healthByAccount[acc.id];
                        return (
                          <li
                            key={acc.id}
                            className="flex flex-col gap-2 p-4 sm:flex-row sm:items-start sm:justify-between"
                          >
                            <div className="grid gap-1">
                              <div className="flex flex-wrap items-center gap-2">
                                <span className="font-medium">
                                  {acc.label || acc.providerName}
                                </span>
                                <Badge variant="outline">
                                  {acc.providerName}
                                </Badge>
                                <AccountStatusBadge status={acc.status} />
                                {acc.method && (
                                  <Badge variant="secondary">
                                    {acc.method}
                                  </Badge>
                                )}
                              </div>
                              {acc.identity && (
                                <span className="text-xs text-muted-foreground">
                                  {acc.identity}
                                </span>
                              )}
                              {acc.description && (
                                <p className="text-sm text-muted-foreground">
                                  {acc.description}
                                </p>
                              )}
                              {acc.linkedApps.length > 0 && (
                                <span className="text-xs text-muted-foreground">
                                  Linked to: {acc.linkedApps.join(', ')}
                                </span>
                              )}
                              {health && (
                                <span
                                  role="status"
                                  className="inline-flex items-center gap-1 text-xs text-muted-foreground"
                                >
                                  <CheckCircle2
                                    className="size-3.5"
                                    aria-hidden
                                  />
                                  Health: {health.status}
                                  {health.message ? ` — ${health.message}` : ''}
                                </span>
                              )}
                            </div>
                            <div className="flex shrink-0 flex-wrap items-center gap-1">
                              <Button
                                size="sm"
                                variant="ghost"
                                disabled={checkHealth.isPending}
                                onClick={() => checkHealth.mutate(acc.id)}
                              >
                                <HeartPulse className="size-3.5" aria-hidden />
                                Check
                              </Button>
                              <Button
                                size="sm"
                                variant="ghost"
                                onClick={() => setEditingAccount(acc)}
                              >
                                <Pencil className="size-3.5" aria-hidden />
                                Edit
                              </Button>
                              <Button
                                size="sm"
                                variant="ghost"
                                disabled={deleteAccount.isPending}
                                onClick={() => {
                                  if (
                                    confirm(
                                      `Disconnect ${acc.label || acc.providerName}? Agents will no longer be able to use it.`,
                                    )
                                  )
                                    deleteAccount.mutate(acc.id);
                                }}
                              >
                                <Trash2 className="size-3.5" aria-hidden />
                                Disconnect
                              </Button>
                            </div>
                          </li>
                        );
                      })}
                    </ul>
                  </section>

                  <section
                    aria-label="Executor apps"
                    className="flex flex-col gap-3"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <div>
                        <h2 className="text-base font-semibold">Apps</h2>
                        <p className="text-sm text-muted-foreground">
                          Install apps from the Executor catalog or import any
                          remote MCP server, then link your accounts.
                        </p>
                      </div>
                      <Button size="sm" onClick={() => setAddOpen(true)}>
                        <Plus className="size-4" aria-hidden />
                        Add app
                      </Button>
                    </div>
                    <ul aria-label="Installed apps" className="grid gap-3">
                      {apps.length === 0 && (
                        <li className="rounded-lg border p-4 text-sm text-muted-foreground">
                          No apps installed yet. Click Add app to install from
                          the Executor catalog.
                        </li>
                      )}
                      {apps.map((app) => (
                        <li
                          key={app.id}
                          className="flex flex-col gap-3 rounded-lg border p-4"
                        >
                          <div className="flex flex-wrap items-center justify-between gap-2">
                            <div className="flex flex-wrap items-center gap-2">
                              <span className="font-medium">{app.name}</span>
                              <code className="text-xs text-muted-foreground">
                                {app.slug}
                              </code>
                              {app.builtin && (
                                <Badge variant="secondary">built-in</Badge>
                              )}
                            </div>
                            {!app.builtin && (
                              <Button
                                size="sm"
                                variant="ghost"
                                disabled={deleteApp.isPending}
                                onClick={() => {
                                  if (
                                    confirm(
                                      `Remove ${app.name} from Executor? Your saved accounts are kept.`,
                                    )
                                  )
                                    deleteApp.mutate(app.id);
                                }}
                              >
                                Remove
                              </Button>
                            )}
                          </div>

                          {app.requirements.length === 0 && (
                            <p className="text-xs text-muted-foreground">
                              No external accounts required.
                            </p>
                          )}

                          {app.requirements.map((req) => {
                            const matching = accounts.filter(
                              (a) => a.providerId === req.providerId,
                            );
                            const linkedSet = new Set(req.linkedAccountIds);
                            return (
                              <div
                                key={`${req.scope}:${req.name}`}
                                className="flex flex-col gap-2 rounded-md border bg-muted/20 p-3"
                              >
                                <div className="flex flex-wrap items-center justify-between gap-2">
                                  <div className="flex flex-wrap items-center gap-2 text-sm">
                                    <span className="font-medium">
                                      {req.providerName}
                                    </span>
                                    <Badge variant="outline">{req.scope}</Badge>
                                    {req.linkedAccountIds.length === 0 && (
                                      <span className="text-xs text-muted-foreground">
                                        Not connected
                                      </span>
                                    )}
                                  </div>
                                  {!app.builtin && (
                                    <Button
                                      size="sm"
                                      variant="outline"
                                      onClick={() =>
                                        setConnectTarget({ app, req })
                                      }
                                    >
                                      Connect account
                                    </Button>
                                  )}
                                </div>
                                {matching.length > 0 && (
                                  <div className="flex flex-wrap gap-2">
                                    {matching.map((acc) => {
                                      const linked = linkedSet.has(acc.id);
                                      return (
                                        <Button
                                          key={acc.id}
                                          size="sm"
                                          variant={
                                            linked ? 'default' : 'outline'
                                          }
                                          disabled={
                                            linkAccount.isPending ||
                                            unlinkAccount.isPending
                                          }
                                          onClick={() => {
                                            const payload = {
                                              appId: app.id,
                                              scope: req.scope,
                                              requirement: req.name,
                                              accountId: acc.id,
                                            };
                                            if (linked) {
                                              unlinkAccount.mutate(payload);
                                            } else {
                                              linkAccount.mutate(payload);
                                            }
                                          }}
                                        >
                                          {linked ? 'Linked: ' : 'Link: '}
                                          {acc.label || acc.providerName}
                                        </Button>
                                      );
                                    })}
                                  </div>
                                )}
                              </div>
                            );
                          })}
                        </li>
                      ))}
                    </ul>
                  </section>
                </>
              )}
            </>
          )}
        </div>
      )}

      {addOpen && (
        <AddAppDialog
          open={addOpen}
          installedSlugs={new Set(apps.map((a) => a.slug))}
          customAppsEnabled={Boolean(status?.customAppsEnabled)}
          onClose={() => setAddOpen(false)}
          onInstalled={() => {
            setAddOpen(false);
            void invalidateAll();
          }}
        />
      )}

      {connectTarget && (
        <ConnectAccountDialog
          app={connectTarget.app}
          requirement={connectTarget.req}
          oauthReady={Boolean(status?.oauthReady)}
          onClose={() => setConnectTarget(null)}
          onConnected={() => {
            setConnectTarget(null);
            void invalidateAll();
          }}
        />
      )}

      {editingAccount && (
        <EditAccountDialog
          account={editingAccount}
          onClose={() => setEditingAccount(null)}
          onSaved={() => {
            setEditingAccount(null);
            void invalidateAll();
          }}
        />
      )}
    </>
  );
}

function StatusBadge({
  enabled,
  available,
  running,
}: {
  enabled: boolean;
  available: boolean;
  running: boolean;
}) {
  if (!enabled) return <Badge variant="secondary">Disabled</Badge>;
  if (!available)
    return (
      <Badge variant="outline" className="border-destructive text-destructive">
        Unavailable
      </Badge>
    );
  if (running) return <Badge variant="default">Running</Badge>;
  return <Badge variant="outline">Stopped</Badge>;
}

function AccountStatusBadge({ status }: { status: string }) {
  switch (status) {
    case 'ready':
      return <Badge variant="default">ready</Badge>;
    case 'needs_reauth':
      return (
        <Badge
          variant="outline"
          className="border-destructive text-destructive"
        >
          needs reauth
        </Badge>
      );
    case 'error':
      return (
        <Badge
          variant="outline"
          className="border-destructive text-destructive"
        >
          error
        </Badge>
      );
    default:
      return <Badge variant="secondary">{status}</Badge>;
  }
}

function AddAppDialog({
  open,
  installedSlugs,
  customAppsEnabled,
  onClose,
  onInstalled,
}: {
  open: boolean;
  installedSlugs: Set<string>;
  customAppsEnabled: boolean;
  onClose: () => void;
  onInstalled: () => void;
}) {
  const [tab, setTab] = useState<'catalog' | 'custom'>('catalog');
  const [query, setQuery] = useState('');
  const [customName, setCustomName] = useState('');
  const [customUrl, setCustomUrl] = useState('');
  const catalog = useQuery(executorCatalogQuery);

  const importCatalog = useMutation({
    mutationFn: (entry: { id: string; name: string }) =>
      unwrap(
        api.POST('/me/executor/catalog/import', {
          body: { entry: entry.id, name: entry.name },
        }),
      ),
    onSuccess: onInstalled,
  });

  const importCustom = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/me/executor/apps/import', {
          body: { name: customName.trim(), url: customUrl.trim() },
        }),
      ),
    onSuccess: onInstalled,
  });

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = catalog.data?.apps ?? [];
    if (!q) return list;
    return list.filter(
      (e) =>
        e.name.toLowerCase().includes(q) ||
        e.id.toLowerCase().includes(q) ||
        (e.description ?? '').toLowerCase().includes(q) ||
        (e.category ?? '').toLowerCase().includes(q),
    );
  }, [catalog.data, query]);

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Add Executor app</DialogTitle>
          <DialogDescription>
            Install an app from the official Executor catalog or import a remote
            MCP server.
          </DialogDescription>
        </DialogHeader>
        <div className="flex gap-2">
          <Button
            size="sm"
            variant={tab === 'catalog' ? 'default' : 'outline'}
            onClick={() => setTab('catalog')}
          >
            Catalog
          </Button>
          <Button
            size="sm"
            variant={tab === 'custom' ? 'default' : 'outline'}
            onClick={() => setTab('custom')}
          >
            Custom MCP server
          </Button>
        </div>

        {tab === 'catalog' ? (
          <div className="grid gap-3">
            <Input
              aria-label="Search catalog"
              placeholder="Search apps (GitHub, Linear, Slack, Google…)"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            {catalog.isPending && <Loading />}
            <ErrorText error={catalog.error ?? importCatalog.error} />
            <ul
              aria-label="Catalog apps"
              className="max-h-72 divide-y overflow-y-auto rounded-md border"
            >
              {filtered.length === 0 && !catalog.isPending && (
                <li className="p-3 text-sm text-muted-foreground">
                  No matching catalog apps.
                </li>
              )}
              {filtered.map((entry) => {
                const already = installedSlugs.has(entry.id);
                return (
                  <li
                    key={entry.id}
                    className="flex items-center justify-between gap-3 p-3"
                  >
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-medium">{entry.name}</span>
                        {entry.category && (
                          <Badge variant="outline">{entry.category}</Badge>
                        )}
                      </div>
                      {entry.description && (
                        <p className="truncate text-xs text-muted-foreground">
                          {entry.description}
                        </p>
                      )}
                    </div>
                    <Button
                      size="sm"
                      variant={already ? 'secondary' : 'outline'}
                      disabled={importCatalog.isPending}
                      onClick={() =>
                        importCatalog.mutate({ id: entry.id, name: entry.name })
                      }
                    >
                      {already ? 'Install another' : 'Install'}
                    </Button>
                  </li>
                );
              })}
            </ul>
          </div>
        ) : !customAppsEnabled ? (
          <div className="grid gap-3">
            <p className="text-sm text-muted-foreground">
              Custom MCP server imports are disabled on this server because
              Executor&apos;s app runtime can reach the controller&apos;s
              network. Install apps from the official Catalog tab, or set{' '}
              <code>executor.allow_private_fetch: true</code> in{' '}
              <code>config.yaml</code>.
            </p>
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={onClose}>
                Close
              </Button>
            </DialogFooter>
          </div>
        ) : (
          <form
            className="grid gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              importCustom.mutate();
            }}
          >
            <label className="grid gap-1 text-sm">
              Display name
              <Input
                value={customName}
                onChange={(e) => setCustomName(e.target.value)}
                placeholder="Internal Docs"
                required
              />
            </label>
            <label className="grid gap-1 text-sm">
              MCP server URL
              <Input
                type="url"
                value={customUrl}
                onChange={(e) => setCustomUrl(e.target.value)}
                placeholder="https://mcp.example.com/mcp"
                required
              />
            </label>
            <ErrorText error={importCustom.error} />
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button type="submit" disabled={importCustom.isPending}>
                Import
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

function ConnectAccountDialog({
  app,
  requirement,
  oauthReady,
  onClose,
  onConnected,
}: {
  app: ExecutorApp;
  requirement: ExecutorRequirement;
  oauthReady: boolean;
  onClose: () => void;
  onConnected: () => void;
}) {
  const [session, setSession] = useState<ExecutorConnectionSession | null>(
    null,
  );
  const [selectedMethod, setSelectedMethod] = useState('');
  const [label, setLabel] = useState('');
  const [description, setDescription] = useState('');
  const [fields, setFields] = useState<Record<string, string>>({});

  const startSession = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST(
          '/me/executor/apps/{appId}/accounts/{scope}/{requirement}/connect',
          {
            params: {
              path: {
                appId: app.id,
                scope: requirement.scope,
                requirement: requirement.name,
              },
            },
            body: {},
          },
        ),
      ),
    onSuccess: (s) => {
      setSession(s);
      const methods =
        (s.methods?.length ?? 0) > 0 ? s.methods : requirement.authMethods;
      const preferred =
        methods.find((m) => (oauthReady ? true : m.kind === 'secrets')) ??
        methods[0];
      if (preferred) setSelectedMethod(preferred.name);
    },
  });

  useEffect(() => {
    startSession.mutate();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [app.id, requirement.scope, requirement.name]);

  const submitSecrets = useMutation({
    mutationFn: () => {
      if (!session) throw new Error('Connection session not ready');
      return unwrap(
        api.POST('/me/executor/connections/submit', {
          body: {
            connectionId: session.connectionId,
            token: session.token,
            method: selectedMethod,
            label: label.trim() || undefined,
            description: description.trim() || undefined,
            fields,
          },
        }),
      );
    },
    onSuccess: onConnected,
  });

  const methods: ExecutorAuthMethod[] =
    (session?.methods?.length ?? 0) > 0
      ? session!.methods
      : requirement.authMethods;
  const activeMethod =
    methods.find((m) => m.name === selectedMethod) ?? methods[0];

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Connect {requirement.providerName}</DialogTitle>
          <DialogDescription>
            Credentials are stored encrypted in Executor and never enter your
            agent sandboxes.
          </DialogDescription>
        </DialogHeader>

        {startSession.isPending && <Loading />}
        <ErrorText error={startSession.error} />

        {session && (
          <div className="grid gap-3">
            {methods.length > 1 && (
              <label className="grid gap-1 text-sm">
                Authentication method
                <Select
                  aria-label="Authentication method"
                  value={activeMethod?.name ?? ''}
                  onChange={(e) => {
                    setSelectedMethod(e.target.value);
                    setFields({});
                  }}
                >
                  {methods.map((m) => (
                    <option key={m.name} value={m.name}>
                      {m.label || m.name} ({m.kind})
                    </option>
                  ))}
                </Select>
              </label>
            )}

            {activeMethod?.kind === 'secrets' ? (
              <form
                className="grid gap-3"
                onSubmit={(e) => {
                  e.preventDefault();
                  submitSecrets.mutate();
                }}
              >
                {activeMethod.guide && (
                  <p className="rounded-md bg-muted/40 p-2.5 text-xs text-muted-foreground">
                    {activeMethod.guide}
                  </p>
                )}
                <label className="grid gap-1 text-sm">
                  Label (optional)
                  <Input
                    value={label}
                    onChange={(e) => setLabel(e.target.value)}
                    placeholder={`Personal ${requirement.providerName}`}
                  />
                </label>
                <label className="grid gap-1 text-sm">
                  Description for agents (optional)
                  <Input
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                    placeholder="Use for work repositories"
                  />
                </label>
                {(activeMethod.fields ?? []).map((f) => (
                  <label key={f.name} className="grid gap-1 text-sm">
                    <span>
                      {f.title || f.name}
                      {f.required ? ' *' : ''}
                    </span>
                    <Input
                      type={f.secret === false ? 'text' : 'password'}
                      value={fields[f.name] ?? ''}
                      onChange={(e) =>
                        setFields((prev) => ({
                          ...prev,
                          [f.name]: e.target.value,
                        }))
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
                <ErrorText error={submitSecrets.error} />
                <DialogFooter>
                  <Button type="button" variant="ghost" onClick={onClose}>
                    Cancel
                  </Button>
                  <Button type="submit" disabled={submitSecrets.isPending}>
                    Save account
                  </Button>
                </DialogFooter>
              </form>
            ) : (
              <div className="grid gap-3">
                {oauthReady && session.browserUrl ? (
                  <>
                    <p className="text-sm text-muted-foreground">
                      Complete sign-in with {requirement.providerName} in your
                      signed-in Executor dashboard, then return here.
                    </p>
                    <div className="flex flex-wrap items-center gap-2">
                      <Button asChild>
                        <a
                          href={session.browserUrl}
                          target="_blank"
                          rel="noreferrer"
                        >
                          Continue in Executor
                          <ExternalLink className="size-3.5" aria-hidden />
                        </a>
                      </Button>
                      <Button variant="outline" onClick={onConnected}>
                        I finished connecting
                      </Button>
                    </div>
                  </>
                ) : (
                  <p className="text-sm text-muted-foreground">
                    OAuth2 sign-in requires an HTTPS Executor dashboard origin (
                    <code>preview.host_suffix</code> or{' '}
                    <code>executor.public_url</code>). Configure HTTPS or choose
                    a token/API-key method if available.
                  </p>
                )}
              </div>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function EditAccountDialog({
  account,
  onClose,
  onSaved,
}: {
  account: ExecutorAccount;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [label, setLabel] = useState(account.label);
  const [description, setDescription] = useState(account.description ?? '');
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/me/executor/accounts/{accountId}', {
          params: { path: { accountId: account.id } },
          body: { label: label.trim(), description: description.trim() },
        }),
      ),
    onSuccess: onSaved,
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Edit account</DialogTitle>
          <DialogDescription>
            Update the label or agent-facing description for{' '}
            {account.providerName}.
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
            Label
            <Input
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              required
            />
          </label>
          <label className="grid gap-1 text-sm">
            Description for agents
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="When agents should pick this account"
            />
          </label>
          <ErrorText error={save.error} />
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending}>
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
