import { lazy, Suspense, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import {
  Activity,
  Bot,
  CircleAlert,
  ExternalLink,
  FolderOpen,
  Gauge,
  KeyRound,
  Settings2,
  Share2,
  SquareTerminal,
  type LucideIcon,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  appQuery,
  appsQuery,
  isOwner as ownerOf,
  sandboxLimitsQuery,
  type App,
} from '@/api/queries';
import { ActivityTab } from '@/components/ActivityTab';
import { AgentSecretsTab } from '@/components/AgentSecretsTab';
import { AgentStateBadge, AgentStateDot } from '@/components/AgentStateBadge';
import { FilesTab } from '@/components/FilesTab';
import { PreviewTab } from '@/components/PreviewTab';
import { ServicesTab } from '@/components/ServicesTab';
import { SharingTab } from '@/components/SharingTab';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { AgentLoadError } from '@/components/agent/AgentLoadError';
import {
  SettingsHeader,
  SettingsLayout,
  SettingsNavItem,
} from '@/components/layout/SettingsLayout';
import { Crumb, TopBar } from '@/components/layout/TopBar';

const TerminalTab = lazy(() =>
  import('@/components/TerminalTab').then((m) => ({ default: m.TerminalTab })),
);

export type AppSection =
  | 'overview'
  | 'files'
  | 'terminal'
  | 'secrets'
  | 'sharing'
  | 'settings'
  | 'activity';

const SECTIONS: {
  id: AppSection;
  label: string;
  hint: string;
  icon: LucideIcon;
  ownerOnly?: boolean;
}[] = [
  {
    id: 'overview',
    label: 'Overview',
    hint: 'State, URLs and the service',
    icon: Gauge,
  },
  {
    id: 'files',
    label: 'Files',
    hint: 'Browse the home directory',
    icon: FolderOpen,
  },
  {
    id: 'terminal',
    label: 'Terminal',
    hint: 'A shell in the app machine',
    icon: SquareTerminal,
  },
  {
    id: 'secrets',
    label: 'Secrets',
    hint: 'Credentials the egress proxy injects for this app',
    icon: KeyRound,
  },
  { id: 'sharing', label: 'Sharing', hint: 'Who can use it', icon: Share2 },
  {
    id: 'settings',
    label: 'Settings',
    hint: 'Resources, sleep, delete',
    icon: Settings2,
    ownerOnly: true,
  },
  {
    id: 'activity',
    label: 'Activity',
    hint: 'Audit log',
    icon: Activity,
    ownerOnly: true,
  },
];

export const isAppSection = (v: unknown): v is AppSection =>
  SECTIONS.some((s) => s.id === v);

/** An app: a machine without a harness that runs services behind URLs. */
export function AppPage({
  appId,
  tab = 'overview',
}: {
  appId: string;
  tab?: AppSection;
}) {
  const q = useQuery(appQuery(appId));
  if (q.isPending)
    return (
      <>
        <TopBar />
        <Loading />
      </>
    );
  if (q.error)
    return (
      <>
        <TopBar />
        <AgentLoadError error={q.error} />
      </>
    );
  const app = q.data;
  const owner = ownerOf(app);
  const sections = SECTIONS.filter((s) => owner || !s.ownerOnly);
  const current = sections.find((s) => s.id === tab) ?? sections[0];
  return (
    <SettingsLayout
      navLabel="App"
      crumbs={
        <Crumb current>
          <AgentStateDot state={app.state} />
          {app.name}
        </Crumb>
      }
      actions={
        app.url ? (
          <Button asChild variant="ghost" size="sm">
            <a href={app.url} target="_blank" rel="noreferrer">
              <ExternalLink className="size-4" />
              Open
            </a>
          </Button>
        ) : undefined
      }
      nav={sections.map((s) => (
        <SettingsNavItem key={s.id} active={s.id === current.id}>
          <Link to="/apps/$appId" params={{ appId }} search={{ tab: s.id }}>
            <s.icon className="size-4 shrink-0" aria-hidden />
            {s.label}
          </Link>
        </SettingsNavItem>
      ))}
    >
      <SettingsHeader title={current.label} description={current.hint} />
      <Section app={app} section={current.id} />
    </SettingsLayout>
  );
}

function useAppActions(app: App) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const path = { params: { path: { appId: app.id } } };
  const refresh = () => qc.invalidateQueries({ queryKey: appsQuery.queryKey });
  const action = useMutation({
    mutationFn: (kind: 'start' | 'stop' | 'rebuild') =>
      unwrap(
        kind === 'start'
          ? api.POST('/apps/{appId}/start', path)
          : kind === 'stop'
            ? api.POST('/apps/{appId}/stop', path)
            : api.POST('/apps/{appId}/rebuild', path),
      ),
    onSuccess: async (a) => {
      qc.setQueryData(['apps', app.id], a);
      await refresh();
    },
  });
  const remove = useMutation({
    mutationFn: () => unwrap(api.DELETE('/apps/{appId}', path)),
    onSuccess: async () => {
      qc.removeQueries({ queryKey: ['apps', app.id] });
      await refresh();
      await navigate({ to: '/' });
    },
  });
  return { action, remove, busy: action.isPending || remove.isPending };
}

