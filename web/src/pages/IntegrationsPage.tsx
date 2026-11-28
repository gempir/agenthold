import { useState, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Building2, Pencil, Plus, Trash2 } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  meQuery,
  orgSkillsQuery,
  orgToolsQuery,
  type OrgSkill,
  type OrgSkillInput,
  type OrgTool,
  type OrgToolInput,
} from '@/api/queries';
import { OrgToolConnectButton } from '@/components/OrgToolConnect';
import { ErrorText, Loading } from '@/components/Spinner';
import { parsePairs, LibraryToolsPanel } from '@/components/ToolsPanel';
import { SettingsHeader } from '@/components/layout/SettingsLayout';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { cn } from '@/lib/utils';
import { ExecutorAccountsSection } from '@/pages/ExecutorAccountsPage';
import { GitSettingsSection } from '@/pages/GitSettingsPage';

export type IntegrationsTab = 'org' | 'accounts' | 'git' | 'advanced';

/** Each tab keeps its own URL so older links (/settings/accounts,
 * /settings/git, /settings/tools) land on the matching view. */
export const INTEGRATIONS_TABS: {
  id: IntegrationsTab;
  to: string;
  label: string;
}[] = [
  { id: 'org', to: '/settings/integrations', label: 'Organization' },
  { id: 'accounts', to: '/settings/accounts', label: 'Connected accounts' },
  { id: 'git', to: '/settings/git', label: 'Git & GitHub' },
  { id: 'advanced', to: '/settings/tools', label: 'Advanced' },
];

/** Paths the Integrations settings entry is active for. */
export const INTEGRATIONS_PATHS = INTEGRATIONS_TABS.map((t) => t.to);

const DESCRIPTIONS: Record<IntegrationsTab, string> = {
  org: 'Tools and skills your organization provides. Log in once and every thread gets them.',
  accounts:
    'External accounts and apps your agents use over MCP. Credentials never enter a sandbox.',
  git: 'The identity your agents commit with and the credentials they use to clone and push.',
  advanced:
    'Your own MCP servers and skills, for anything the organization does not provide.',
};

export function IntegrationsPage({ tab }: { tab: IntegrationsTab }) {
  return (
    <>
      <SettingsHeader title="Integrations" description={DESCRIPTIONS[tab]} />
      <nav
        aria-label="Integrations sections"
        className="mb-6 flex gap-1 overflow-x-auto border-b"
      >
        {INTEGRATIONS_TABS.map((t) => (
          <Link
            key={t.id}
            to={t.to}
            aria-current={t.id === tab ? 'page' : undefined}
            className={cn(
              '-mb-px shrink-0 border-b-2 px-3 py-2 text-sm whitespace-nowrap transition-colors',
              t.id === tab
                ? 'border-foreground font-medium text-foreground'
                : 'border-transparent text-muted-foreground hover:text-foreground',
            )}
          >
            {t.label}
          </Link>
        ))}
      </nav>
      {tab === 'org' && <OrgIntegrations />}
      {tab === 'accounts' && <ExecutorAccountsSection />}
      {tab === 'git' && <GitSettingsSection />}
      {tab === 'advanced' && <LibraryToolsPanel />}
    </>
  );
}

function OrgIntegrations() {
  const admin = useQuery(meQuery).data?.admin ?? false;
  return (
    <div className="grid max-w-3xl gap-8">
      <OrgToolsSection admin={admin} />
      <OrgSkillsSection admin={admin} />
    </div>
  );
}

// --- Organization tools ---------------------------------------------------

const KIND_LABEL: Record<OrgTool['kind'], string> = {
  mcp_http: 'Remote MCP',
  mcp_stdio: 'Local MCP',
  executor_app: 'App',
};

