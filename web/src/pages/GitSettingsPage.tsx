import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import {
  gitSettingsQuery,
  type GitCredential,
  type GitSettings,
} from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

/** Host presets of the credential form: host and the username git expects. */
const PRESETS = [
  {
    id: 'github',
    label: 'GitHub',
    host: 'github.com',
    username: 'x-access-token',
  },
  { id: 'gitlab', label: 'GitLab', host: 'gitlab.com', username: 'oauth2' },
  { id: 'custom', label: 'Custom host', host: '', username: '' },
] as const;

/** Git in account settings: who commits are authored as, and HTTPS tokens. */
export function GitSettingsSection() {
  const settings = useQuery(gitSettingsQuery);
  return (
    <>
      {settings.isPending && <Loading />}
      <ErrorText error={settings.error} />
      {settings.data && (
        <div className="grid max-w-3xl gap-6">
          <AuthorCard settings={settings.data} />
          {settings.data.github.enabled && (
            <GitHubCard settings={settings.data} />
          )}
          <CredentialsCard settings={settings.data} />
        </div>
      )}
    </>
  );
}

function AuthorCard({ settings }: { settings: GitSettings }) {
  const qc = useQueryClient();
  const [name, setName] = useState(settings.authorName);
  const [email, setEmail] = useState(settings.authorEmail);
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/me/git', {
          body: { authorName: name.trim(), authorEmail: email.trim() },
        }),
      ),
    onSuccess: (s) => {
      qc.setQueryData(gitSettingsQuery.queryKey, s);
    },
  });
  const dirty =
    name.trim() !== settings.authorName ||
    email.trim() !== settings.authorEmail;
  return (
    <section
      aria-label="Author identity"
      className="grid gap-3 rounded-lg border p-4"
    >
      <div>
        <h2 className="text-sm font-semibold">Author identity</h2>
        <p className="text-sm text-muted-foreground">
          Commits made in your agents are authored with this name and email.
          Leave a field empty to use your account profile.
        </p>
      </div>
      <form
        className="grid gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <label className="grid gap-1 text-sm">
          Author name
          <Input
            value={name}
            placeholder={settings.defaultAuthorName}
            onChange={(e) => setName(e.target.value)}
            autoComplete="off"
          />
        </label>
        <label className="grid gap-1 text-sm">
          Author email
          <Input
            type="email"
            value={email}
            placeholder={settings.defaultAuthorEmail}
            onChange={(e) => setEmail(e.target.value)}
            autoComplete="off"
          />
        </label>
        <ErrorText error={save.error} />
        <div className="flex items-center gap-3">
          <Button type="submit" disabled={save.isPending || !dirty}>
            Save
          </Button>
          {save.isSuccess && !dirty && (
            <span role="status" className="text-sm text-muted-foreground">
              Saved.
            </span>
          )}
        </div>
      </form>
    </section>
  );
}

type DeviceFlow = {
  id: string;
  userCode: string;
  verificationUri: string;
  expiresAt: string;
  interval: number;
};

/**
 * Connect GitHub with GitHub's device flow: the user enters a short code at
 * GitHub, the server polls GitHub and stores the token. Neither the device
 * code nor the token ever reaches the browser.
 */
