import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import type { Workspace } from '@/api/queries';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { ErrorText } from '@/components/Spinner';

export function SettingsPanel({ workspace }: { workspace: Workspace }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState(workspace.name);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const manager = workspace.role === 'owner' || workspace.role === 'admin';

  const rename = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/workspaces/{workspaceId}', {
          params: { path: { workspaceId: workspace.id } },
          body: { name: name.trim() },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['workspaces'] }),
  });

  const remove = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE('/workspaces/{workspaceId}', {
          params: { path: { workspaceId: workspace.id } },
        }),
      ),
    onSuccess: async () => {
      setConfirmOpen(false);
      qc.removeQueries({ queryKey: ['workspaces', workspace.id] });
      await qc.invalidateQueries({ queryKey: ['workspaces'] });
      await navigate({ to: '/' });
    },
  });

  return (
    <div className="flex flex-col gap-6">
      {manager ? (
        <form
          className="flex flex-col gap-3 rounded-lg border p-4"
          onSubmit={(e) => {
            e.preventDefault();
            rename.mutate();
          }}
        >
          <label className="flex flex-col gap-1 text-sm font-medium">
            Workspace name
            <Input
              value={name}
              maxLength={100}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </label>
          <ErrorText error={rename.error} />
          <div>
            <Button
              type="submit"
              disabled={
                rename.isPending ||
                !name.trim() ||
                name.trim() === workspace.name
              }
            >
              Rename
            </Button>
          </div>
        </form>
      ) : (
        <p className="text-sm text-muted-foreground">
          Only owners and admins can change workspace settings.
        </p>
      )}

      {workspace.role === 'owner' && !workspace.personal && (
        <div className="flex flex-col gap-3 rounded-lg border border-destructive/50 p-4">
          <h3 className="text-sm font-semibold text-destructive">
            Danger zone
          </h3>
          <p className="text-sm text-muted-foreground">
            Deleting a workspace removes it for all members.
          </p>
          <div>
            <Button variant="destructive" onClick={() => setConfirmOpen(true)}>
              Delete workspace
            </Button>
          </div>
          <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Delete {workspace.name}?</DialogTitle>
                <DialogDescription>This cannot be undone.</DialogDescription>
              </DialogHeader>
              <ErrorText error={remove.error} />
              <DialogFooter>
                <DialogClose asChild>
                  <Button variant="outline">Cancel</Button>
                </DialogClose>
                <Button
                  variant="destructive"
                  disabled={remove.isPending}
                  onClick={() => remove.mutate()}
                >
                  Delete
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </div>
      )}
    </div>
  );
}
