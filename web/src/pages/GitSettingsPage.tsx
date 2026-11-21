import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '@/api/client';
import {
  gitSettingsQuery,
  type GitCredential,
  type GitSettings,
} from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { SettingsHeader } from '@/components/layout/SettingsLayout';
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
export function GitSettingsPage() {
  const settings = useQuery(gitSettingsQuery);
  return (
    <>
      <SettingsHeader
        title="Git"
        description="The identity your agents commit with and the credentials they use to clone and push over HTTPS."
      />
      {settings.isPending && <Loading />}
      <ErrorText error={settings.error} />
      {settings.data && (
        <div className="grid max-w-3xl gap-6">
          <AuthorCard settings={settings.data} />
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
