import { useQuery } from '@tanstack/react-query';
import { providersQuery } from '@/api/queries';
import { Button } from '@/components/ui/button';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
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

  return (
    <main className="mx-auto flex min-h-screen max-w-sm items-center p-4">
      <Card className="w-full">
        <CardHeader>
          <CardTitle className="text-xl">Sign in to agenthold</CardTitle>
          <CardDescription>Choose a provider to continue.</CardDescription>
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
              No sign-in providers are configured. Ask your administrator to set
              one up.
            </p>
          )}
          {data?.map((p) => (
            <Button key={p.id} asChild variant="outline">
              <a href={loginHref(p.id, redirect)}>Continue with {p.name}</a>
            </Button>
          ))}
        </CardContent>
      </Card>
    </main>
  );
}
