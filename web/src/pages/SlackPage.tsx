import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import { meQuery } from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { SettingsHeader } from '@/components/layout/SettingsLayout';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

const linksKey = ['me', 'slack'];
const settingsKey = ['admin', 'slack'];

/** The URL that creates the Slack app from agenthold's manifest. */
export function createAppUrl(manifest: unknown): string {
  return `https://api.slack.com/apps?new_app=1&manifest_json=${encodeURIComponent(JSON.stringify(manifest))}`;
}

/**
 * Slack in account settings: the Slack users linked to you, and, for admins,
 * the connection of this installation to a workspace.
 */
export function SlackPage() {
  const me = useQuery(meQuery);
  return (
    <>
      <SettingsHeader
        title="Slack"
        description="Chat with your agents from Slack. Everything a linked Slack user does runs as you."
      />
      <div className="grid gap-10">
        <SlackLinks />
        {me.data?.admin && <SlackAdmin />}
      </div>
    </>
  );
}

/** The Slack users linked to the signed-in account. */
export function SlackLinks() {
  const qc = useQueryClient();
  const links = useQuery({
    queryKey: linksKey,
    queryFn: () => unwrap(api.GET('/me/slack')),
  });
  const unlink = useMutation({
    mutationFn: (l: { teamId: string; slackUserId: string }) =>
      unwrap(
        api.DELETE('/me/slack/{teamId}/{slackUserId}', {
          params: { path: l },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: linksKey }),
  });
  return (
    <section className="grid gap-3" aria-labelledby="slack-links">
      <h2 id="slack-links" className="text-sm font-medium">
        Your Slack users
      </h2>
      <ErrorText error={links.error ?? unlink.error} />
      {links.isPending && <Loading />}
      {links.data?.length === 0 && (
        <p className="text-sm text-muted-foreground">
          No Slack user is linked to your account. To link one, send a message
          to the agenthold bot in Slack or run <code>/agenthold link</code>,
          then open the link it answers with.
        </p>
      )}
      {links.data && links.data.length > 0 && (
        <ul
          aria-label="Linked Slack users"
          className="divide-y rounded-lg border"
        >
          {links.data.map((l) => (
            <li
              key={`${l.teamId}/${l.slackUserId}`}
              className="flex flex-wrap items-center gap-x-3 gap-y-1 p-3 text-sm"
            >
              <span className="font-medium">@{l.slackUserName}</span>
              <span className="text-muted-foreground">in {l.teamName}</span>
              <span className="text-xs text-muted-foreground">
                linked {new Date(l.createdAt).toLocaleDateString()}
              </span>
              <Button
                className="ml-auto"
                size="sm"
                variant="ghost"
                disabled={unlink.isPending}
                aria-label={`Unlink @${l.slackUserName}`}
                onClick={() => {
                  if (
                    confirm(
                      `Unlink @${l.slackUserName} in ${l.teamName}? It can no longer act as you.`,
                    )
                  )
                    unlink.mutate(l);
                }}
              >
                Unlink
              </Button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** Connect or disconnect the Slack workspace. Admins only (the API enforces it). */
export function SlackAdmin() {
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: settingsKey,
    queryFn: () => unwrap(api.GET('/admin/slack')),
  });
  const manifest = useQuery({
    queryKey: [...settingsKey, 'manifest'],
    queryFn: () => unwrap(api.GET('/admin/slack/manifest')),
  });
  const [botToken, setBotToken] = useState('');
  const [appToken, setAppToken] = useState('');
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/admin/slack', {
          body: { botToken: botToken.trim(), appToken: appToken.trim() },
        }),
      ),
    onSuccess: async (s) => {
      setBotToken('');
      setAppToken('');
      qc.setQueryData(settingsKey, s);
      await qc.invalidateQueries({ queryKey: settingsKey });
    },
  });
  const disconnect = useMutation({
    mutationFn: () => unwrap(api.DELETE('/admin/slack')),
    onSuccess: () => qc.invalidateQueries({ queryKey: settingsKey }),
  });
  const manifestJson = manifest.data
    ? JSON.stringify(manifest.data, null, 2)
    : '';
  const s = settings.data;

  return (
    <section className="grid gap-4" aria-labelledby="slack-admin">
      <h2 id="slack-admin" className="text-sm font-medium">
        Workspace connection
      </h2>
      <ErrorText error={settings.error ?? manifest.error ?? disconnect.error} />
      {settings.isPending && <Loading />}
      {s && (
        <div
          className="rounded-xl border p-4 text-sm"
          aria-label="Slack status"
        >
          <div className="flex flex-wrap items-center gap-2">
            <strong>
              {s.connected
                ? 'Connected'
                : s.configured
                  ? 'Not connected'
                  : 'Not configured'}
            </strong>
            <span
              className={
                s.connected
                  ? 'text-emerald-700 dark:text-emerald-400'
                  : s.configured
                    ? 'text-destructive'
                    : 'text-muted-foreground'
              }
            >
              {s.connected
                ? 'The Socket Mode connection is up.'
                : s.configured
                  ? 'The tokens are stored but the connection is down.'
                  : 'No Slack app is connected yet.'}
            </span>
            {s.configured && (
              <Button
                className="ml-auto"
                size="sm"
                variant="ghost"
                disabled={disconnect.isPending}
                onClick={() => {
                  if (
                    confirm(
                      'Disconnect Slack? The tokens are forgotten; linked users and threads stay for a later reconnect.',
                    )
                  )
                    disconnect.mutate();
                }}
              >
                Disconnect
              </Button>
            )}
          </div>
          {s.configured && (
            <dl className="mt-3 grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1">
              <dt className="text-muted-foreground">Workspace</dt>
              <dd>{s.teamName || s.teamId || 'Unknown'}</dd>
              <dt className="text-muted-foreground">Bot user</dt>
              <dd>{s.botUserId || 'Unknown'}</dd>
            </dl>
          )}
          {s.lastError && (
            <p role="alert" className="mt-3 text-destructive">
              Last error: {s.lastError}
            </p>
          )}
        </div>
      )}

      <ol className="grid list-decimal gap-4 pl-5 text-sm">
        <li className="grid gap-2">
          <span>
            Create the Slack app from agenthold&apos;s manifest. It sets up the
            bot user, Socket Mode, the <code>/agenthold</code> command, the bot
            scopes and the events.
          </span>
          <div className="flex flex-wrap gap-2">
            {manifest.data ? (
              <Button asChild size="sm">
                <a
                  href={createAppUrl(manifest.data)}
                  target="_blank"
                  rel="noreferrer"
                >
                  Create Slack app
                </a>
              </Button>
            ) : (
              <Button size="sm" disabled>
                Create Slack app
              </Button>
            )}
            <Button
              size="sm"
              variant="outline"
              disabled={!manifestJson}
              onClick={() => void navigator.clipboard?.writeText(manifestJson)}
            >
              Copy manifest
            </Button>
          </div>
          {manifestJson && (
            <details>
              <summary className="cursor-pointer text-xs text-muted-foreground">
                Show manifest
              </summary>
              <pre
                aria-label="Slack app manifest"
                className="mt-2 max-h-64 overflow-auto rounded bg-muted p-2 font-mono text-xs"
              >
                {manifestJson}
              </pre>
            </details>
          )}
        </li>
        <li>
          Install the app to your workspace (<strong>Install App</strong>) and
          copy the <strong>Bot User OAuth Token</strong> (<code>xoxb-…</code>).
        </li>
        <li>
          Under <strong>Basic Information → App-Level Tokens</strong>, create a
          token with the <code>connections:write</code> scope and copy it (
          <code>xapp-…</code>).
        </li>
        <li>Paste both tokens here.</li>
      </ol>

      <form
        className="grid max-w-md gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <label className="grid gap-1 text-sm">
          Bot User OAuth Token
          <Input
            type="password"
            autoComplete="off"
            placeholder="xoxb-…"
            required
            value={botToken}
            onChange={(e) => setBotToken(e.target.value)}
          />
        </label>
        <label className="grid gap-1 text-sm">
          App-level token
          <Input
            type="password"
            autoComplete="off"
            placeholder="xapp-…"
            required
            value={appToken}
            onChange={(e) => setAppToken(e.target.value)}
          />
        </label>
        <ErrorText error={save.error} />
        <div>
          <Button type="submit" disabled={save.isPending}>
            {s?.configured ? 'Replace tokens' : 'Save'}
          </Button>
        </div>
      </form>
    </section>
  );
}