function OrgToolsSection({ admin }: { admin: boolean }) {
  const tools = useQuery(orgToolsQuery);
  const [editing, setEditing] = useState<OrgTool | 'new' | null>(null);
  return (
    <section aria-label="Organization tools" className="grid gap-3">
      <SectionTitle
        title="Organization tools"
        description="Login once, works everywhere: enabled tools are available in every thread."
        action={
          admin &&
          editing !== 'new' && (
            <Button size="sm" onClick={() => setEditing('new')}>
              <Plus className="size-4" aria-hidden />
              Add tool
            </Button>
          )
        }
      />
      {editing === 'new' && <OrgToolForm onDone={() => setEditing(null)} />}
      {tools.isPending && <Loading />}
      <ErrorText error={tools.error} />
      {tools.data && (
        <ul aria-label="Organization tool list" className="grid gap-3">
          {tools.data.length === 0 && (
            <li className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
              {admin
                ? 'No organization tools yet. Add one so every user gets it without setup.'
                : 'Your organization has not added any tools yet.'}
            </li>
          )}
          {tools.data.map((t) =>
            editing !== 'new' && editing?.id === t.id ? (
              <li key={t.id}>
                <OrgToolForm tool={t} onDone={() => setEditing(null)} />
              </li>
            ) : (
              <OrgToolRow
                key={t.id}
                tool={t}
                admin={admin}
                onEdit={() => setEditing(t)}
              />
            ),
          )}
        </ul>
      )}
    </section>
  );
}

function OrgToolRow({
  tool,
  admin,
  onEdit,
}: {
  tool: OrgTool;
  admin: boolean;
  onEdit: () => void;
}) {
  const qc = useQueryClient();
  const toggle = useMutation({
    mutationFn: (enabled: boolean) =>
      unwrap(
        api.PUT('/me/org-tools/{toolId}', {
          params: { path: { toolId: tool.id } },
          body: { enabled },
        }),
      ),
    onSuccess: (updated) => {
      qc.setQueryData(orgToolsQuery.queryKey, (prev: OrgTool[] | undefined) =>
        prev?.map((t) => (t.id === updated.id ? updated : t)),
      );
    },
  });
  const remove = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE('/org/tools/{toolId}', {
          params: { path: { toolId: tool.id } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: orgToolsQuery.queryKey }),
  });
  const needsConnect = tool.authMode === 'user_oauth';
  return (
    <li
      aria-label={tool.name}
      className="flex flex-col gap-3 rounded-lg border p-4"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid min-w-0 gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{tool.name}</span>
            <Badge variant="outline">{KIND_LABEL[tool.kind]}</Badge>
            {!needsConnect && (
              <Badge className="gap-1 bg-sky-500/15 text-sky-700 dark:text-sky-400">
                <Building2 className="size-3" aria-hidden />
                Provided by organization
              </Badge>
            )}
          </div>
          {tool.description && (
            <p className="text-sm text-muted-foreground">{tool.description}</p>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-3">
          <Switch
            label={`Use ${tool.name}`}
            checked={tool.userEnabled}
            disabled={toggle.isPending}
            onChange={(v) => toggle.mutate(v)}
          />
        </div>
      </div>
      {needsConnect && (
        <div className="flex flex-wrap items-center gap-2">
          <OrgToolConnectButton
            toolId={tool.id}
            name={tool.name}
            connected={tool.connected}
          />
          {!tool.connected && (
            <span className="text-xs text-muted-foreground">
              Connect your own {tool.name} account once; every thread uses it.
            </span>
          )}
        </div>
      )}
      <ErrorText error={toggle.error ?? remove.error} />
      {admin && (
        <div className="flex flex-wrap items-center gap-1 border-t pt-2 text-xs text-muted-foreground">
          <code className="mr-auto">{tool.slug}</code>
          <Button size="sm" variant="ghost" onClick={onEdit}>
            <Pencil className="size-3.5" aria-hidden />
            Edit
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={remove.isPending}
            onClick={() => {
              if (confirm(`Delete ${tool.name} for everyone?`)) remove.mutate();
            }}
          >
            <Trash2 className="size-3.5" aria-hidden />
            Delete
          </Button>
        </div>
      )}
    </li>
  );
}

const pairsText = (m: Record<string, string> | undefined, sep: string) =>
  Object.entries(m ?? {})
    .map(([k, v]) => `${k}${sep}${v}`)
    .join('\n');

const splitList = (s: string) =>
  s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);

