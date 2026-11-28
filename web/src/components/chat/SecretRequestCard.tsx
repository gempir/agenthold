import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { KeyRound, LockKeyhole, ShieldCheck, ShieldX } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { ErrorText } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import type { SecretRequestItem } from '@/lib/session';

/**
 * An agent's request_secret call, inline in the thread. The value goes
 * straight to the user's encrypted secret store; it is held only in this
 * input's state and never becomes part of the conversation.
 */
export function SecretRequestCard({
  item,
  sessionId,
  canUse,
}: {
  item: SecretRequestItem;
  sessionId: string;
  canUse: boolean;
}) {
  const [value, setValue] = useState('');
  const [local, setLocal] = useState<SecretRequestItem['resolved']>();
  const respond = useMutation({
    mutationFn: (body: { value: string } | { decline: true }) =>
      unwrap(
        api.POST('/sessions/{sessionId}/secret-requests/{requestId}', {
          params: { path: { sessionId, requestId: item.requestId } },
          body,
        }),
      ),
    onSuccess: (res) => {
      setValue('');
      setLocal({
        status: res.status,
        ...(res.name && { name: res.name }),
        ...(res.envVar && { envVar: res.envVar }),
      });
    },
    onError: () => setValue(''),
  });

  const resolved = item.resolved ?? local;
  if (resolved) {
    const name = resolved.name || item.name;
    return (
      <p
        role="status"
        className="flex items-center gap-2 py-1 text-sm text-muted-foreground"
      >
        {resolved.status === 'submitted' ? (
          <ShieldCheck
            className="size-3.5 shrink-0 text-emerald-500"
            aria-hidden
          />
        ) : (
          <ShieldX className="size-3.5 shrink-0" aria-hidden />
        )}
        <span className="min-w-0 truncate">
          {resolved.status === 'submitted' ? (
            <>
              Secret <code className="font-mono text-xs">{name}</code> saved
              securely
              {resolved.envVar && (
                <>
                  {' '}
                  as{' '}
                  <code className="font-mono text-xs">{resolved.envVar}</code>
                </>
              )}
            </>
          ) : (
            'Secret request declined'
          )}
        </span>
      </p>
    );
  }

  const field = `secret-${item.requestId}`;
  return (
    <div
      role="group"
      aria-label="Secret request"
      className="grid gap-3 rounded-xl border border-amber-500/40 bg-amber-500/5 p-3 text-sm"
    >
      <div className="flex items-start gap-2">
        <KeyRound
          className="mt-0.5 size-4 shrink-0 text-amber-500"
          aria-hidden
        />
        <div className="grid min-w-0 gap-1">
          <p>
            The agent needs a secret:{' '}
            <code className="font-mono font-semibold">{item.name}</code>
          </p>
          {item.description && (
            <p className="text-muted-foreground">{item.description}</p>
          )}
          {item.hosts.length > 0 && (
            <p className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
              Only sent to
              {item.hosts.map((h) => (
                <code
                  key={h}
                  className="rounded bg-muted px-1.5 py-0.5 font-mono"
                >
                  {h}
                </code>
              ))}
            </p>
          )}
          {item.envVar && (
            <p className="text-xs text-muted-foreground">
              Available to the agent as a placeholder in{' '}
              <code className="font-mono">{item.envVar}</code>
            </p>
          )}
        </div>
      </div>
      {canUse ? (
        <form
          className="grid gap-2 pl-6"
          onSubmit={(e) => {
            e.preventDefault();
            if (value) respond.mutate({ value });
          }}
        >
          <label htmlFor={field} className="sr-only">
            Value for {item.name}
          </label>
          <Input
            id={field}
            type="password"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            placeholder="Paste the value"
          />
          <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <LockKeyhole className="size-3 shrink-0" aria-hidden />
            Stored encrypted in your secrets. It never enters the chat or the
            sandbox.
          </p>
          <ErrorText error={respond.error} />
          <div className="flex flex-wrap gap-2">
            <Button
              type="submit"
              size="sm"
              disabled={!value || respond.isPending}
            >
              Save securely
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={respond.isPending}
              onClick={() => respond.mutate({ decline: true })}
            >
              Decline
            </Button>
          </div>
        </form>
      ) : (
        <p className="pl-6 text-xs text-muted-foreground">
          Waiting for the thread owner to provide it.
        </p>
      )}
    </div>
  );
}