function Section({ app, section }: { app: App; section: AppSection }) {
  const owner = ownerOf(app);
  const { action, remove, busy } = useAppActions(app);
  switch (section) {
    case 'overview':
      return (
        <div className="grid gap-8">
          <div className="grid gap-3">
            <div className="flex flex-wrap items-center gap-2">
              <AgentStateBadge state={app.state} sleepsAt={app.sleepsAt} />
              {app.idleMinutes === 0 && (
                <span className="text-xs text-muted-foreground">Always on</span>
              )}
              {app.createdByAgent && (
                <span className="flex items-center gap-1 text-xs text-muted-foreground">
                  <Bot className="size-3.5" aria-hidden />
                  created by{' '}
                  {app.createdByAgent.name ? (
                    <Link
                      to="/a/$agentId"
                      params={{ agentId: app.createdByAgent.id }}
                      className="hover:underline"
                    >
                      {app.createdByAgent.name}
                    </Link>
                  ) : (
                    'a deleted agent'
                  )}
                </span>
              )}
            </div>
            {app.state === 'error' && app.stateError && (
              <p
                role="alert"
                className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/5 p-2.5 text-sm text-destructive"
              >
                <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
                {app.stateError}
              </p>
            )}
            <div className="flex flex-wrap gap-2">
              {(app.state === 'sleeping' || app.state === 'error') && (
                <Button disabled={busy} onClick={() => action.mutate('start')}>
                  Start
                </Button>
              )}
              {app.state === 'running' && (
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() => action.mutate('stop')}
                >
                  Stop
                </Button>
              )}
              {owner && app.needsRebuild && (
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() => action.mutate('rebuild')}
                >
                  Reset system to apply resources
                </Button>
              )}
            </div>
            <ErrorText error={action.error} />
          </div>
          <section className="grid gap-2">
            <h2 className="text-sm font-semibold">Services</h2>
            <ServicesTab agentId={app.id} isOwner={owner} allowAdd={owner} />
          </section>
          <section className="grid gap-2">
            <h2 className="text-sm font-semibold">URLs</h2>
            <PreviewTab agentId={app.id} isOwner={owner} />
          </section>
        </div>
      );
    case 'files':
      return (
        <>
          <FilesTab
            agentId={app.id}
            canUse
            starting={busy}
            onStart={() => action.mutate('start')}
          />
          <ErrorText error={action.error} />
        </>
      );
    case 'terminal':
      return (
        <Suspense fallback={<Loading />}>
          <TerminalTab agentId={app.id} />
        </Suspense>
      );
    case 'secrets':
      return <AgentSecretsTab agentId={app.id} isOwner={owner} />;
    case 'sharing':
      return <SharingTab agentId={app.id} isOwner={owner} />;
    case 'settings':
      return owner ? (
        <div className="grid gap-8">
          <AppSettingsForm key={app.updatedAt} app={app} />
          <section className="grid gap-2 rounded-lg border border-destructive/30 p-4">
            <h2 className="text-sm font-semibold">Delete app</h2>
            <p className="text-sm text-muted-foreground">
              Destroys the machine, its files and its URLs.
            </p>
            <div>
              <Button
                variant="destructive"
                disabled={busy}
                onClick={() => {
                  if (confirm(`Delete ${app.name}?`)) remove.mutate();
                }}
              >
                Delete
              </Button>
            </div>
            <ErrorText error={remove.error} />
          </section>
        </div>
      ) : null;
    case 'activity':
      return owner ? <ActivityTab agentId={app.id} /> : null;
  }
}

function AppSettingsForm({ app }: { app: App }) {
  const qc = useQueryClient();
  const limits = useQuery(sandboxLimitsQuery).data;
  const [name, setName] = useState(app.name);
  const [cpus, setCpus] = useState(String(app.cpus));
  const [memory, setMemory] = useState(String(app.memoryMb));
  const [alwaysOn, setAlwaysOn] = useState(app.idleMinutes === 0);
  const [idle, setIdle] = useState(String(app.idleMinutes || 30));
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/apps/{appId}', {
          params: { path: { appId: app.id } },
          body: {
            name,
            cpus: Number(cpus),
            memoryMb: Number(memory),
            idleMinutes: alwaysOn ? 0 : Number(idle),
          },
        }),
      ),
    onSuccess: (a) => qc.setQueryData(['apps', app.id], a),
  });
  return (
    <form
      className="grid gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <label className="grid gap-1 text-sm">
        Name
        <Input value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="grid gap-1 text-sm">
          vCPUs
          <Input
            inputMode="decimal"
            value={cpus}
            onChange={(e) => setCpus(e.target.value)}
          />
        </label>
        <label className="grid gap-1 text-sm">
          Memory (MB)
          <Input
            inputMode="numeric"
            value={memory}
            onChange={(e) => setMemory(e.target.value)}
          />
          {limits && (
            <span className="text-xs text-muted-foreground">
              {limits.minMemoryMb} to {limits.maxMemoryMb} MB
            </span>
          )}
        </label>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={alwaysOn}
          onChange={(e) => setAlwaysOn(e.target.checked)}
        />
        Always on (never sleeps when idle)
      </label>
      {!alwaysOn && (
        <label className="grid gap-1 text-sm">
          Sleep after idle (minutes)
          <Input
            inputMode="numeric"
            value={idle}
            onChange={(e) => setIdle(e.target.value)}
          />
          <span className="text-xs text-muted-foreground">
            A sleeping app wakes when someone allowed to wake it opens its URL.
          </span>
        </label>
      )}
      <div>
        <Button type="submit" disabled={save.isPending}>
          Save
        </Button>
      </div>
      <ErrorText error={save.error} />
    </form>
  );
}