function OrgToolForm({ tool, onDone }: { tool?: OrgTool; onDone: () => void }) {
  const qc = useQueryClient();
  const [slug, setSlug] = useState(tool?.slug ?? '');
  const [name, setName] = useState(tool?.name ?? '');
  const [description, setDescription] = useState(tool?.description ?? '');
  const [kind, setKind] = useState<OrgTool['kind']>(tool?.kind ?? 'mcp_http');
  const [authMode, setAuthMode] = useState<OrgTool['authMode']>(
    tool?.authMode ?? 'none',
  );
  const [url, setUrl] = useState(tool?.config.url ?? '');
  const [headers, setHeaders] = useState(pairsText(tool?.config.headers, ': '));
  const [command, setCommand] = useState(tool?.config.command ?? '');
  const [args, setArgs] = useState((tool?.config.args ?? []).join(' '));
  const [env, setEnv] = useState(pairsText(tool?.config.env, '='));
  const [catalogId, setCatalogId] = useState(
    tool?.config.executorCatalogId ?? '',
  );
  const [secret, setSecret] = useState('');
  const [clearSecret, setClearSecret] = useState(false);
  const [secretHosts, setSecretHosts] = useState(
    (tool?.secretHosts ?? []).join(', '),
  );
  const [enabledByDefault, setEnabledByDefault] = useState(
    tool?.enabledByDefault ?? true,
  );
  const [roles, setRoles] = useState((tool?.allowedRoles ?? []).join(', '));

  const save = useMutation({
    mutationFn: () => {
      const config: OrgToolInput['config'] =
        kind === 'mcp_http'
          ? { url: url.trim(), headers: parsePairs(headers, ':') }
          : kind === 'mcp_stdio'
            ? {
                command: command.trim(),
                args: splitList(args),
                env: parsePairs(env, '='),
              }
            : { executorCatalogId: catalogId.trim() };
      const body: OrgToolInput = {
        slug: slug.trim(),
        name: name.trim(),
        description: description.trim(),
        kind,
        authMode,
        config,
        enabledByDefault,
        allowedRoles: splitList(roles),
        secretHosts: authMode === 'org_shared' ? splitList(secretHosts) : [],
        ...(authMode === 'org_shared' && secret && { secret }),
        ...(tool && clearSecret && { clearSecret: true }),
      };
      return tool
        ? unwrap(
            api.PUT('/org/tools/{toolId}', {
              params: { path: { toolId: tool.id } },
              body,
            }),
          )
        : unwrap(api.POST('/org/tools', { body }));
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: orgToolsQuery.queryKey });
      onDone();
    },
  });

  return (
    <form
      aria-label={tool ? `Edit ${tool.name}` : 'New organization tool'}
      className="grid gap-3 rounded-lg border bg-muted/20 p-4 text-sm"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Name">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Linear"
            required
          />
        </Field>
        <Field label="Slug">
          <Input
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            placeholder="linear"
            className="font-mono"
            required
          />
        </Field>
      </div>
      <Field label="Description">
        <Input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="Issue tracking for the whole team"
        />
      </Field>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Kind">
          <Select
            value={kind}
            onChange={(e) => setKind(e.target.value as OrgTool['kind'])}
          >
            <option value="mcp_http">Remote MCP server (HTTP)</option>
            <option value="mcp_stdio">Local MCP server (stdio)</option>
            <option value="executor_app">Catalog app</option>
          </Select>
        </Field>
        <Field label="Authentication">
          <Select
            value={authMode}
            onChange={(e) => setAuthMode(e.target.value as OrgTool['authMode'])}
          >
            <option value="none">None</option>
            <option value="org_shared">Shared organization credential</option>
            <option value="user_oauth">Each user connects their own</option>
          </Select>
        </Field>
      </div>
      {kind === 'mcp_http' && (
        <>
          <Field label="Server URL">
            <Input
              type="url"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="https://mcp.example.com/mcp"
              required
            />
          </Field>
          <Field label="Headers (one Name: value per line)">
            <textarea
              value={headers}
              onChange={(e) => setHeaders(e.target.value)}
              className="min-h-16 rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs"
            />
          </Field>
        </>
      )}
      {kind === 'mcp_stdio' && (
        <>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Command">
              <Input
                value={command}
                onChange={(e) => setCommand(e.target.value)}
                placeholder="npx"
                className="font-mono"
                required
              />
            </Field>
            <Field label="Arguments">
              <Input
                value={args}
                onChange={(e) => setArgs(e.target.value)}
                placeholder="-y @example/mcp"
                className="font-mono"
              />
            </Field>
          </div>
          <Field label="Environment (one KEY=value per line)">
            <textarea
              value={env}
              onChange={(e) => setEnv(e.target.value)}
              className="min-h-16 rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs"
            />
          </Field>
        </>
      )}
      {kind === 'executor_app' && (
        <Field label="Catalog app ID">
          <Input
            value={catalogId}
            onChange={(e) => setCatalogId(e.target.value)}
            placeholder="linear"
            className="font-mono"
            required
          />
        </Field>
      )}
      {authMode === 'org_shared' && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field
            label={tool?.hasSecret ? 'Shared secret (saved)' : 'Shared secret'}
          >
            <Input
              type="password"
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              placeholder={tool?.hasSecret ? 'Leave empty to keep' : ''}
              autoComplete="off"
            />
          </Field>
          <Field label="Secret hosts">
            <Input
              value={secretHosts}
              onChange={(e) => setSecretHosts(e.target.value)}
              placeholder="api.example.com"
              className="font-mono"
            />
          </Field>
          {tool?.hasSecret && (
            <label className="flex items-center gap-2 sm:col-span-2">
              <input
                type="checkbox"
                checked={clearSecret}
                onChange={(e) => setClearSecret(e.target.checked)}
              />
              Remove the saved secret
            </label>
          )}
        </div>
      )}
      <Field label="Allowed roles (empty: everyone)">
        <Input
          value={roles}
          onChange={(e) => setRoles(e.target.value)}
          placeholder="user, admin"
        />
      </Field>
      <label className="flex items-center gap-2">
        <input
          type="checkbox"
          checked={enabledByDefault}
          onChange={(e) => setEnabledByDefault(e.target.checked)}
        />
        Enabled for everyone by default
      </label>
      <ErrorText error={save.error} />
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={save.isPending}>
          {tool ? 'Save tool' : 'Add tool'}
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={onDone}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

