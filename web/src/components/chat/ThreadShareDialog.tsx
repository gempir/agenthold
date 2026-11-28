import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Check,
  Copy,
  Link2,
  Send,
  Share2,
  Trash2,
  UserPlus,
  Users,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  meQuery,
  sessionGuestLinksQuery,
  sessionSharesQuery,
  type GuestLinkCreated,
  type SessionShareRole,
} from '@/api/queries';
import { Avatar } from '@/components/Avatar';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';

export function ThreadShareDialog({
  sessionId,
  sessionTitle,
  isOwner,
  canPrompt,
  open,
  onOpenChange,
}: {
  sessionId: string;
  sessionTitle?: string;
  isOwner: boolean;
  canPrompt: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const qc = useQueryClient();
  const me = useQuery(meQuery);
  const shares = useQuery({
    ...sessionSharesQuery(sessionId),
    enabled: open,
  });
  const guestLinks = useQuery({
    ...sessionGuestLinksQuery(sessionId),
    enabled: open && isOwner,
  });

  const [email, setEmail] = useState('');
  const [role, setRole] = useState<SessionShareRole>('viewer');
  const [createdGuest, setCreatedGuest] = useState<GuestLinkCreated | null>(
    null,
  );
  const [copied, setCopied] = useState(false);
  const [handoffTarget, setHandoffTarget] = useState('');
  const [handoffNote, setHandoffNote] = useState('');
  const [handoffDone, setHandoffDone] = useState<string | null>(null);

  const addShare = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/sessions/{sessionId}/shares', {
          params: { path: { sessionId } },
          body: { email: email.trim(), role },
        }),
      ),
    onSuccess: async () => {
      setEmail('');
      await qc.invalidateQueries({
        queryKey: sessionSharesQuery(sessionId).queryKey,
      });
    },
  });

  const removeShare = useMutation({
    mutationFn: (userId: string) =>
      unwrap(
        api.DELETE('/sessions/{sessionId}/shares/{userId}', {
          params: { path: { sessionId, userId } },
        }),
      ),
    onSuccess: async () => {
      await qc.invalidateQueries({
        queryKey: sessionSharesQuery(sessionId).queryKey,
      });
    },
  });

  const createGuestLink = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/sessions/{sessionId}/guest-links', {
          params: { path: { sessionId } },
        }),
      ),
    onSuccess: async (res) => {
      setCreatedGuest(res);
      setCopied(false);
      await qc.invalidateQueries({
        queryKey: sessionGuestLinksQuery(sessionId).queryKey,
      });
    },
  });

  const revokeGuestLink = useMutation({
    mutationFn: (linkId: string) =>
      unwrap(
        api.DELETE('/sessions/{sessionId}/guest-links/{linkId}', {
          params: { path: { sessionId, linkId } },
        }),
      ),
    onSuccess: async (_, linkId) => {
      if (createdGuest?.link.id === linkId) {
        setCreatedGuest(null);
      }
      await qc.invalidateQueries({
        queryKey: sessionGuestLinksQuery(sessionId).queryKey,
      });
    },
  });

  const sendHandoff = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/sessions/{sessionId}/handoff', {
          params: { path: { sessionId } },
          body: {
            target: handoffTarget.trim(),
            ...(handoffNote.trim() ? { note: handoffNote.trim() } : {}),
          },
        }),
      ),
    onSuccess: (res) => {
      setHandoffDone(res.message);
      setHandoffTarget('');
      setHandoffNote('');
    },
  });

  const fullGuestUrl = createdGuest
    ? `${globalThis.location?.origin ?? ''}${createdGuest.url}`
    : '';

  const copyGuestUrl = async () => {
    if (!fullGuestUrl) return;
    try {
      await globalThis.navigator?.clipboard?.writeText(fullGuestUrl);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // clipboard unavailable
    }
  };

  const myId = me.data?.user.id;
  const activeLinks = (guestLinks.data ?? []).filter((l) => l.active);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Share2 className="size-4" aria-hidden />
            Share thread
          </DialogTitle>
          <DialogDescription>
            {sessionTitle
              ? `Share "${sessionTitle}" without exposing the rest of the agent.`
              : 'Share this single thread with teammates or a 24-hour guest link.'}
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-5">
          {/* Teammates */}
          <section className="grid gap-2.5">
            <h3 className="flex items-center gap-1.5 text-xs font-medium tracking-wide text-muted-foreground uppercase">
              <Users className="size-3.5" aria-hidden />
              People with thread access
            </h3>

            {isOwner && (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  if (email.trim()) addShare.mutate();
                }}
                className="flex flex-wrap gap-2"
              >
                <Input
                  type="email"
                  aria-label="Teammate email"
                  placeholder="teammate@example.com"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  className="h-8 min-w-0 flex-1 text-sm"
                />
                <Select
                  aria-label="Thread role"
                  value={role}
                  onChange={(e) => setRole(e.target.value as SessionShareRole)}
                  className="h-8 w-34 text-xs"
                >
                  <option value="viewer">Viewer</option>
                  <option value="collaborator">Collaborator</option>
                </Select>
                <Button
                  type="submit"
                  size="sm"
                  className="h-8"
                  disabled={!email.trim() || addShare.isPending}
                >
                  <UserPlus className="size-3.5" aria-hidden />
                  Share
                </Button>
              </form>
            )}
            <ErrorText error={addShare.error ?? removeShare.error} />

            {shares.isPending ? (
              <Loading />
            ) : shares.error ? (
              <ErrorText error={shares.error} />
            ) : (
              <ul
                aria-label="Thread shares"
                className="grid gap-1.5 rounded-lg border p-2 text-sm"
              >
                {shares.data?.owner && (
                  <li className="flex items-center justify-between gap-2 px-1 py-0.5">
                    <span className="flex min-w-0 items-center gap-2">
                      <Avatar
                        name={shares.data.owner.name}
                        url={shares.data.owner.avatarUrl}
                        size="sm"
                      />
                      <span className="min-w-0 truncate">
                        {shares.data.owner.name}
                      </span>
                    </span>
                    <span className="shrink-0 text-xs text-muted-foreground">
                      Owner
                    </span>
                  </li>
                )}
                {shares.data?.shares.map((s) => (
                  <li
                    key={s.user.id}
                    className="flex items-center justify-between gap-2 px-1 py-0.5"
                  >
                    <span className="flex min-w-0 items-center gap-2">
                      <Avatar
                        name={s.user.name}
                        url={s.user.avatarUrl}
                        size="sm"
                      />
                      <span className="min-w-0">
                        <span className="block truncate">{s.user.name}</span>
                        <span className="block truncate text-xs text-muted-foreground">
                          {s.user.email}
                        </span>
                      </span>
                    </span>
                    <span className="flex shrink-0 items-center gap-1.5">
                      <span className="rounded-full border px-2 py-0.5 text-xs text-muted-foreground capitalize">
                        {s.role}
                      </span>
                      {(isOwner || s.user.id === myId) && (
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          className="size-7 text-muted-foreground hover:text-destructive"
                          aria-label={
                            s.user.id === myId
                              ? 'Leave thread'
                              : `Remove ${s.user.name}`
                          }
                          disabled={removeShare.isPending}
                          onClick={() => removeShare.mutate(s.user.id)}
                        >
                          <Trash2 className="size-3.5" />
                        </Button>
                      )}
                    </span>
                  </li>
                ))}
                {shares.data && shares.data.shares.length === 0 && (
                  <li className="px-1 py-1 text-xs text-muted-foreground">
                    Not shared with anyone else yet. Viewers can read and add
                    review comments; Collaborators can also prompt and approve
                    guest messages.
                  </li>
                )}
              </ul>
            )}
          </section>

          {/* Guest link */}
          {isOwner && (
            <section className="grid gap-2.5 border-t pt-4">
              <div className="flex items-center justify-between gap-2">
                <h3 className="flex items-center gap-1.5 text-xs font-medium tracking-wide text-muted-foreground uppercase">
                  <Link2 className="size-3.5" aria-hidden />
                  Guest link (24h read-only + request queue)
                </h3>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  className="h-7 text-xs"
                  disabled={createGuestLink.isPending}
                  onClick={() => createGuestLink.mutate()}
                >
                  Create guest link
                </Button>
              </div>
              <ErrorText
                error={createGuestLink.error ?? revokeGuestLink.error}
              />

              {createdGuest && (
                <div
                  data-testid="guest-link-created"
                  className="grid gap-1.5 rounded-lg border border-emerald-500/40 bg-emerald-500/5 p-2.5 text-xs"
                >
                  <p className="font-medium text-foreground">
                    Copy this link now — the raw token is shown only once:
                  </p>
                  <div className="flex items-center gap-1.5">
                    <Input
                      readOnly
                      aria-label="Guest link URL"
                      value={fullGuestUrl}
                      className="h-7 font-mono text-xs"
                    />
                    <Button
                      type="button"
                      size="sm"
                      variant="secondary"
                      className="h-7 shrink-0"
                      onClick={() => void copyGuestUrl()}
                    >
                      {copied ? (
                        <Check className="size-3.5" aria-hidden />
                      ) : (
                        <Copy className="size-3.5" aria-hidden />
                      )}
                      {copied ? 'Copied' : 'Copy'}
                    </Button>
                  </div>
                </div>
              )}

              {activeLinks.length > 0 && (
                <ul
                  aria-label="Active guest links"
                  className="grid gap-1.5 text-xs"
                >
                  {activeLinks.map((l) => (
                    <li
                      key={l.id}
                      className="flex items-center justify-between gap-2 rounded-md border px-2.5 py-1.5"
                    >
                      <span className="text-muted-foreground">
                        Active · expires{' '}
                        {new Date(l.expiresAt).toLocaleString()}
                      </span>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        className="h-6 px-2 text-xs text-destructive hover:text-destructive"
                        disabled={revokeGuestLink.isPending}
                        onClick={() => revokeGuestLink.mutate(l.id)}
                      >
                        Revoke
                      </Button>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          )}

          {/* @teammate handoff */}
          {canPrompt && (
            <section className="grid gap-2 border-t pt-4">
              <h3 className="flex items-center gap-1.5 text-xs font-medium tracking-wide text-muted-foreground uppercase">
                <Send className="size-3.5" aria-hidden />
                Hand off to teammate
              </h3>
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  if (handoffTarget.trim()) sendHandoff.mutate();
                }}
                className="grid gap-2"
              >
                <div className="flex gap-2">
                  <Input
                    aria-label="Hand off to"
                    placeholder="@teammate name or email"
                    value={handoffTarget}
                    onChange={(e) => setHandoffTarget(e.target.value)}
                    className="h-8 flex-1 text-sm"
                  />
                  <Button
                    type="submit"
                    size="sm"
                    variant="secondary"
                    className="h-8"
                    disabled={!handoffTarget.trim() || sendHandoff.isPending}
                  >
                    Hand off
                  </Button>
                </div>
                <Input
                  aria-label="Handoff note"
                  placeholder="Optional note (e.g. Ready for your review on the diff)"
                  value={handoffNote}
                  onChange={(e) => setHandoffNote(e.target.value)}
                  className="h-8 text-xs"
                />
              </form>
              <ErrorText error={sendHandoff.error} />
              {handoffDone && (
                <p
                  role="status"
                  className="text-xs text-emerald-600 dark:text-emerald-400"
                >
                  {handoffDone}
                </p>
              )}
            </section>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
