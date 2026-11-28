import { useState, type FormEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import {
  Bot,
  CheckCircle2,
  Clock,
  Copy,
  ExternalLink,
  Hand,
  Loader2,
  Pencil,
  Play,
  Plus,
  RotateCcw,
  Square,
  Terminal,
  Trash2,
  Webhook,
  Workflow as WorkflowIcon,
  XCircle,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  agentsQuery,
  workflowRunsQuery,
  workflowsQuery,
  type Agent,
  type Workflow,
  type WorkflowInput,
  type WorkflowRun,
  type WorkflowStep,
  type WorkflowStepDef,
  type WorkflowStepKind,
} from '@/api/queries';
import { SettingsHeader } from '@/components/layout/SettingsLayout';
import { Crumb, TopBar } from '@/components/layout/TopBar';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { useDocumentTitle } from '@/hooks/useDocumentTitle';
import { relTime, useNow } from '@/lib/time';
import { cn } from '@/lib/utils';

type WorkflowTab = 'needs_input' | 'running' | 'all' | 'definitions';

function runStatusBadge(status: WorkflowRun['status']) {
  switch (status) {
    case 'needs_input':
      return (
        <Badge className="bg-amber-500/15 text-amber-700 dark:text-amber-300">
          Needs input
        </Badge>
      );
    case 'running':
      return (
        <Badge className="bg-sky-500/15 text-sky-700 dark:text-sky-300">
          Running
        </Badge>
      );
    case 'queued':
      return <Badge variant="secondary">Queued</Badge>;
    case 'completed':
      return (
        <Badge className="bg-emerald-500/15 text-emerald-700 dark:text-emerald-300">
          Completed
        </Badge>
      );
    case 'failed':
      return (
        <Badge className="bg-destructive/15 text-destructive">Failed</Badge>
      );
    case 'cancelled':
      return <Badge variant="outline">Cancelled</Badge>;
  }
}

function stepKindIcon(kind: WorkflowStepKind) {
  switch (kind) {
    case 'agent':
      return <Bot className="size-3.5 text-sky-500" aria-hidden />;
    case 'shell':
      return <Terminal className="size-3.5 text-violet-500" aria-hidden />;
    case 'ask':
      return <Hand className="size-3.5 text-amber-500" aria-hidden />;
  }
}

function stepStatusIcon(status: WorkflowStep['status']) {
  switch (status) {
    case 'completed':
      return (
        <CheckCircle2
          className="size-3.5 text-emerald-500"
          aria-label="completed"
        />
      );
    case 'running':
      return (
        <Loader2
          className="size-3.5 animate-spin text-sky-500"
          aria-label="running"
        />
      );
    case 'waiting':
      return <Hand className="size-3.5 text-amber-500" aria-label="waiting" />;
    case 'failed':
      return (
        <XCircle className="size-3.5 text-destructive" aria-label="failed" />
      );
    default:
      return (
        <Clock className="size-3.5 text-muted-foreground" aria-label={status} />
      );
  }
}

/** Always-On Durable Multi-Stage Workflows Dashboard. */
export function WorkflowsPage() {
  useDocumentTitle('Workflows');
  const qc = useQueryClient();
  const now = useNow(15_000);

  const [tab, setTab] = useState<WorkflowTab>('needs_input');
  const [editing, setEditing] = useState<Workflow | 'new' | null>(null);
  const [webhookBanner, setWebhookBanner] = useState<string | null>(null);

  const agents = useQuery(agentsQuery);
  const defs = useQuery(workflowsQuery);
  const allRuns = useQuery(workflowRunsQuery('all'));

  const runs = allRuns.data ?? [];
  const needsInputRuns = runs.filter((r) => r.status === 'needs_input');
  const runningRuns = runs.filter(
    (r) => r.status === 'running' || r.status === 'queued',
  );

  const invalidateAll = async () => {
    await Promise.all([
      qc.invalidateQueries({ queryKey: ['workflows'] }),
      qc.invalidateQueries({ queryKey: ['workflow-runs'] }),
    ]);
  };

  const answerMut = useMutation({
    mutationFn: ({
      runId,
      stepId,
      choice,
      reason,
    }: {
      runId: string;
      stepId: string;
      choice: string;
      reason?: string;
    }) =>
      unwrap(
        api.POST('/workflow-runs/{runId}/steps/{stepId}/answer', {
          params: { path: { runId, stepId } },
          body: { choice, ...(reason ? { reason } : {}) },
        }),
      ),
    onSuccess: invalidateAll,
  });

  const cancelMut = useMutation({
    mutationFn: (runId: string) =>
      unwrap(
        api.POST('/workflow-runs/{runId}/cancel', {
          params: { path: { runId } },
        }),
      ),
    onSuccess: invalidateAll,
  });

  const resumeMut = useMutation({
    mutationFn: (runId: string) =>
      unwrap(
        api.POST('/workflow-runs/{runId}/resume', {
          params: { path: { runId } },
        }),
      ),
    onSuccess: invalidateAll,
  });

  const startMut = useMutation({
    mutationFn: ({
      workflowId,
      input,
    }: {
      workflowId: string;
      input?: Record<string, unknown>;
    }) =>
      unwrap(
        api.POST('/workflows/{workflowId}/run', {
          params: { path: { workflowId } },
          body: input ? { input } : {},
        }),
      ),
    onSuccess: async () => {
      await invalidateAll();
      setTab('running');
    },
  });

  const toggleMut = useMutation({
    mutationFn: (wf: Workflow) =>
      unwrap(
        api.PUT('/workflows/{workflowId}', {
          params: { path: { workflowId: wf.id } },
          body: {
            name: wf.name,
            description: wf.description,
            definition: wf.definition,
            enabled: !wf.enabled,
          },
        }),
      ),
    onSuccess: invalidateAll,
  });

  const deleteMut = useMutation({
    mutationFn: (workflowId: string) =>
      unwrap(
        api.DELETE('/workflows/{workflowId}', {
          params: { path: { workflowId } },
        }),
      ),
    onSuccess: invalidateAll,
  });

  const saveDefMut = useMutation({
    mutationFn: async ({
      id,
      body,
    }: {
      id?: string;
      body: WorkflowInput;
    }): Promise<Workflow> =>
      id
        ? unwrap(
            api.PUT('/workflows/{workflowId}', {
              params: { path: { workflowId: id } },
              body,
            }),
          )
        : unwrap(api.POST('/workflows', { body })),
    onSuccess: async (saved) => {
      if (saved.webhookUrl) {
        setWebhookBanner(saved.webhookUrl);
      }
      setEditing(null);
      await invalidateAll();
    },
  });

  const tabs: { id: WorkflowTab; label: string; count?: number }[] = [
    { id: 'needs_input', label: 'Needs you', count: needsInputRuns.length },
    { id: 'running', label: 'Running', count: runningRuns.length },
    { id: 'all', label: 'All runs', count: runs.length },
    { id: 'definitions', label: 'Definitions', count: defs.data?.length ?? 0 },
  ];

  const actionError =
    answerMut.error ??
    cancelMut.error ??
    resumeMut.error ??
    startMut.error ??
    toggleMut.error ??
    deleteMut.error;

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-auto">
      <TopBar>
        <Crumb current>Workflows</Crumb>
      </TopBar>
      <div className="mx-auto flex w-full max-w-6xl flex-col gap-6 p-6">
        <SettingsHeader
          title="Workflows"
          description="Always-on durable multi-stage pipelines chaining agent turns, sandbox shell steps, and human approval gates."
        >
          <Button
            onClick={() => {
              setTab('definitions');
              setEditing('new');
            }}
          >
            <Plus className="size-4" />
            New workflow
          </Button>
        </SettingsHeader>

        {webhookBanner && (
          <Card className="flex flex-wrap items-center gap-2 border-emerald-500/40 bg-emerald-500/5 p-3 text-xs">
            <Webhook className="size-4 shrink-0 text-emerald-600" />
            <span className="font-medium">Webhook URL (shown once):</span>
            <code className="flex-1 truncate rounded bg-muted px-2 py-1 font-mono">
              {webhookBanner}
            </code>
            <Button
              size="sm"
              variant="outline"
              onClick={() => void navigator.clipboard?.writeText(webhookBanner)}
            >
              <Copy className="size-3.5" />
              Copy
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setWebhookBanner(null)}
            >
              Dismiss
            </Button>
          </Card>
        )}

        <div
          role="tablist"
          aria-label="Workflow views"
          className="flex flex-wrap gap-1 border-b pb-2"
        >
          {tabs.map((t) => (
            <button
              key={t.id}
              type="button"
              role="tab"
              aria-selected={tab === t.id}
              onClick={() => setTab(t.id)}
              className={cn(
                'inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-medium transition-colors',
                tab === t.id
                  ? 'bg-accent text-foreground'
                  : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
              )}
            >
              <span>{t.label}</span>
              {typeof t.count === 'number' && (
                <span
                  className={cn(
                    'rounded-full px-1.5 py-0.5 text-[11px] leading-none tabular-nums',
                    t.id === 'needs_input' && t.count > 0
                      ? 'bg-amber-500/20 text-amber-700 dark:text-amber-300'
                      : 'bg-muted text-muted-foreground',
                  )}
                >
                  {t.count}
                </span>
              )}
            </button>
          ))}
        </div>

        <ErrorText error={allRuns.error ?? defs.error ?? actionError} />

        {tab === 'needs_input' && (
          <section aria-label="Pending approvals" className="grid gap-4">
            {allRuns.isPending && <Loading />}
            {!allRuns.isPending && needsInputRuns.length === 0 && (
              <Card className="p-8 text-center text-sm text-muted-foreground">
                Nothing needs your input right now. When a workflow reaches an
                approval gate, it will pause here until you respond.
              </Card>
            )}
            {needsInputRuns.map((run) => {
              const waiting = run.steps.find((s) => s.status === 'waiting');
              if (!waiting) return null;
              return (
                <ApprovalCard
                  key={run.id}
                  run={run}
                  step={waiting}
                  now={now}
                  busy={answerMut.isPending || cancelMut.isPending}
                  onAnswer={(choice, reason) =>
                    answerMut.mutate({
                      runId: run.id,
                      stepId: waiting.id,
                      choice,
                      reason,
                    })
                  }
                  onCancel={() => cancelMut.mutate(run.id)}
                />
              );
            })}
          </section>
        )}

        {tab === 'running' && (
          <section aria-label="Running workflows" className="grid gap-4">
            {allRuns.isPending && <Loading />}
            {!allRuns.isPending && runningRuns.length === 0 && (
              <Card className="p-8 text-center text-sm text-muted-foreground">
                No workflows are currently running or queued.
              </Card>
            )}
            {runningRuns.map((run) => (
              <RunCard
                key={run.id}
                run={run}
                now={now}
                busy={cancelMut.isPending || resumeMut.isPending}
                onCancel={() => cancelMut.mutate(run.id)}
                onResume={() => resumeMut.mutate(run.id)}
              />
            ))}
          </section>
        )}

        {tab === 'all' && (
          <section aria-label="All workflow runs" className="grid gap-4">
            {allRuns.isPending && <Loading />}
            {!allRuns.isPending && runs.length === 0 && (
              <Card className="p-8 text-center text-sm text-muted-foreground">
                No workflow runs yet. Create a workflow under Definitions and
                click Run.
              </Card>
            )}
            {runs.map((run) => (
              <RunCard
                key={run.id}
                run={run}
                now={now}
                busy={cancelMut.isPending || resumeMut.isPending}
                onCancel={() => cancelMut.mutate(run.id)}
                onResume={() => resumeMut.mutate(run.id)}
              />
            ))}
          </section>
        )}

        {tab === 'definitions' && (
          <section aria-label="Workflow definitions" className="grid gap-4">
            {editing !== null && (
              <WorkflowBuilderForm
                initial={editing === 'new' ? undefined : editing}
                agents={agents.data ?? []}
                pending={saveDefMut.isPending}
                error={saveDefMut.error}
                onCancel={() => setEditing(null)}
                onSave={(body) =>
                  saveDefMut.mutate({
                    id: editing === 'new' ? undefined : editing.id,
                    body,
                  })
                }
              />
            )}

            {defs.isPending && <Loading />}
            {!defs.isPending && (defs.data?.length ?? 0) === 0 && !editing && (
              <Card className="p-8 text-center text-sm text-muted-foreground">
                No workflow definitions yet. Click &ldquo;New workflow&rdquo; to
                build a multi-stage pipeline.
              </Card>
            )}
            {(defs.data ?? []).map((wf) => (
              <Card key={wf.id} className="grid gap-3 p-4">
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div className="grid gap-1">
                    <div className="flex items-center gap-2">
                      <WorkflowIcon
                        className="size-4 text-muted-foreground"
                        aria-hidden
                      />
                      <h3 className="font-medium">{wf.name}</h3>
                      <Badge variant={wf.enabled ? 'secondary' : 'outline'}>
                        {wf.enabled ? 'Enabled' : 'Disabled'}
                      </Badge>
                      {wf.definition.schedule && (
                        <Badge
                          variant="outline"
                          className="font-mono text-[11px]"
                        >
                          cron: {wf.definition.schedule}
                        </Badge>
                      )}
                      {wf.hasWebhook && (
                        <Badge variant="outline">Webhook</Badge>
                      )}
                      {wf.definition.concurrencyLimit ? (
                        <Badge variant="outline">
                          max {wf.definition.concurrencyLimit} concurrent
                        </Badge>
                      ) : null}
                    </div>
                    {wf.description && (
                      <p className="text-xs text-muted-foreground">
                        {wf.description}
                      </p>
                    )}
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <Button
                      size="sm"
                      disabled={startMut.isPending}
                      onClick={() => startMut.mutate({ workflowId: wf.id })}
                    >
                      <Play className="size-3.5" />
                      Run
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={toggleMut.isPending}
                      onClick={() => toggleMut.mutate(wf)}
                    >
                      {wf.enabled ? 'Disable' : 'Enable'}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      aria-label={`Edit ${wf.name}`}
                      onClick={() => setEditing(wf)}
                    >
                      <Pencil className="size-3.5" />
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      aria-label={`Delete ${wf.name}`}
                      disabled={deleteMut.isPending}
                      onClick={() => deleteMut.mutate(wf.id)}
                    >
                      <Trash2 className="size-3.5 text-destructive" />
                    </Button>
                  </div>
                </div>
                <ol className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  {wf.definition.steps.map((s, idx) => (
                    <li
                      key={s.name}
                      className="inline-flex items-center gap-1.5 rounded-md border bg-muted/30 px-2 py-1"
                    >
                      <span className="font-mono text-[10px] opacity-60">
                        {idx + 1}.
                      </span>
                      {stepKindIcon(s.kind)}
                      <span className="font-medium text-foreground">
                        {s.name}
                      </span>
                      <span>({s.kind})</span>
                    </li>
                  ))}
                </ol>
              </Card>
            ))}
          </section>
        )}
      </div>
    </div>
  );
}

