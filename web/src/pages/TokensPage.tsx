import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

const key = ['me', 'tokens'];

/** Personal access tokens for scripts and other API clients. */
export function TokensPage() {
  const qc = useQueryClient();
  const tokens = useQuery({
    queryKey: key,
    queryFn: () => unwrap(api.GET('/me/tokens')),
  });
  const [name, setName] = useState('');
  const [days, setDays] = useState('90');
  const [created, setCreated] = useState('');
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/me/tokens', {
          body: { name, ...(days && { expiresInDays: Number(days) }) },
        }),
      ),
    onSuccess: async (t) => {
      setCreated(t.token);
      setName('');
      await qc.invalidateQueries({ queryKey: key });
    },
  });
  const revoke = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/me/tokens/{tokenId}', {
          params: { path: { tokenId: id } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });

  return (
    <div className="flex max-w-3xl flex-col gap-6">
      <header>
        <h1 className="text-xl font-semibold">API tokens</h1>
        <p className="text-sm text-muted-foreground">
          A token acts as you, with your permissions. Send it as{' '}
          <code>Authorization: Bearer &lt;token&gt;</code>. See the{' '}
          <a
            className="underline"
            href="/api/docs"
            target="_blank"
            rel="noreferrer"
          >
            API reference
          </a>
          .
        </p>
      </header>
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <label className="grid gap-1 text-sm">
          Name
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="CI"
            required
          />
        </label>
        <label className="grid gap-1 text-sm">
          Expires in (days, empty = never)
          <Input
            type="number"
            min={1}
            value={days}
            onChange={(e) => setDays(e.target.value)}
          />
        </label>
        <Button type="submit" disabled={create.isPending}>
          Create token
        </Button>
      </form>
      <ErrorText error={create.error ?? revoke.error} />
      {created && (
        <div
          className="rounded-lg border border-amber-500/50 bg-amber-500/10 p-3 text-sm"
          role="status"
        >
          Copy the token now; it is not shown again:
          <code className="mt-2 block break-all rounded bg-background p-2 font-mono text-xs">
            {created}
          </code>
          <Button
            className="mt-2"
            size="sm"
            variant="outline"
            onClick={() => void navigator.clipboard?.writeText(created)}
          >
            Copy
          </Button>
        </div>
      )}
      {tokens.isPending && <Loading />}
      <ul aria-label="Tokens" className="divide-y rounded-lg border">
        {tokens.data?.length === 0 && (
          <li className="p-3 text-sm text-muted-foreground">No tokens.</li>
        )}
        {tokens.data?.map((t) => (
          <li key={t.id} className="flex items-center gap-3 p-3 text-sm">
            <span className="font-medium">{t.name}</span>
            <code className="text-xs text-muted-foreground">{t.prefix}…</code>
            <span className="text-xs text-muted-foreground">
              {t.lastUsedAt
                ? `used ${new Date(t.lastUsedAt).toLocaleString()}`
                : 'never used'}
              {t.expiresAt &&
                ` · expires ${new Date(t.expiresAt).toLocaleDateString()}`}
            </span>
            <Button
              className="ml-auto"
              size="sm"
              variant="ghost"
              onClick={() => {
                if (confirm(`Revoke ${t.name}?`)) revoke.mutate(t.id);
              }}
            >
              Revoke
            </Button>
          </li>
        ))}
      </ul>
    </div>
  );
}