// --- Organization skills --------------------------------------------------

function OrgSkillsSection({ admin }: { admin: boolean }) {
  const qc = useQueryClient();
  const skills = useQuery(orgSkillsQuery);
  const [editing, setEditing] = useState<OrgSkill | 'new' | null>(null);
  const remove = useMutation({
    mutationFn: (skillId: string) =>
      unwrap(
        api.DELETE('/org/skills/{skillId}', {
          params: { path: { skillId } },
        }),
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: orgSkillsQuery.queryKey }),
  });
  return (
    <section aria-label="Organization skills" className="grid gap-3">
      <SectionTitle
        title="Organization skills"
        description="Delivered to every agent alongside your own skills."
        action={
          admin &&
          editing !== 'new' && (
            <Button size="sm" onClick={() => setEditing('new')}>
              <Plus className="size-4" aria-hidden />
              Add skill
            </Button>
          )
        }
      />
      {editing === 'new' && <OrgSkillForm onDone={() => setEditing(null)} />}
      {skills.isPending && <Loading />}
      <ErrorText error={skills.error ?? remove.error} />
      {skills.data && (
        <ul
          aria-label="Organization skill list"
          className="divide-y rounded-lg border"
        >
          {skills.data.length === 0 && (
            <li className="p-4 text-sm text-muted-foreground">
              No organization skills yet.
            </li>
          )}
          {skills.data.map((sk) =>
            editing !== 'new' && editing?.id === sk.id ? (
              <li key={sk.id} className="p-3">
                <OrgSkillForm skill={sk} onDone={() => setEditing(null)} />
              </li>
            ) : (
              <li
                key={sk.id}
                aria-label={sk.name}
                className="flex flex-wrap items-start justify-between gap-2 p-4"
              >
                <div className="grid min-w-0 gap-0.5">
                  <div className="flex flex-wrap items-center gap-2">
                    <code className="text-sm font-medium">{sk.name}</code>
                    {!sk.enabledByDefault && (
                      <Badge variant="outline">off by default</Badge>
                    )}
                  </div>
                  <p className="text-sm text-muted-foreground">
                    {sk.description}
                  </p>
                </div>
                {admin && (
                  <div className="flex shrink-0 gap-1">
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => setEditing(sk)}
                    >
                      <Pencil className="size-3.5" aria-hidden />
                      Edit
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={remove.isPending}
                      onClick={() => {
                        if (confirm(`Delete skill ${sk.name} for everyone?`))
                          remove.mutate(sk.id);
                      }}
                    >
                      <Trash2 className="size-3.5" aria-hidden />
                      Delete
                    </Button>
                  </div>
                )}
              </li>
            ),
          )}
        </ul>
      )}
    </section>
  );
}