function ApprovalCard({
  run,
  step,
  now,
  busy,
  onAnswer,
  onCancel,
}: {
  run: WorkflowRun;
  step: WorkflowStep;
  now: number;
  busy: boolean;
  onAnswer: (choice: string, reason?: string) => void;
  onCancel: () => void;
}) {
  const [reason, setReason] = useState('');
  const prevSteps = run.steps.filter(
    (s) => s.stepIndex < step.stepIndex && s.status === 'completed',
  );
  return (
    <Card className="grid gap-4 border-amber-500/40 p-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="grid gap-1">
          <div className="flex items-center gap-2">
            <Hand className="size-4 text-amber-500" aria-hidden />
            <span className="text-xs font-medium text-muted-foreground">
              {run.workflowName} · step {step.stepIndex + 1} ({step.name})
            </span>
            {runStatusBadge(run.status)}
          </div>
          <h3 className="text-base font-medium">
            {step.askTitle || 'Approval required'}
          </h3>
        </div>
        <span className="text-xs text-muted-foreground tabular-nums">
          {relTime(run.updatedAt, now)}
        </span>
      </div>

      {prevSteps.length > 0 && (
        <div className="grid gap-1.5 rounded-lg bg-muted/40 p-3 text-xs">
          <div className="font-medium text-muted-foreground">
            Previous step output
          </div>
          {prevSteps.map((ps) => (
            <div key={ps.id} className="grid gap-0.5">
              <div className="flex items-center gap-2">
                <span className="font-medium">{ps.name}</span>
                {ps.kind === 'agent' && ps.agentId && ps.sessionId && (
                  <Link
                    to="/a/$agentId"
                    params={{ agentId: ps.agentId }}
                    search={{ s: ps.sessionId }}
                    className="inline-flex items-center gap-1 text-primary hover:underline"
                  >
                    Open thread
                    <ExternalLink className="size-3" />
                  </Link>
                )}
              </div>
              {ps.output && (
                <pre className="max-h-28 overflow-auto whitespace-pre-wrap font-mono text-[11px] text-muted-foreground">
                  {ps.output}
                </pre>
              )}
            </div>
          ))}
        </div>
      )}

      <div className="grid gap-2">
        <Input
          aria-label={`Reason for ${step.name}`}
          placeholder="Optional note or reason…"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
        />
        <div className="flex flex-wrap items-center gap-2">
          {(step.askChoices.length > 0
            ? step.askChoices
            : ['Approve', 'Reject']
          ).map((choice, idx) => (
            <Button
              key={choice}
              size="sm"
              variant={idx === 0 ? 'default' : 'outline'}
              disabled={busy}
              onClick={() => onAnswer(choice, reason.trim() || undefined)}
            >
              {choice}
            </Button>
          ))}
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={onCancel}
            className="ml-auto text-muted-foreground"
          >
            <Square className="size-3.5" />
            Cancel run
          </Button>
        </div>
      </div>
    </Card>
  );
}

