import { useInfiniteQuery } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import { Avatar } from '@/components/MembersList';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { describeDetails, humanizeAction } from '@/lib/audit';

const PAGE = 50;

export function ActivityTab({ agentId }: { agentId: string }) {
  const q = useInfiniteQuery({
    queryKey: ['agents', agentId, 'audit'],
    initialPageParam: undefined as number | undefined,
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/agents/{agentId}/audit', {
          params: {
            path: { agentId },
            query: { before: pageParam, limit: PAGE },
          },
        }),
      ),
    getNextPageParam: (last) =>
      last.length < PAGE ? undefined : last[last.length - 1]?.id,
    retry: false,
  });

  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorText error={q.error} />;
  const rows = q.data.pages.flat();

  return (
    <div className="grid gap-3">
      {rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">No activity yet.</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-left text-sm">
            <thead className="border-b text-xs text-muted-foreground">
              <tr>
                <th className="p-2 font-medium">Time</th>
                <th className="p-2 font-medium">User</th>
                <th className="p-2 font-medium">Action</th>
                <th className="p-2 font-medium">Target</th>
                <th className="p-2 font-medium">Details</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {rows.map((e) => (
                <tr key={e.id}>
                  <td className="whitespace-nowrap p-2 text-muted-foreground">
                    {new Date(e.at).toLocaleString()}
                  </td>
                  <td className="p-2">
                    <span className="flex items-center gap-2">
                      <Avatar
                        name={e.user.name}
                        url={e.user.avatarUrl}
                        size="sm"
                      />
                      {e.user.name}
                    </span>
                  </td>
                  <td className="p-2">{humanizeAction(e.action)}</td>
                  <td className="max-w-48 truncate p-2">{e.target}</td>
                  <td className="max-w-72 truncate p-2 text-muted-foreground">
                    {describeDetails(e)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {q.hasNextPage && (
        <div>
          <Button
            variant="outline"
            size="sm"
            disabled={q.isFetchingNextPage}
            onClick={() => q.fetchNextPage()}
          >
            Load more
          </Button>
        </div>
      )}
    </div>
  );
}
