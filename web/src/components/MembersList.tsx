import type { Member, WorkspaceRole } from '@/api/queries';
import { canManage } from '@/api/queries';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Select } from '@/components/ui/select';

export function Avatar({ name, url }: { name: string; url?: string }) {
  if (url) {
    return (
      <img
        src={url}
        alt=""
        className="size-8 rounded-full bg-muted object-cover"
      />
    );
  }
  return (
    <span
      aria-hidden
      className="flex size-8 items-center justify-center rounded-full bg-muted text-xs font-medium"
    >
      {name.slice(0, 1).toUpperCase()}
    </span>
  );
}

interface Props {
  members: Member[];
  myId: string;
  myRole: WorkspaceRole;
  personal: boolean;
  onChangeRole: (userId: string, role: WorkspaceRole) => void;
  onRemove: (userId: string) => void;
  onLeave: () => void;
  busy?: boolean;
}

export function MembersList({
  members,
  myId,
  myRole,
  personal,
  onChangeRole,
  onRemove,
  onLeave,
  busy,
}: Props) {
  const manager = canManage(myRole);
  return (
    <ul className="divide-y rounded-lg border">
      {members.map((m) => {
        const self = m.user.id === myId;
        // Admins may not touch owners; only owners can.
        const editable =
          manager && !self && (myRole === 'owner' || m.role !== 'owner');
        return (
          <li key={m.user.id} className="flex flex-wrap items-center gap-3 p-3">
            <Avatar name={m.user.name} url={m.user.avatarUrl} />
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-medium">
                {m.user.name}
                {self && (
                  <span className="ml-2 text-xs text-muted-foreground">
                    (you)
                  </span>
                )}
              </div>
              <div className="truncate text-xs text-muted-foreground">
                {m.user.email}
              </div>
            </div>
            {editable ? (
              <>
                <Select
                  aria-label={`Role for ${m.user.name}`}
                  value={m.role}
                  disabled={busy}
                  onChange={(e) =>
                    onChangeRole(m.user.id, e.target.value as WorkspaceRole)
                  }
                >
                  <option value="owner">owner</option>
                  <option value="admin">admin</option>
                  <option value="member">member</option>
                </Select>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy}
                  onClick={() => onRemove(m.user.id)}
                >
                  Remove
                </Button>
              </>
            ) : (
              <Badge>{m.role}</Badge>
            )}
            {self && !personal && (
              <Button
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={onLeave}
              >
                Leave
              </Button>
            )}
          </li>
        );
      })}
    </ul>
  );
}