function RunCard({
  run,
  now,
  busy,
  onCancel,
  onResume,
}: {
  run: WorkflowRun;
  now: number;
  busy: boolean;
  onCancel: () => void;
  onResume: () => void;
}) {
  const canCancel =
    run.status === 'running' ||
    run.status === 'queued' ||
    run.status === 'needs_input';
  const canResume = run.status === 'failed';

  return (
    <Card className="grid gap-3 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="font-medium">{run.workflowName}</h3>
          {runStatusBadge(run.status)}
          <Badge variant="outline" className="text-[11px]">
            {run.triggerKind}
          </Badge>
          <span className="font-mono text-xs text-muted-foreground">
            {run.id}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground tabular-nums">
            {relTime(run.updatedAt, now)}
          </span>
          {canResume && (
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={onResume}
            >
              <RotateCcw className="size-3.5" />
              Resume
            </Button>
          )}
          {canCancel && (
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={onCancel}
            >
              <Square className="size-3.5" />
              Cancel
            </Button>
          )}
        </div>
      </div>

      {run.error && (
        <p className="rounded-md bg-destructive/10 px-3 py-1.5 text-xs text-destructive">
          {run.error}
        </p>
      )}

      <ol className="grid gap-1.5">
        {run.steps.map((s) => (
          <li
            key={s.id}
            className="grid gap-1 rounded-lg border bg-muted/20 px-3 py-2 text-xs"
          >
            <div className="flex flex-wrap items-center gap-2">
              {stepStatusIcon(s.status)}
              {stepKindIcon(s.kind)}
              <span className="font-medium">{s.name}</span>
              <span className="text-muted-foreground">({s.kind})</span>
              {s.kind === 'agent' && s.agentId && s.sessionId && (
                <Link
                  to="/a/$agentId"
                  params={{ agentId: s.agentId }}
                  search={{ s: s.sessionId }}
                  className="ml-auto inline-flex items-center gap-1 font-medium text-primary hover:underline"
                >
                  Open live thread
                  <ExternalLink className="size-3" />
                </Link>
              )}
              {s.kind === 'ask' && s.answerChoice && (
                <Badge variant="secondary" className="ml-auto">
                  Answered: {s.answerChoice}
                </Badge>
              )}
            </div>
            {s.output && (
              <pre className="max-h-24 overflow-auto whitespace-pre-wrap rounded bg-background/60 p-2 font-mono text-[11px] text-muted-foreground">
                {s.output}
              </pre>
            )}
            {s.error && <div className="text-destructive">{s.error}</div>}
          </li>
        ))}
      </ol>
    </Card>
  );
}

