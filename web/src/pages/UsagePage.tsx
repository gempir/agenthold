import { useMemo, useState, type FormEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ChartNoAxesColumn,
  Coins,
  Cpu,
  RotateCcw,
  Save,
  SlidersHorizontal,
  Users,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  meQuery,
  usagePricesQuery,
  usageSummaryQuery,
  type UsageBucket,
  type UsageModelPrice,
  type UsageRange,
  type UsageScope,
} from '@/api/queries';
import { HarnessIcon } from '@/components/HarnessIcon';
import { SettingsHeader } from '@/components/layout/SettingsLayout';
import { Crumb, TopBar } from '@/components/layout/TopBar';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { useDocumentTitle } from '@/hooks/useDocumentTitle';
import { cn } from '@/lib/utils';

type MetricMode = 'cost' | 'tokens';

const RANGES: { value: UsageRange; label: string }[] = [
  { value: '24h', label: '24h' },
  { value: '7d', label: '7d' },
  { value: '30d', label: '30d' },
  { value: '90d', label: '90d' },
];

function formatUsd(v: number): string {
  if (v === 0) return '$0.00';
  if (v < 0.01) return `$${v.toFixed(4)}`;
  return `$${v.toFixed(2)}`;
}

function formatTokens(n: number): string {
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(2)}B`;
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

function shortBucketLabel(bucket: string, range: UsageRange): string {
  if (range === '24h') {
    const m = bucket.match(/T(\d{2}:\d{2})Z$/);
    return m ? m[1] : bucket;
  }
  return bucket.slice(5); // MM-DD
}

/** Usage & Cost Analytics Dashboard. */
export function UsagePage() {
  useDocumentTitle('Usage');
  const me = useQuery(meQuery);
  const isAdmin = !!me.data?.admin;

  const [range, setRange] = useState<UsageRange>('7d');
  const [metric, setMetric] = useState<MetricMode>('cost');
  const [scope, setScope] = useState<UsageScope>('me');
  const [showPricing, setShowPricing] = useState(false);
  const [editingModel, setEditingModel] = useState('');

  const effectiveScope: UsageScope = isAdmin ? scope : 'me';
  const summary = useQuery(usageSummaryQuery(range, effectiveScope));
  const prices = useQuery({
    ...usagePricesQuery,
    enabled: showPricing || isAdmin,
  });

  const data = summary.data;

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-auto">
      <TopBar>
        <Crumb current>Usage</Crumb>
      </TopBar>
      <div className="mx-auto flex w-full max-w-6xl flex-col gap-6 p-6">
        <SettingsHeader
          title="Usage & Cost Analytics"
          description="Track token volume, cache hits, and estimated API-equivalent costs across harnesses and models."
        >
          <div className="flex flex-wrap items-center gap-2">
            {isAdmin && (
              <div
                role="group"
                aria-label="Usage scope"
                className="inline-flex rounded-lg border bg-muted/40 p-0.5 text-xs"
              >
                <button
                  type="button"
                  aria-pressed={effectiveScope === 'me'}
                  onClick={() => setScope('me')}
                  className={cn(
                    'rounded-md px-2.5 py-1 font-medium transition-colors',
                    effectiveScope === 'me'
                      ? 'bg-background text-foreground shadow-xs'
                      : 'text-muted-foreground hover:text-foreground',
                  )}
                >
                  My usage
                </button>
                <button
                  type="button"
                  aria-pressed={effectiveScope === 'org'}
                  onClick={() => setScope('org')}
                  className={cn(
                    'rounded-md px-2.5 py-1 font-medium transition-colors',
                    effectiveScope === 'org'
                      ? 'bg-background text-foreground shadow-xs'
                      : 'text-muted-foreground hover:text-foreground',
                  )}
                >
                  Organization
                </button>
              </div>
            )}

            <div
              role="group"
              aria-label="Metric view"
              className="inline-flex rounded-lg border bg-muted/40 p-0.5 text-xs"
            >
              <button
                type="button"
                aria-pressed={metric === 'cost'}
                onClick={() => setMetric('cost')}
                className={cn(
                  'rounded-md px-2.5 py-1 font-medium transition-colors',
                  metric === 'cost'
                    ? 'bg-background text-foreground shadow-xs'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                Cost
              </button>
              <button
                type="button"
                aria-pressed={metric === 'tokens'}
                onClick={() => setMetric('tokens')}
                className={cn(
                  'rounded-md px-2.5 py-1 font-medium transition-colors',
                  metric === 'tokens'
                    ? 'bg-background text-foreground shadow-xs'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                Tokens
              </button>
            </div>

            <div
              role="group"
              aria-label="Time range"
              className="inline-flex rounded-lg border bg-muted/40 p-0.5 text-xs"
            >
              {RANGES.map((r) => (
                <button
                  key={r.value}
                  type="button"
                  aria-pressed={range === r.value}
                  onClick={() => setRange(r.value)}
                  className={cn(
                    'rounded-md px-2.5 py-1 font-medium transition-colors',
                    range === r.value
                      ? 'bg-background text-foreground shadow-xs'
                      : 'text-muted-foreground hover:text-foreground',
                  )}
                >
                  {r.label}
                </button>
              ))}
            </div>

            {isAdmin && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => setShowPricing((v) => !v)}
              >
                <SlidersHorizontal className="size-3.5" />
                Model prices
              </Button>
            )}
          </div>
        </SettingsHeader>

        {summary.isPending && <Loading />}
        <ErrorText error={summary.error} />

        {data && (
          <>
            <section
              aria-label="Usage summary"
              className="grid grid-cols-2 gap-3 lg:grid-cols-4"
            >
              <Card className="grid gap-1 p-4">
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <Coins className="size-3.5" aria-hidden />
                  Estimated cost
                </div>
                <div
                  data-testid="usage-total-cost"
                  className="text-2xl font-semibold tracking-tight tabular-nums"
                >
                  {formatUsd(data.totals.totalCostUsd)}
                </div>
                <div className="text-xs text-muted-foreground">
                  API-equivalent ({range})
                </div>
              </Card>

              <Card className="grid gap-1 p-4">
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <ChartNoAxesColumn className="size-3.5" aria-hidden />
                  Total tokens
                </div>
                <div
                  data-testid="usage-total-tokens"
                  className="text-2xl font-semibold tracking-tight tabular-nums"
                >
                  {formatTokens(data.totals.totalTokens)}
                </div>
                <div className="text-xs text-muted-foreground tabular-nums">
                  {formatTokens(data.totals.inputTokens)} in ·{' '}
                  {formatTokens(data.totals.outputTokens)} out
                </div>
              </Card>

              <Card className="grid gap-1 p-4">
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <Cpu className="size-3.5" aria-hidden />
                  Cache read tokens
                </div>
                <div className="text-2xl font-semibold tracking-tight tabular-nums">
                  {formatTokens(data.totals.cacheReadTokens)}
                </div>
                <div className="text-xs text-muted-foreground tabular-nums">
                  {data.totals.totalTokens > 0
                    ? `${Math.round((data.totals.cacheReadTokens / data.totals.totalTokens) * 100)}% of total`
                    : '0% of total'}
                </div>
              </Card>

              <Card className="grid gap-1 p-4">
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <Users className="size-3.5" aria-hidden />
                  Completed turns
                </div>
                <div className="text-2xl font-semibold tracking-tight tabular-nums">
                  {data.totals.turnCount.toLocaleString()}
                </div>
                <div className="text-xs text-muted-foreground">
                  {effectiveScope === 'org'
                    ? `${data.byUser.length} active user${data.byUser.length === 1 ? '' : 's'}`
                    : `${data.byModel.length} model${data.byModel.length === 1 ? '' : 's'}`}
                </div>
              </Card>
            </section>

            <UsageBarChart series={data.series} metric={metric} range={range} />

            <section aria-label="Usage by model" className="grid gap-2">
              <h2 className="text-sm font-medium">Models & harnesses</h2>
              {data.byModel.length === 0 ? (
                <Card className="p-6 text-center text-sm text-muted-foreground">
                  No token usage recorded in this window yet. Usage is captured
                  automatically when agent turns complete.
                </Card>
              ) : (
                <Card className="overflow-x-auto">
                  <table className="w-full text-left text-sm">
                    <thead className="border-b text-xs text-muted-foreground">
                      <tr>
                        <th className="px-4 py-2.5 font-medium">Model</th>
                        <th className="px-4 py-2.5 font-medium">Harness</th>
                        <th className="px-4 py-2.5 text-right font-medium">
                          Turns
                        </th>
                        <th className="px-4 py-2.5 text-right font-medium">
                          Input
                        </th>
                        <th className="px-4 py-2.5 text-right font-medium">
                          Output
                        </th>
                        <th className="px-4 py-2.5 text-right font-medium">
                          Cache read
                        </th>
                        <th className="px-4 py-2.5 text-right font-medium">
                          Total tokens
                        </th>
                        <th className="px-4 py-2.5 text-right font-medium">
                          Est. cost
                        </th>
                      </tr>
                    </thead>
                    <tbody className="divide-y">
                      {data.byModel.map((m) => (
                        <tr
                          key={`${m.harness}:${m.model}`}
                          className="hover:bg-accent/30"
                        >
                          <td className="px-4 py-2.5 font-mono text-xs font-medium">
                            <span className="flex items-center gap-2">
                              <span>{m.model || '(default)'}</span>
                              {!m.priced && (
                                <Badge variant="secondary">unpriced</Badge>
                              )}
                              {!m.priced && isAdmin && m.model && (
                                <button
                                  type="button"
                                  onClick={() => {
                                    setEditingModel(m.model);
                                    setShowPricing(true);
                                  }}
                                  className="text-[11px] font-sans text-primary underline-offset-2 hover:underline"
                                >
                                  Set price
                                </button>
                              )}
                            </span>
                          </td>
                          <td className="px-4 py-2.5">
                            <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                              <HarnessIcon
                                harness={m.harness}
                                className="size-3.5"
                              />
                              {m.harness}
                            </span>
                          </td>
                          <td className="px-4 py-2.5 text-right tabular-nums">
                            {m.turnCount}
                          </td>
                          <td className="px-4 py-2.5 text-right tabular-nums">
                            {formatTokens(m.inputTokens)}
                          </td>
                          <td className="px-4 py-2.5 text-right tabular-nums">
                            {formatTokens(m.outputTokens)}
                          </td>
                          <td className="px-4 py-2.5 text-right tabular-nums">
                            {formatTokens(m.cacheReadTokens)}
                          </td>
                          <td className="px-4 py-2.5 text-right font-medium tabular-nums">
                            {formatTokens(m.totalTokens)}
                          </td>
                          <td className="px-4 py-2.5 text-right font-medium tabular-nums">
                            {formatUsd(m.costUsd)}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </Card>
              )}
            </section>

            {effectiveScope === 'org' && (
              <section aria-label="Usage by user" className="grid gap-2">
                <h2 className="text-sm font-medium">Users</h2>
                {data.byUser.length === 0 ? (
                  <Card className="p-6 text-center text-sm text-muted-foreground">
                    No user activity in this window.
                  </Card>
                ) : (
                  <Card className="overflow-x-auto">
                    <table className="w-full text-left text-sm">
                      <thead className="border-b text-xs text-muted-foreground">
                        <tr>
                          <th className="px-4 py-2.5 font-medium">User</th>
                          <th className="px-4 py-2.5 text-right font-medium">
                            Turns
                          </th>
                          <th className="px-4 py-2.5 text-right font-medium">
                            Input
                          </th>
                          <th className="px-4 py-2.5 text-right font-medium">
                            Output
                          </th>
                          <th className="px-4 py-2.5 text-right font-medium">
                            Total tokens
                          </th>
                          <th className="px-4 py-2.5 text-right font-medium">
                            Est. cost
                          </th>
                        </tr>
                      </thead>
                      <tbody className="divide-y">
                        {data.byUser.map((u) => (
                          <tr key={u.userId} className="hover:bg-accent/30">
                            <td className="px-4 py-2.5">
                              <div className="font-medium">{u.userName}</div>
                              {u.userEmail && (
                                <div className="text-xs text-muted-foreground">
                                  {u.userEmail}
                                </div>
                              )}
                            </td>
                            <td className="px-4 py-2.5 text-right tabular-nums">
                              {u.turnCount}
                            </td>
                            <td className="px-4 py-2.5 text-right tabular-nums">
                              {formatTokens(u.inputTokens)}
                            </td>
                            <td className="px-4 py-2.5 text-right tabular-nums">
                              {formatTokens(u.outputTokens)}
                            </td>
                            <td className="px-4 py-2.5 text-right font-medium tabular-nums">
                              {formatTokens(u.totalTokens)}
                            </td>
                            <td className="px-4 py-2.5 text-right font-medium tabular-nums">
                              {formatUsd(u.costUsd)}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </Card>
                )}
              </section>
            )}
          </>
        )}

        {isAdmin && showPricing && (
          <PricingEditor
            prices={prices.data ?? []}
            loading={prices.isPending}
            error={prices.error}
            initialModel={editingModel}
            onClearInitial={() => setEditingModel('')}
          />
        )}
      </div>
    </div>
  );
}

function UsageBarChart({
  series,
  metric,
  range,
}: {
  series: UsageBucket[];
  metric: MetricMode;
  range: UsageRange;
}) {
  const values = useMemo(
    () => series.map((b) => (metric === 'cost' ? b.costUsd : b.totalTokens)),
    [series, metric],
  );
  const maxVal = Math.max(...values, 0);
  const labelStep =
    series.length > 30 ? 7 : series.length > 14 ? 5 : series.length > 8 ? 3 : 1;

  return (
    <Card className="grid gap-3 p-4">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-medium">
          {metric === 'cost'
            ? 'Estimated cost over time'
            : 'Token volume over time'}
        </h2>
        <span className="text-xs text-muted-foreground tabular-nums">
          Peak: {metric === 'cost' ? formatUsd(maxVal) : formatTokens(maxVal)}
        </span>
      </div>
      <div
        role="img"
        aria-label={
          metric === 'cost'
            ? `Cost chart for ${range}`
            : `Tokens chart for ${range}`
        }
        className="w-full"
      >
        <svg
          viewBox={`0 0 ${Math.max(series.length * 24, 240)} 150`}
          className="h-44 w-full overflow-visible"
          preserveAspectRatio="none"
        >
          <line
            x1="0"
            y1="125"
            x2={Math.max(series.length * 24, 240)}
            y2="125"
            stroke="currentColor"
            className="text-border"
            strokeWidth="1"
          />
          {series.map((b, i) => {
            const val = values[i];
            const h =
              maxVal > 0 ? Math.max((val / maxVal) * 112, val > 0 ? 3 : 0) : 0;
            const x = i * 24 + 4;
            const y = 125 - h;
            const title = `${b.bucket}: ${formatUsd(b.costUsd)} · ${formatTokens(b.totalTokens)} tokens (${b.turnCount} turns)`;
            return (
              <g key={b.bucket}>
                <rect
                  x={x}
                  y={y}
                  width={16}
                  height={h}
                  rx={3}
                  className={cn(
                    val > 0
                      ? 'fill-primary/85 transition-opacity hover:opacity-100'
                      : 'fill-muted/40',
                  )}
                >
                  <title>{title}</title>
                </rect>
              </g>
            );
          })}
        </svg>
        <div className="mt-1 flex justify-between text-[11px] text-muted-foreground tabular-nums">
          {series.map((b, i) =>
            i % labelStep === 0 || i === series.length - 1 ? (
              <span key={b.bucket}>{shortBucketLabel(b.bucket, range)}</span>
            ) : null,
          )}
        </div>
      </div>
    </Card>
  );
}

function PricingEditor({
  prices,
  loading,
  error,
  initialModel,
  onClearInitial,
}: {
  prices: UsageModelPrice[];
  loading: boolean;
  error: unknown;
  initialModel: string;
  onClearInitial: () => void;
}) {
  const qc = useQueryClient();
  const [model, setModel] = useState(initialModel);
  const [inputPerM, setInputPerM] = useState('3.0');
  const [outputPerM, setOutputPerM] = useState('15.0');
  const [cacheReadPerM, setCacheReadPerM] = useState('0.3');

  const effectiveModel = initialModel || model;

  const save = useMutation({
    mutationFn: (body: {
      model: string;
      inputPerM: number;
      outputPerM: number;
      cacheReadPerM: number;
      reset?: boolean;
    }) => unwrap(api.PUT('/usage/prices', { body })),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['usage'] });
      onClearInitial();
    },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const m = effectiveModel.trim();
    if (!m) return;
    save.mutate({
      model: m,
      inputPerM: Number(inputPerM) || 0,
      outputPerM: Number(outputPerM) || 0,
      cacheReadPerM: Number(cacheReadPerM) || 0,
    });
  };

  return (
    <section aria-label="Custom model pricing" className="grid gap-3">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-sm font-medium">Custom model pricing (Admin)</h2>
          <p className="text-xs text-muted-foreground">
            USD per 1 million tokens. Custom prices override built-in defaults
            across all usage summaries immediately.
          </p>
        </div>
      </div>
      <Card className="grid gap-4 p-4">
        <form
          onSubmit={onSubmit}
          className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5"
        >
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">Model ID</span>
            <Input
              aria-label="Model ID"
              placeholder="e.g. claude-sonnet-4-6"
              value={effectiveModel}
              onChange={(e) => {
                onClearInitial();
                setModel(e.target.value);
              }}
              required
            />
          </label>
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">Input $/1M</span>
            <Input
              aria-label="Input USD per million"
              type="number"
              step="any"
              min="0"
              value={inputPerM}
              onChange={(e) => setInputPerM(e.target.value)}
              required
            />
          </label>
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">Output $/1M</span>
            <Input
              aria-label="Output USD per million"
              type="number"
              step="any"
              min="0"
              value={outputPerM}
              onChange={(e) => setOutputPerM(e.target.value)}
              required
            />
          </label>
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">Cache read $/1M</span>
            <Input
              aria-label="Cache read USD per million"
              type="number"
              step="any"
              min="0"
              value={cacheReadPerM}
              onChange={(e) => setCacheReadPerM(e.target.value)}
            />
          </label>
          <div className="flex items-end">
            <Button type="submit" disabled={save.isPending} className="w-full">
              <Save className="size-3.5" />
              Save price
            </Button>
          </div>
        </form>
        <ErrorText error={error ?? save.error} />
        {loading ? (
          <Loading />
        ) : (
          <div className="max-h-72 overflow-auto rounded-lg border">
            <table className="w-full text-left text-xs">
              <thead className="border-b bg-muted/30 text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">Model</th>
                  <th className="px-3 py-2 text-right font-medium">
                    Input $/1M
                  </th>
                  <th className="px-3 py-2 text-right font-medium">
                    Output $/1M
                  </th>
                  <th className="px-3 py-2 text-right font-medium">
                    Cache $/1M
                  </th>
                  <th className="px-3 py-2 text-right font-medium">Action</th>
                </tr>
              </thead>
              <tbody className="divide-y">
                {prices.map((p) => (
                  <tr key={p.model} className="hover:bg-accent/30">
                    <td className="px-3 py-1.5 font-mono">
                      <span className="flex items-center gap-1.5">
                        {p.model}
                        {p.custom && <Badge variant="secondary">custom</Badge>}
                      </span>
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums">
                      ${p.inputPerM.toFixed(3)}
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums">
                      ${p.outputPerM.toFixed(3)}
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums">
                      ${p.cacheReadPerM.toFixed(3)}
                    </td>
                    <td className="px-3 py-1.5 text-right">
                      <div className="inline-flex items-center gap-2">
                        <button
                          type="button"
                          onClick={() => {
                            onClearInitial();
                            setModel(p.model);
                            setInputPerM(String(p.inputPerM));
                            setOutputPerM(String(p.outputPerM));
                            setCacheReadPerM(String(p.cacheReadPerM));
                          }}
                          className="text-primary hover:underline"
                        >
                          Edit
                        </button>
                        {p.custom && (
                          <button
                            type="button"
                            aria-label={`Reset price for ${p.model}`}
                            onClick={() =>
                              save.mutate({
                                model: p.model,
                                inputPerM: 0,
                                outputPerM: 0,
                                cacheReadPerM: 0,
                                reset: true,
                              })
                            }
                            className="inline-flex items-center gap-0.5 text-muted-foreground hover:text-foreground"
                          >
                            <RotateCcw className="size-3" />
                            Reset
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </section>
  );
}
