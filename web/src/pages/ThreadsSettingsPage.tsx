import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import { mySettingsQuery } from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { Select } from '@/components/ui/select';

const PRESETS = [1, 2, 3, 7, 14, 30, 90];

const daysLabel = (d: number) =>
  d === 0 ? 'Never' : d === 1 ? 'After 1 day' : `After ${d} days`;

/** Account settings for threads: when idle threads settle themselves. */
export function ThreadsSettingsPage() {
  const qc = useQueryClient();
  const settings = useQuery(mySettingsQuery);
  const save = useMutation({
    mutationFn: (threadAutoSettleDays: number) =>
      unwrap(api.PUT('/me/settings', { body: { threadAutoSettleDays } })),
    onSuccess: (s) => qc.setQueryData(mySettingsQuery.queryKey, s),
  });
  const current = settings.data?.threadAutoSettleDays;
  const options = [0, ...PRESETS];
  if (current != null && !options.includes(current)) options.push(current);
  options.sort((a, b) => a - b);

  return (
    <div className="flex max-w-3xl flex-col gap-6">
      <header>
        <h1 className="text-xl font-semibold">Threads</h1>
        <p className="text-sm text-muted-foreground">
          A settled thread is done: it moves out of the active list into
          Settled, and a new message makes it active again. You, people you
          share an agent with, and the agent itself can settle threads.
        </p>
      </header>
      {settings.isPending ? (
        <Loading />
      ) : (
        <section className="grid gap-2">
          <label className="grid max-w-xs gap-1 text-sm font-medium">
            Settle idle threads
            <Select
              value={current ?? 3}
              disabled={save.isPending || settings.isError}
              onChange={(e) => save.mutate(Number(e.target.value))}
            >
              {options.map((d) => (
                <option key={d} value={d}>
                  {daysLabel(d)}
                  {d === 3 ? ' (default)' : ''}
                </option>
              ))}
            </Select>
          </label>
          <p className="text-sm text-muted-foreground">
            Threads of the agents you own settle themselves after this long
            without a new message. It applies to every thread of your agents,
            also those that people you share them with started; their own
            setting applies to their agents.
          </p>
          {save.isSuccess && (
            <p role="status" className="text-sm text-muted-foreground">
              Saved.
            </p>
          )}
        </section>
      )}
      <ErrorText error={settings.error ?? save.error} />
    </div>
  );
}