interface DraftStep {
  name: string;
  kind: WorkflowStepKind;
  agentId: string;
  prompt: string;
  reuseSession: boolean;
  command: string;
  cwd: string;
  askTitle: string;
  askChoicesText: string;
  continueOnChoicesText: string;
}

function makeDraftStep(
  kind: WorkflowStepKind,
  idx: number,
  defaultAgentId: string,
): DraftStep {
  return {
    name: `${kind}_${idx + 1}`,
    kind,
    agentId: defaultAgentId,
    prompt: '',
    reuseSession: false,
    command: '',
    cwd: '',
    askTitle: '',
    askChoicesText: 'Approve, Reject',
    continueOnChoicesText: 'Approve',
  };
}

function fromStepDef(s: WorkflowStepDef, fallbackAgent: string): DraftStep {
  return {
    name: s.name,
    kind: s.kind,
    agentId: s.agentId ?? fallbackAgent,
    prompt: s.prompt ?? '',
    reuseSession: !!s.reuseSession,
    command: s.command ?? '',
    cwd: s.cwd ?? '',
    askTitle: s.askTitle ?? '',
    askChoicesText: (s.askChoices ?? ['Approve', 'Reject']).join(', '),
    continueOnChoicesText: (s.continueOnChoices ?? []).join(', '),
  };
}

