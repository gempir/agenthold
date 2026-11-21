import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import { adminUsersQuery, meQuery, type AdminUser } from '@/api/queries';
import { Avatar } from '@/components/Avatar';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Card } from '@/components/ui/card';
import { Select } from '@/components/ui/select';
import { ROLE_LABEL, type Role } from '@/lib/roles';
import { relTime, useNow } from '@/lib/time';

/**
 * The users of this agenthold and their roles. An admin can override the
 * role configuration gives a user; the override wins until it is cleared.
 */
export function UsersAdmin() {
  const users = useQuery(adminUsersQuery);
  const myId = useQuery(meQuery).data?.user.id;
  const now = useNow(60_000);
  return (
    <section aria-labelledby="users-heading" className="grid gap-3">
      <div>
        <h2 id="users-heading" className="text-base font-semibold">
          Users
        </h2>
        <p className="text-sm text-muted-foreground">
          Roles come from the sign-in configuration (see the auth docs). An
          override here wins over it and applies at the user's next request.
        </p>
      </div>
      {users.isPending && <Loading />}
      <ErrorText error={users.error} />
      {users.data && (
        <Card className="overflow-x-auto p-0">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-muted-foreground">
              <tr className="border-b">
                <th className="px-3 py-2 font-medium">User</th>
                <th className="px-3 py-2 font-medium">Signs in with</th>
                <th className="px-3 py-2 font-medium">Agents</th>
                <th className="px-3 py-2 font-medium">Last sign-in</th>
                <th className="px-3 py-2 font-medium">Role</th>
              </tr>
            </thead>
            <tbody>
              {users.data.map((u) => (
                <UserRow
                  key={u.user.id}
                  u={u}
                  self={u.user.id === myId}
                  now={now}
                />
              ))}
            </tbody>
          </table>
        </Card>
      )}
    </section>
  );
}

function UserRow({
  u,
  self,
  now,
}: {
  u: AdminUser;
  self: boolean;
  now: number;
}) {
  const qc = useQueryClient();
  const set = useMutation({
    mutationFn: (override: Role | null) =>
      unwrap(
        api.PUT('/admin/users/{userId}/role', {
          params: { path: { userId: u.user.id } },
          body: { override },
        }),
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: adminUsersQuery.queryKey }),
  });
  const locked = u.local || self;
  return (
    <tr className="border-b last:border-0">
      <td className="px-3 py-2">
        <div className="flex items-center gap-2">
          <Avatar name={u.user.name} url={u.user.avatarUrl} size="sm" />
          <div className="grid min-w-0">
            <span className="truncate">
              {u.user.name}
              {self && <span className="text-muted-foreground"> (you)</span>}
            </span>
            <span className="truncate text-xs text-muted-foreground">
              {u.user.email}
            </span>
          </div>
        </div>
      </td>
      <td className="px-3 py-2 text-muted-foreground">
        {u.provider}
        {u.username && ` (${u.username})`}
      </td>
      <td className="px-3 py-2">{u.agents}</td>
      <td className="px-3 py-2 text-muted-foreground">
        {u.lastLoginAt ? relTime(u.lastLoginAt, now) : 'never'}
      </td>
      <td className="px-3 py-2">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={u.role === 'disabled' ? 'outline' : 'secondary'}>
            {ROLE_LABEL[u.role]}
          </Badge>
          <Select
            aria-label={`Role of ${u.user.name}`}
            className="h-8 w-auto"
            value={u.override ?? ''}
            disabled={locked || set.isPending}
            title={
              u.local
                ? 'The local admin is always an admin'
                : self
                  ? 'Another admin can change your role'
                  : undefined
            }
            onChange={(e) =>
              set.mutate(e.target.value ? (e.target.value as Role) : null)
            }
          >
            <option value="">
              From configuration ({ROLE_LABEL[u.configRole]})
            </option>
            <option value="admin">Override: Admin</option>
            <option value="user">Override: User</option>
            <option value="disabled">Override: Disabled</option>
          </Select>
        </div>
        <ErrorText error={set.error} />
      </td>
    </tr>
  );
}