function OrgSkillForm({
  skill,
  onDone,
}: {
  skill?: OrgSkill;
  onDone: () => void;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState(skill?.name ?? '');
  const [description, setDescription] = useState(skill?.description ?? '');
  const [content, setContent] = useState(skill?.content ?? '');
  const [enabledByDefault, setEnabledByDefault] = useState(
    skill?.enabledByDefault ?? true,
  );
  const save = useMutation({
    mutationFn: () => {
      const body: OrgSkillInput = {
        name: name.trim(),
        description: description.trim(),
        content,
        enabledByDefault,
      };
      return skill
        ? unwrap(
            api.PUT('/org/skills/{skillId}', {
              params: { path: { skillId: skill.id } },
              body,
            }),
          )
        : unwrap(api.POST('/org/skills', { body }));
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: orgSkillsQuery.queryKey });
      onDone();
    },
  });
  return (
    <form
      aria-label={skill ? `Edit ${skill.name}` : 'New organization skill'}
      className="grid gap-3 rounded-lg border bg-muted/20 p-4 text-sm"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <Field label="Skill name">
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="release-notes"
          className="font-mono"
          required
        />
      </Field>
      <Field label="Skill description">
        <Input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="When to use this skill"
          required
        />
      </Field>
      <Field label="Instructions (SKILL.md body)">
        <textarea
          value={content}
          onChange={(e) => setContent(e.target.value)}
          className="min-h-32 rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs"
          required
        />
      </Field>
      <label className="flex items-center gap-2">
        <input
          type="checkbox"
          checked={enabledByDefault}
          onChange={(e) => setEnabledByDefault(e.target.checked)}
        />
        Enabled for everyone by default
      </label>
      <ErrorText error={save.error} />
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={save.isPending}>
          {skill ? 'Save skill' : 'Add skill'}
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={onDone}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

// --- Small building blocks ------------------------------------------------

function SectionTitle({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-2">
      <div>
        <h2 className="text-base font-semibold">{title}</h2>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      {action}
    </div>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="grid gap-1">
      <span>{label}</span>
      {children}
    </label>
  );
}

function Switch({
  label,
  checked,
  disabled,
  onChange,
}: {
  label: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors disabled:opacity-50',
        checked ? 'bg-primary' : 'bg-muted-foreground/30',
      )}
    >
      <span
        aria-hidden
        className={cn(
          'inline-block size-4 rounded-full bg-background shadow transition-transform',
          checked ? 'translate-x-4' : 'translate-x-0.5',
        )}
      />
    </button>
  );
}
