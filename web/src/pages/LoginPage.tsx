import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import { providersQuery } from '@/api/queries';
import { Button } from '@/components/ui/button';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { ErrorText, Loading } from '@/components/Spinner';

/** Only same-origin absolute paths are allowed as post-login targets. */
export function safeRedirect(r: string | undefined): string {
  if (!r || !r.startsWith('/') || r.startsWith('//') || r.startsWith('/\\'))
    return '/';
  return r;
}

export function loginHref(
  providerId: string,
  redirect: string | undefined,
): string {
  return `/api/v1/auth/login/${encodeURIComponent(providerId)}?redirect=${encodeURIComponent(safeRedirect(redirect))}`;
}

export function LoginPage({
  redirect,
  error,
}: {
  redirect?: string;
  error?: string;
}) {
  const { data, isPending, error: loadError } = useQuery(providersQuery);
  const local = data?.some((p) => p.type === 'local');
  const others = data?.filter((p) => p.type !== 'local') ?? [];

  return (
    <main className="mx-auto flex min-h-screen max-w-sm items-center p-4">
      <Card className="w-full">
        <CardHeader>
          <CardTitle className="text-xl">Sign in to agenthold</CardTitle>
          {data && data.length > 0 && (
            <CardDescription>
              {local && others.length === 0
                ? 'Sign in with your username and password.'
                : 'Choose how to continue.'}
            </CardDescription>
          )}
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          {isPending && <Loading />}
          <ErrorText error={loadError} />
          {data && data.length === 0 && (
            <p className="text-sm text-muted-foreground">
              Sign-in is not set up on this server. Its configuration needs a
              local admin (auth.local) or an identity provider.
            </p>
          )}
          {local && <LocalLoginForm redirect={redirect} />}
          {local && others.length > 0 && (
            <p className="text-center text-xs text-muted-foreground">or</p>
          )}
          {others.map((p) => (
            <Button key={p.id} asChild variant="outline">
              <a href={loginHref(p.id, redirect)}>Continue with {p.name}</a>
            </Button>
          ))}
        </CardContent>
      </Card>
    </main>
  );
}

function LocalLoginForm({ redirect }: { redirect?: string }) {
  const qc = useQueryClient();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const login = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/auth/local', { body: { username, password } })),
    onSuccess: () => {
      qc.clear();
      // A full load: the app starts fresh with the new session cookie.
      globalThis.location.assign(safeRedirect(redirect));
    },
  });
  return (
    <form
      className="grid gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        login.mutate();
      }}
    >
      <label className="grid gap-1 text-sm">
        Username
        <Input
          autoComplete="username"
          required
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
      </label>
      <label className="grid gap-1 text-sm">
        Password
        <Input
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      </label>
      <ErrorText error={login.error} />
      <Button type="submit" disabled={login.isPending}>
        Sign in
      </Button>
    </form>
  );
}