function GitHubCard({ settings }: { settings: GitSettings }) {
  const qc = useQueryClient();
  const host = settings.github.host;
  const connected = settings.credentials.find(
    (c) => c.host === host && c.source === 'github',
  );
  const pasted = settings.credentials.find(
    (c) => c.host === host && c.source !== 'github',
  );
  const [flow, setFlow] = useState<DeviceFlow | null>(null);
  const [outcome, setOutcome] = useState<string | null>(null);
  const [pollError, setPollError] = useState<unknown>(null);
  const start = useMutation({
    mutationFn: () => unwrap(api.POST('/me/git/github/device')),
    onSuccess: (f) => {
      setOutcome(null);
      setPollError(null);
      setFlow(f);
    },
  });
  const disconnect = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/me/git/credentials/{credentialId}', {
          params: { path: { credentialId: id } },
        }),
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: gitSettingsQuery.queryKey }),
  });

  useEffect(() => {
    if (!flow) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const r = await unwrap(
          api.POST('/me/git/github/device/{flowId}', {
            params: { path: { flowId: flow.id } },
          }),
        );
        if (stopped) return;
        if (r.status === 'pending') {
          timer = setTimeout(poll, flow.interval * 1000);
          return;
        }
        setFlow(null);
        if (r.status === 'connected') {
          setOutcome(null);
          await qc.invalidateQueries({ queryKey: gitSettingsQuery.queryKey });
        } else if (r.status === 'denied') {
          setOutcome('The request was denied at GitHub.');
        } else {
          setOutcome('The code expired before it was entered. Start again.');
        }
      } catch (err) {
        if (stopped) return;
        setFlow(null);
        setPollError(err);
      }
    };
    timer = setTimeout(poll, flow.interval * 1000);
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [flow, qc]);

  const cancel = () => {
    if (flow) {
      void api.DELETE('/me/git/github/device/{flowId}', {
        params: { path: { flowId: flow.id } },
      });
    }
    setFlow(null);
  };

  return (
    <section aria-label="GitHub" className="grid gap-3 rounded-lg border p-4">
      <div>
        <h2 className="text-sm font-semibold">GitHub</h2>
        <p className="text-sm text-muted-foreground">
          Connect your GitHub account so your agents can clone, push and use the
          GitHub CLI (<code>gh</code>) on {host}. The token stays on this
          server: the egress proxy adds it to your agents&apos; requests to
          GitHub and it never enters a sandbox.
        </p>
      </div>
      {connected && !flow && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
          <span>
            Connected as{' '}
            <span className="font-medium">
              {connected.account ?? connected.label}
            </span>
          </span>
          {connected.scopes && (
            <span className="text-xs text-muted-foreground">
              scopes {connected.scopes}
            </span>
          )}
          {connected.expiresAt && (
            <span className="text-xs text-muted-foreground">
              token renews before{' '}
              {new Date(connected.expiresAt).toLocaleString()}
            </span>
          )}
        </div>
      )}
      {flow ? (
        <div role="status" className="grid gap-2 rounded-md bg-muted p-3">
          <p className="text-sm">
            Enter this code at GitHub to approve the connection:
          </p>
          <p
            className="font-mono text-2xl font-semibold tracking-widest"
            aria-label="GitHub device code"
          >
            {flow.userCode}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Button asChild size="sm">
              <a href={flow.verificationUri} target="_blank" rel="noreferrer">
                Open GitHub
              </a>
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => void navigator.clipboard?.writeText(flow.userCode)}
            >
              Copy code
            </Button>
            <Button size="sm" variant="ghost" onClick={cancel}>
              Cancel
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            Waiting for approval. The code expires at{' '}
            {new Date(flow.expiresAt).toLocaleTimeString()}.
          </p>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            disabled={start.isPending}
            onClick={() => start.mutate()}
          >
            {connected ? 'Reconnect GitHub' : 'Connect GitHub'}
          </Button>
          {connected && (
            <Button
              size="sm"
              variant="ghost"
              disabled={disconnect.isPending}
              onClick={() => {
                if (
                  confirm(
                    'Disconnect GitHub? Your agents lose access to private repositories and the GitHub CLI.',
                  )
                )
                  disconnect.mutate(connected.id);
              }}
            >
              Disconnect
            </Button>
          )}
        </div>
      )}
      {pasted && !connected && (
        <p className="text-xs text-muted-foreground">
          Connecting replaces the token stored for {host} below.
        </p>
      )}
      {outcome && <p className="text-sm text-muted-foreground">{outcome}</p>}
      <ErrorText error={start.error ?? pollError ?? disconnect.error} />
    </section>
  );
}

