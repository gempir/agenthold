import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ApiError, api, unwrap } from '@/api/client';
import { meQuery } from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';

/**
 * Where the Slack bot's "Connect your agenthold account" link lands. Signed-in
 * users (the app shell redirects to the login page otherwise) confirm which
 * Slack user to link to their account.
 */
export function SlackLinkPage({ code }: { code?: string }) {
  const qc = useQueryClient();
  const me = useQuery(meQuery);
  const preview = useQuery({
    queryKey: ['me', 'slack', 'link', code],
    queryFn: () =>
      unwrap(
        api.GET('/me/slack/links/{code}', {
          params: { path: { code: code! } },
        }),
      ),
    enabled: !!code,
    retry: false,
  });
  const link = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/me/slack/links', { body: { code: code! } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me', 'slack'] }),
  });

  const p = preview.data;
  let body;
  if (!code) {
    body = <Invalid />;
  } else if (link.isSuccess) {
    body = (
      <>
        <CardHeader>
          <CardTitle className="text-xl">Slack connected</CardTitle>
          <CardDescription>
            @{link.data.slackUserName} in {link.data.teamName} now acts as you.
            You can go back to Slack; the bot confirms in a direct message.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Button asChild variant="outline">
            <Link to="/settings/slack">Manage Slack links</Link>
          </Button>
        </CardContent>
      </>
    );
  } else if (preview.isPending) {
    body = <Loading />;
  } else if (preview.error) {
    body =
      preview.error instanceof ApiError && preview.error.status === 404 ? (
        <Invalid />
      ) : (
        <CardContent>
          <ErrorText error={preview.error} />
        </CardContent>
      );
  } else if (p) {
    body = (
      <>
        <CardHeader>
          <CardTitle className="text-xl">Connect Slack</CardTitle>
          <CardDescription>
            Connect Slack user <strong>@{p.slackUserName}</strong> in workspace{' '}
            <strong>{p.teamName}</strong> to your agenthold account
            {me.data?.user.name && <> ({me.data.user.name})</>}? Everything that
            Slack user does then runs as you, with your access.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3">
          <ErrorText error={link.error} />
          <div className="flex gap-2">
            <Button disabled={link.isPending} onClick={() => link.mutate()}>
              Connect
            </Button>
            <Button asChild variant="ghost">
              <Link to="/">Cancel</Link>
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            This link works once and expires{' '}
            {new Date(p.expiresAt).toLocaleTimeString()}.
          </p>
        </CardContent>
      </>
    );
  }

  return (
    <div className="flex flex-1 items-center justify-center p-4">
      <Card className="w-full max-w-md">{body}</Card>
    </div>
  );
}

function Invalid() {
  return (
    <>
      <CardHeader>
        <CardTitle className="text-xl">This link does not work</CardTitle>
        <CardDescription>
          The link is invalid, was already used or has expired (links work once,
          for 15 minutes). Message the bot in Slack or run{' '}
          <code>/agenthold link</code> to get a new one.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Button asChild variant="outline">
          <Link to="/">Back to agenthold</Link>
        </Button>
      </CardContent>
    </>
  );
}
