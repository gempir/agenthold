import { ShieldOff } from 'lucide-react';
import type { User } from '@/api/queries';
import { Button } from '@/components/ui/button';
import { useLogout } from '@/hooks/useLogout';

/** What a Disabled user sees: signed in, but no access to anything. */
export function NoAccess({ user }: { user: User }) {
  const logout = useLogout();
  return (
    <div className="flex h-dvh items-center justify-center p-6">
      <div className="grid max-w-md gap-4 text-center">
        <ShieldOff
          className="mx-auto size-8 text-muted-foreground"
          aria-hidden
        />
        <h1 className="text-lg font-semibold">No access</h1>
        <p className="text-sm text-muted-foreground">
          Your account has no access to this agenthold. Ask an admin to give it
          a role.
        </p>
        <p className="text-sm">
          Signed in as{' '}
          <span className="font-medium">{user.email || user.name}</span>
        </p>
        <div>
          <Button variant="outline" onClick={() => logout.mutate()}>
            Sign out
          </Button>
        </div>
      </div>
    </div>
  );
}