function CredentialsCard({ settings }: { settings: GitSettings }) {
  const qc = useQueryClient();
  const del = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/me/git/credentials/{credentialId}', {
          params: { path: { credentialId: id } },
        }),
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: gitSettingsQuery.queryKey }),
  });
  return (
    <section
      aria-label="HTTPS credentials"
      className="grid gap-4 rounded-lg border p-4"
    >
      <div>
        <h2 className="text-sm font-semibold">HTTPS credentials</h2>
        <p className="text-sm text-muted-foreground">
          Personal access tokens for private repositories. Tokens are encrypted
          at rest and injected by the egress proxy into git requests to their
          host; they never enter a sandbox.
        </p>
      </div>
      {settings.credentials.length === 0 ? (
        <p className="text-sm text-muted-foreground">No host connected yet.</p>
      ) : (
        <ul aria-label="Connected hosts" className="divide-y rounded-lg border">
          {settings.credentials.map((c) => (
            <CredentialRow
              key={c.id}
              credential={c}
              busy={del.isPending}
              onRemove={() => del.mutate(c.id)}
            />
          ))}
        </ul>
      )}
      <ErrorText error={del.error} />
      <CredentialForm />
    </section>
  );
}

function CredentialRow({
  credential: c,
  busy,
  onRemove,
}: {
  credential: GitCredential;
  busy: boolean;
  onRemove: () => void;
}) {
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 p-3 text-sm">
      <span className="font-medium">{c.host}</span>
      <span className="text-muted-foreground">{c.username}</span>
      {c.label && <span className="text-muted-foreground">{c.label}</span>}
      {c.source === 'github' && (
        <span className="rounded bg-muted px-1.5 py-0.5 text-xs">
          connected account
        </span>
      )}
      <span className="text-xs text-muted-foreground">
        updated {new Date(c.updatedAt).toLocaleDateString()}
      </span>
      <Button
        className="ml-auto"
        size="sm"
        variant="ghost"
        disabled={busy}
        aria-label={`Remove ${c.host}`}
        onClick={() => {
          if (confirm(`Remove the git credential for ${c.host}?`)) onRemove();
        }}
      >
        Remove
      </Button>
    </li>
  );
}

function CredentialForm() {
  const qc = useQueryClient();
  const [preset, setPreset] =
    useState<(typeof PRESETS)[number]['id']>('github');
  const [host, setHost] = useState('github.com');
  const [username, setUsername] = useState('');
  const [token, setToken] = useState('');
  const [label, setLabel] = useState('');
  const current = PRESETS.find((p) => p.id === preset)!;
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/me/git/credentials', {
          body: {
            host: host.trim(),
            token,
            ...(username.trim() && { username: username.trim() }),
            ...(label.trim() && { label: label.trim() }),
          },
        }),
      ),
    onSuccess: async () => {
      setToken('');
      setLabel('');
      setUsername('');
      await qc.invalidateQueries({ queryKey: gitSettingsQuery.queryKey });
    },
  });
  return (
    <form
      aria-label="Add credential"
      className="grid gap-3 border-t pt-4"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <h3 className="text-sm font-medium">Add or replace a credential</h3>
      <div
        role="group"
        aria-label="Host presets"
        className="flex flex-wrap gap-2"
      >
        {PRESETS.map((p) => (
          <Button
            key={p.id}
            type="button"
            size="sm"
            variant={preset === p.id ? 'default' : 'outline'}
            aria-pressed={preset === p.id}
            onClick={() => {
              setPreset(p.id);
              setHost(p.host);
              setUsername('');
            }}
          >
            {p.label}
          </Button>
        ))}
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="grid gap-1 text-sm">
          Host
          <Input
            value={host}
            placeholder="git.example.com"
            onChange={(e) => setHost(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            required
          />
        </label>
        <label className="grid gap-1 text-sm">
          Username (optional)
          <Input
            value={username}
            placeholder={current.username || 'git'}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </label>
        <label className="grid gap-1 text-sm">
          Token
          <Input
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoComplete="off"
            required
          />
        </label>
        <label className="grid gap-1 text-sm">
          Label (optional)
          <Input
            value={label}
            placeholder="e.g. work account"
            onChange={(e) => setLabel(e.target.value)}
            autoComplete="off"
          />
        </label>
      </div>
      <p className="text-xs text-muted-foreground">
        Stored encrypted on this server and never shown again. Saving a host
        that is already connected replaces its token.
      </p>
      <ErrorText error={save.error} />
      <div>
        <Button
          type="submit"
          disabled={save.isPending || !host.trim() || !token}
        >
          Save credential
        </Button>
      </div>
    </form>
  );
}
