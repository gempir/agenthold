import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import { invitePreviewQuery } from '@/api/queries';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { ErrorText, Loading } from '@/components/Spinner';

export function InvitePage({ token }: { token: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const preview = useQuery(invitePreviewQuery(token));
  const accept = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/invites/{token}/accept', { params: { path: { token } } }),
      ),
    onSuccess: async (ws) => {
      await qc.invalidateQueries({ queryKey: ['workspaces'] });
      await navigate({ to: '/w/$workspaceId', params: { workspaceId: ws.id } });
    },
  });

  return (
    <main className="mx-auto flex min-h-screen max-w-sm items-center p-4">
      <Card className="w-full">
        <CardHeader>
          <CardTitle className="text-xl">Workspace invitation</CardTitle>
          <CardDescription>
            You have been invited to join a workspace.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {preview.isPending && <Loading />}
          <ErrorText error={preview.error} />
          {preview.data && (
            <>
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
                <dt className="text-muted-foreground">Workspace</dt>
                <dd className="font-medium">{preview.data.workspaceName}</dd>
                <dt className="text-muted-foreground">Role</dt>
                <dd>
                  <Badge>{preview.data.role}</Badge>
                </dd>
                <dt className="text-muted-foreground">Invited by</dt>
                <dd>{preview.data.invitedBy}</dd>
                <dt className="text-muted-foreground">Expires</dt>
                <dd>{new Date(preview.data.expiresAt).toLocaleString()}</dd>
              </dl>
              {preview.data.alreadyMember ? (
                <p className="text-sm">
                  You are already a member of this workspace.{' '}
                  <Link
                    to="/w/$workspaceId"
                    params={{ workspaceId: preview.data.workspaceId }}
                    className="underline"
                  >
                    Open {preview.data.workspaceName}
                  </Link>
                </p>
              ) : (
                <Button
                  onClick={() => accept.mutate()}
                  disabled={accept.isPending}
                >
                  Join workspace
                </Button>
              )}
              <ErrorText error={accept.error} />
            </>
          )}
        </CardContent>
      </Card>
    </main>
  );
}