function WorkflowBuilderForm({
  initial,
  agents,
  pending,
  error,
  onSave,
  onCancel,
}: {
  initial?: Workflow;
  agents: Agent[];
  pending: boolean;
  error: unknown;
  onSave: (body: WorkflowInput) => void;
  onCancel: () => void;
}) {
  const defaultAgentId = agents[0]?.id ?? '';
  const [name, setName] = useState(initial?.name ?? '');
  const [description, setDescription] = useState(initial?.description ?? '');
  const [schedule, setSchedule] = useState(initial?.definition.schedule ?? '');
  const [timezone, setTimezone] = useState(
    initial?.definition.timezone ?? 'UTC',
  );
  const [concurrencyLimit, setConcurrencyLimit] = useState(
    String(initial?.definition.concurrencyLimit ?? 0),
  );
  const [webhookEnabled, setWebhookEnabled] = useState(
    !!initial?.definition.webhookEnabled,
  );
  const [steps, setSteps] = useState<DraftStep[]>(() =>
    initial
      ? initial.definition.steps.map((s) => fromStepDef(s, defaultAgentId))
      : [makeDraftStep('agent', 0, defaultAgentId)],
  );

  const updateStep = (idx: number, patch: Partial<DraftStep>) =>
    setSteps((prev) =>
      prev.map((s, i) => (i === idx ? { ...s, ...patch } : s)),
    );

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const mapped: WorkflowStepDef[] = steps.map((s) => {
      const base: WorkflowStepDef = { name: s.name.trim(), kind: s.kind };
      if (s.kind === 'agent') {
        base.agentId = s.agentId;
        base.prompt = s.prompt;
        if (s.reuseSession) base.reuseSession = true;
      } else if (s.kind === 'shell') {
        base.agentId = s.agentId;
        base.command = s.command;
        if (s.cwd.trim()) base.cwd = s.cwd.trim();
      } else {
        base.askTitle = s.askTitle;
        base.askChoices = s.askChoicesText
          .split(',')
          .map((c) => c.trim())
          .filter(Boolean);
        const cont = s.continueOnChoicesText
          .split(',')
          .map((c) => c.trim())
          .filter(Boolean);
        if (cont.length > 0) base.continueOnChoices = cont;
      }
      return base;
    });
    const limit = Math.max(0, Number(concurrencyLimit) || 0);
    onSave({
      name: name.trim(),
      description: description.trim(),
      enabled: initial ? initial.enabled : true,
      definition: {
        steps: mapped,
        ...(schedule.trim()
          ? { schedule: schedule.trim(), timezone: timezone.trim() || 'UTC' }
          : {}),
        ...(limit > 0 ? { concurrencyLimit: limit } : {}),
        webhookEnabled,
      },
    });
  };

  return (
    <Card className="grid gap-4 p-4">
      <h2 className="text-base font-medium">
        {initial ? `Edit workflow: ${initial.name}` : 'New workflow'}
      </h2>
      <form onSubmit={onSubmit} className="grid gap-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">Workflow name</span>
            <Input
              aria-label="Workflow name"
              placeholder="e.g. Triage and release"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </label>
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">Description</span>
            <Input
              aria-label="Workflow description"
              placeholder="What this pipeline automates"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </label>
        </div>

        <div className="grid gap-3 sm:grid-cols-4">
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">
              Schedule cron (optional)
            </span>
            <Input
              aria-label="Schedule cron"
              placeholder="0 9 * * 1-5"
              value={schedule}
              onChange={(e) => setSchedule(e.target.value)}
            />
          </label>
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">Timezone</span>
            <Input
              aria-label="Timezone"
              value={timezone}
              onChange={(e) => setTimezone(e.target.value)}
            />
          </label>
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">
              Concurrency limit (0 = unlimited)
            </span>
            <Input
              aria-label="Concurrency limit"
              type="number"
              min="0"
              value={concurrencyLimit}
              onChange={(e) => setConcurrencyLimit(e.target.value)}
            />
          </label>
          <label className="flex items-end gap-2 pb-2 text-xs">
            <input
              type="checkbox"
              checked={webhookEnabled}
              onChange={(e) => setWebhookEnabled(e.target.checked)}
            />
            <span>Enable webhook trigger</span>
          </label>
        </div>

        <div className="grid gap-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h3 className="text-sm font-medium">Pipeline steps</h3>
            <div className="flex gap-1.5">
              {(['agent', 'shell', 'ask'] as const).map((kind) => (
                <Button
                  key={kind}
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() =>
                    setSteps((prev) => [
                      ...prev,
                      makeDraftStep(kind, prev.length, defaultAgentId),
                    ])
                  }
                >
                  <Plus className="size-3.5" />
                  Add {kind}
                </Button>
              ))}
            </div>
          </div>

          {steps.map((s, i) => (
            <div
              key={i}
              className="grid gap-2.5 rounded-lg border bg-muted/20 p-3"
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-xs text-muted-foreground">
                  Step {i + 1}
                </span>
                <Input
                  aria-label={`Step ${i + 1} name`}
                  value={s.name}
                  onChange={(e) => updateStep(i, { name: e.target.value })}
                  className="h-8 max-w-44 text-xs"
                  required
                />
                <Select
                  aria-label={`Step ${i + 1} kind`}
                  value={s.kind}
                  onChange={(e) =>
                    updateStep(i, { kind: e.target.value as WorkflowStepKind })
                  }
                  className="h-8 w-32 text-xs"
                >
                  <option value="agent">agent</option>
                  <option value="shell">shell</option>
                  <option value="ask">ask</option>
                </Select>
                {steps.length > 1 && (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    aria-label={`Remove step ${i + 1}`}
                    onClick={() =>
                      setSteps((prev) => prev.filter((_, idx) => idx !== i))
                    }
                    className="ml-auto"
                  >
                    <Trash2 className="size-3.5 text-destructive" />
                  </Button>
                )}
              </div>

              {(s.kind === 'agent' || s.kind === 'shell') && (
                <label className="grid gap-1 text-xs">
                  <span className="text-muted-foreground">Target agent</span>
                  <Select
                    aria-label={`Step ${i + 1} agent`}
                    value={s.agentId}
                    onChange={(e) => updateStep(i, { agentId: e.target.value })}
                    required
                  >
                    <option value="">Select an agent…</option>
                    {agents.map((ag) => (
                      <option key={ag.id} value={ag.id}>
                        {ag.name} ({ag.harness})
                      </option>
                    ))}
                  </Select>
                </label>
              )}

              {s.kind === 'agent' && (
                <>
                  <label className="grid gap-1 text-xs">
                    <span className="text-muted-foreground">
                      Prompt (supports {'{{input}}'} and{' '}
                      {'{{steps.<name>.output}}'})
                    </span>
                    <textarea
                      aria-label={`Step ${i + 1} prompt`}
                      rows={3}
                      value={s.prompt}
                      onChange={(e) =>
                        updateStep(i, { prompt: e.target.value })
                      }
                      required
                      className="rounded-md border bg-background px-3 py-2 text-xs focus:ring-2 focus:ring-ring focus:outline-none"
                    />
                  </label>
                  <label className="flex items-center gap-2 text-xs text-muted-foreground">
                    <input
                      type="checkbox"
                      checked={s.reuseSession}
                      onChange={(e) =>
                        updateStep(i, { reuseSession: e.target.checked })
                      }
                    />
                    Reuse prior agent thread in this run
                  </label>
                </>
              )}

              {s.kind === 'shell' && (
                <div className="grid gap-2 sm:grid-cols-3">
                  <label className="grid gap-1 text-xs sm:col-span-2">
                    <span className="text-muted-foreground">
                      Shell command (runs in agent sandbox)
                    </span>
                    <Input
                      aria-label={`Step ${i + 1} command`}
                      placeholder="make test"
                      value={s.command}
                      onChange={(e) =>
                        updateStep(i, { command: e.target.value })
                      }
                      required
                    />
                  </label>
                  <label className="grid gap-1 text-xs">
                    <span className="text-muted-foreground">
                      Working dir (optional)
                    </span>
                    <Input
                      aria-label={`Step ${i + 1} working directory`}
                      placeholder="repo"
                      value={s.cwd}
                      onChange={(e) => updateStep(i, { cwd: e.target.value })}
                    />
                  </label>
                </div>
              )}

              {s.kind === 'ask' && (
                <div className="grid gap-2 sm:grid-cols-3">
                  <label className="grid gap-1 text-xs sm:col-span-3">
                    <span className="text-muted-foreground">
                      Approval question title
                    </span>
                    <Input
                      aria-label={`Step ${i + 1} ask title`}
                      placeholder="Ship release? Summary: {{steps.triage.output}}"
                      value={s.askTitle}
                      onChange={(e) =>
                        updateStep(i, { askTitle: e.target.value })
                      }
                      required
                    />
                  </label>
                  <label className="grid gap-1 text-xs sm:col-span-2">
                    <span className="text-muted-foreground">
                      Choices (comma-separated)
                    </span>
                    <Input
                      aria-label={`Step ${i + 1} ask choices`}
                      value={s.askChoicesText}
                      onChange={(e) =>
                        updateStep(i, { askChoicesText: e.target.value })
                      }
                    />
                  </label>
                  <label className="grid gap-1 text-xs">
                    <span className="text-muted-foreground">
                      Continue on choices (optional)
                    </span>
                    <Input
                      aria-label={`Step ${i + 1} continue choices`}
                      placeholder="Approve"
                      value={s.continueOnChoicesText}
                      onChange={(e) =>
                        updateStep(i, { continueOnChoicesText: e.target.value })
                      }
                    />
                  </label>
                </div>
              )}
            </div>
          ))}
        </div>

        <ErrorText error={error} />
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onCancel}>
            Cancel
          </Button>
          <Button type="submit" disabled={pending}>
            {initial ? 'Save workflow' : 'Create workflow'}
          </Button>
        </div>
      </form>
    </Card>
  );
}
