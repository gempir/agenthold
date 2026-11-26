import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import {
  executorStatusQuery,
  type Agent,
  type ExecutorApprovalMode,
} from '@/api/queries';
import type { components } from '@/api/schema';
import { ErrorText, Loading } from '@/components/Spinner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';

type MCPServer = components['schemas']['MCPServer'];
type MCPServerInput = components['schemas']['MCPServerInput'];
type Skill = components['schemas']['Skill'];

const libServersKey = ['mcp-servers'];
const libSkillsKey = ['skills'];
const agentServersKey = (ag: string) => ['agents', ag, 'mcp-servers'];
const agentSkillsKey = (ag: string) => ['agents', ag, 'skills'];

/** "KEY=value" lines <-> object. */
export function parsePairs(text: string, sep: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split('\n')) {
    const i = line.indexOf(sep);
    if (i <= 0) continue;
    const k = line.slice(0, i).trim();
    if (k) out[k] = line.slice(i + sep.length).trim();
  }
  return out;
}

function formatPairs(m: Record<string, string> | undefined, sep: string) {
  return Object.entries(m ?? {})
    .map(([k, v]) => `${k}${sep}${v}`)
    .join('\n');
}

function describe(s: MCPServer) {
  return s.transport === 'http'
    ? s.url
    : [s.command, ...(s.args ?? [])].join(' ');
}

/** Form for a new or edited MCP server. */
export function MCPServerForm({
  initial,
  onSubmit,
  busy,
  error,
  submitLabel,
}: {
  initial?: MCPServer;
  onSubmit: (v: MCPServerInput) => void;
  busy: boolean;
  error: unknown;
  submitLabel: string;
}) {
  const [name, setName] = useState(initial?.name ?? '');
  const [transport, setTransport] = useState<'stdio' | 'http'>(
    initial?.transport ?? 'http',
  );
  const [url, setUrl] = useState(initial?.url ?? '');
  const [command, setCommand] = useState(initial?.command ?? '');
  const [args, setArgs] = useState((initial?.args ?? []).join('\n'));
  const [env, setEnv] = useState(formatPairs(initial?.env, '='));
  const [headers, setHeaders] = useState(formatPairs(initial?.headers, ': '));

  return (
    <form
      className="grid gap-3 rounded-lg border p-4"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit(
          transport === 'http'
            ? { name, transport, url, headers: parsePairs(headers, ':') }
            : {
                name,
                transport,
                command,
                args: args
                  .split('\n')
                  .map((a) => a.trim())
                  .filter(Boolean),
                env: parsePairs(env, '='),
              },
        );
      }}
    >
      <div className="grid grid-cols-2 gap-3">
        <label className="grid gap-1 text-sm">
          Name
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="docs"
            required
          />
        </label>
        <label className="grid gap-1 text-sm">
          Transport
          <Select
            value={transport}
            onChange={(e) => setTransport(e.target.value as 'stdio' | 'http')}
          >
            <option value="http">Remote (HTTP)</option>
            <option value="stdio">Local command (stdio)</option>
          </Select>
        </label>
      </div>
      {transport === 'http' ? (
        <>
          <label className="grid gap-1 text-sm">
            URL
            <Input
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="https://mcp.example.com/mcp"
              required
            />
          </label>
          <label className="grid gap-1 text-sm">
            Headers (one per line, <code>Name: value</code>)
            <textarea
              aria-label="Headers"
              className="min-h-16 rounded-md border bg-background p-2 font-mono text-xs"
              value={headers}
              onChange={(e) => setHeaders(e.target.value)}
              placeholder={'Authorization: Bearer {{secret:my-token}}'}
            />
          </label>
        </>
      ) : (
        <>
          <label className="grid gap-1 text-sm">
            Command (runs inside the sandbox)
            <Input
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              placeholder="npx"
              required
            />
          </label>
          <label className="grid gap-1 text-sm">
            Arguments (one per line)
            <textarea
              aria-label="Arguments"
              className="min-h-16 rounded-md border bg-background p-2 font-mono text-xs"
              value={args}
              onChange={(e) => setArgs(e.target.value)}
              placeholder={'-y\n@modelcontextprotocol/server-everything'}
            />
          </label>
          <label className="grid gap-1 text-sm">
            Environment (one per line, <code>KEY=value</code>)
            <textarea
              aria-label="Environment"
              className="min-h-16 rounded-md border bg-background p-2 font-mono text-xs"
              value={env}
              onChange={(e) => setEnv(e.target.value)}
              placeholder={'API_KEY={{secret:my-token}}'}
            />
          </label>
        </>
      )}
      <p className="text-xs text-muted-foreground">
        Reference secrets as <code>{'{{secret:NAME}}'}</code>. The sandbox only
        sees a placeholder; the egress proxy injects the value for the secret's
        hosts.
      </p>
      <ErrorText error={error} />
      <div>
        <Button type="submit" disabled={busy}>
          {submitLabel}
        </Button>
      </div>
    </form>
  );
}

function SkillForm({
  initial,
  onSubmit,
  busy,
  error,
  submitLabel,
}: {
  initial?: Skill;
  onSubmit: (v: { name: string; description: string; content: string }) => void;
  busy: boolean;
  error: unknown;
  submitLabel: string;
}) {
  const [name, setName] = useState(initial?.name ?? '');
  const [description, setDescription] = useState(initial?.description ?? '');
  const [content, setContent] = useState(initial?.content ?? '');
  return (
    <form
      className="grid gap-3 rounded-lg border p-4"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit({ name, description, content });
      }}
    >
      <label className="grid gap-1 text-sm">
        Name (lowercase, dashes)
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="release-notes"
          required
        />
      </label>
      <label className="grid gap-1 text-sm">
        When to use it
        <Input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="Write release notes from the git history"
          required
        />
      </label>
      <label className="grid gap-1 text-sm">
        Instructions (SKILL.md, Markdown)
        <textarea
          aria-label="Instructions"
          className="min-h-40 rounded-md border bg-background p-2 font-mono text-xs"
          value={content}
          onChange={(e) => setContent(e.target.value)}
          required
        />
      </label>
      <ErrorText error={error} />
      <div>
        <Button type="submit" disabled={busy}>
          {submitLabel}
        </Button>
      </div>
    </form>
  );
}

/** Your MCP server and skills libraries, attachable to agents you own. */
export function LibraryToolsPanel() {
  const qc = useQueryClient();
  const servers = useQuery(libServersQueryOpts);
  const skills = useQuery(libSkillsQueryOpts);
  const [adding, setAdding] = useState<'' | 'server' | 'skill'>('');
  const [editing, setEditing] = useState<string>('');

  const createServer = useMutation({
    mutationFn: (body: MCPServerInput) =>
      unwrap(
        api.POST('/mcp-servers', {
          body,
        }),
      ),
    onSuccess: async () => {
      setAdding('');
      await qc.invalidateQueries({ queryKey: libServersKey });
    },
  });
  const updateServer = useMutation({
    mutationFn: ({ id, body }: { id: string; body: MCPServerInput }) =>
      unwrap(
        api.PUT('/mcp-servers/{serverId}', {
          params: { path: { serverId: id } },
          body,
        }),
      ),
    onSuccess: async () => {
      setEditing('');
      await qc.invalidateQueries({ queryKey: libServersKey });
    },
  });
  const deleteServer = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/mcp-servers/{serverId}', {
          params: { path: { serverId: id } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: libServersKey }),
  });
  const createSkill = useMutation({
    mutationFn: (body: {
      name: string;
      description: string;
      content: string;
    }) =>
      unwrap(
        api.POST('/skills', {
          body,
        }),
      ),
    onSuccess: async () => {
      setAdding('');
      await qc.invalidateQueries({ queryKey: libSkillsKey });
    },
  });
  const updateSkill = useMutation({
    mutationFn: ({
      id,
      body,
    }: {
      id: string;
      body: { name: string; description: string; content: string };
    }) =>
      unwrap(
        api.PUT('/skills/{skillId}', {
          params: { path: { skillId: id } },
          body,
        }),
      ),
    onSuccess: async () => {
      setEditing('');
      await qc.invalidateQueries({ queryKey: libSkillsKey });
    },
  });
  const deleteSkill = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/skills/{skillId}', { params: { path: { skillId: id } } }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: libSkillsKey }),
  });

  return (
    <div className="flex flex-col gap-8">
      <section className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <h2 className="text-base font-semibold">MCP servers</h2>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setAdding('server')}
          >
            Add server
          </Button>
        </div>
        <p className="text-sm text-muted-foreground">
          Servers here can be attached to any agent you own.
        </p>
        {adding === 'server' && (
          <MCPServerForm
            submitLabel="Add"
            busy={createServer.isPending}
            error={createServer.error}
            onSubmit={(v) => createServer.mutate(v)}
          />
        )}
        {servers.isPending && <Loading />}
        <ErrorText error={servers.error ?? deleteServer.error} />
        <ul aria-label="MCP servers" className="divide-y rounded-lg border">
          {servers.data?.length === 0 && (
            <li className="p-3 text-sm text-muted-foreground">
              No servers yet.
            </li>
          )}
          {servers.data?.map((s) => (
            <li key={s.id} className="flex flex-col gap-2 p-3">
              <div className="flex items-center gap-2">
                <span className="font-medium">{s.name}</span>
                <Badge variant="outline">{s.transport}</Badge>
                <code className="truncate text-xs text-muted-foreground">
                  {describe(s)}
                </code>
                <span className="ml-auto flex gap-2">
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => setEditing(s.id)}
                  >
                    Edit
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      if (
                        confirm(
                          `Delete ${s.name}? It is removed from every agent.`,
                        )
                      )
                        deleteServer.mutate(s.id);
                    }}
                  >
                    Delete
                  </Button>
                </span>
              </div>
              {s.secretRefs.length > 0 && (
                <span className="text-xs text-muted-foreground">
                  Uses secrets: {s.secretRefs.join(', ')}
                </span>
              )}
              {editing === s.id && (
                <MCPServerForm
                  initial={s}
                  submitLabel="Save"
                  busy={updateServer.isPending}
                  error={updateServer.error}
                  onSubmit={(v) => updateServer.mutate({ id: s.id, body: v })}
                />
              )}
            </li>
          ))}
        </ul>
      </section>

      <section className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <h2 className="text-base font-semibold">Skills</h2>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setAdding('skill')}
          >
            Add skill
          </Button>
        </div>
        <p className="text-sm text-muted-foreground">
          Instructions agents load when a task matches. Attached skills are
          written into the agent's sandbox for its harness.
        </p>
        {adding === 'skill' && (
          <SkillForm
            submitLabel="Add"
            busy={createSkill.isPending}
            error={createSkill.error}
            onSubmit={(v) => createSkill.mutate(v)}
          />
        )}
        {skills.isPending && <Loading />}
        <ErrorText error={skills.error ?? deleteSkill.error} />
        <ul aria-label="Skills" className="divide-y rounded-lg border">
          {skills.data?.length === 0 && (
            <li className="p-3 text-sm text-muted-foreground">
              No skills yet.
            </li>
          )}
          {skills.data?.map((s) => (
            <li key={s.id} className="flex flex-col gap-2 p-3">
              <div className="flex items-center gap-2">
                <span className="font-medium">{s.name}</span>
                <span className="truncate text-sm text-muted-foreground">
                  {s.description}
                </span>
                <span className="ml-auto flex gap-2">
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => setEditing(s.id)}
                  >
                    Edit
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      if (confirm(`Delete the skill ${s.name}?`))
                        deleteSkill.mutate(s.id);
                    }}
                  >
                    Delete
                  </Button>
                </span>
              </div>
              {editing === s.id && (
                <SkillForm
                  initial={s}
                  submitLabel="Save"
                  busy={updateSkill.isPending}
                  error={updateSkill.error}
                  onSubmit={(v) => updateSkill.mutate({ id: s.id, body: v })}
                />
              )}
            </li>
          ))}
        </ul>
      </section>
    </div>
  );
}

/** Agent Tools tab: what the agent uses, attach and detach. */
export function AgentToolsPanel({
  agentId,
  agent,
  isOwner,
  onAgentSaved,
}: {
  agentId: string;
  agent?: Agent;
  isOwner: boolean;
  onAgentSaved?: (a: Agent) => void;
}) {
  const qc = useQueryClient();
  const executorStatus = useQuery({
    ...executorStatusQuery,
    enabled: Boolean(agent),
  });
  const saveExecutor = useMutation({
    mutationFn: (body: {
      executorEnabled: boolean;
      executorApprovalMode: ExecutorApprovalMode;
    }) =>
      unwrap(
        api.PATCH('/agents/{agentId}', {
          params: { path: { agentId } },
          body,
        }),
      ),
    onSuccess: (updated) => {
      qc.setQueryData(['agents', agentId], updated);
      onAgentSaved?.(updated);
    },
  });
  const servers = useQuery({
    queryKey: agentServersKey(agentId),
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/mcp-servers', {
          params: { path: { agentId } },
        }),
      ),
  });
  const skills = useQuery({
    queryKey: agentSkillsKey(agentId),
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/skills', { params: { path: { agentId } } }),
      ),
  });
  const wsServers = useQuery({ ...libServersQueryOpts, enabled: isOwner });
  const wsSkills = useQuery({ ...libSkillsQueryOpts, enabled: isOwner });
  const [adding, setAdding] = useState(false);
  const [pickServer, setPickServer] = useState('');
  const [pickSkill, setPickSkill] = useState('');

  const refresh = async () => {
    await qc.invalidateQueries({ queryKey: agentServersKey(agentId) });
    await qc.invalidateQueries({ queryKey: agentSkillsKey(agentId) });
  };
  const attachServer = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.PUT('/agents/{agentId}/mcp-servers/{serverId}', {
          params: { path: { agentId, serverId: id } },
        }),
      ),
    onSuccess: async () => {
      setPickServer('');
      await refresh();
    },
  });
  const detachServer = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/agents/{agentId}/mcp-servers/{serverId}', {
          params: { path: { agentId, serverId: id } },
        }),
      ),
    onSuccess: refresh,
  });
  const createOwn = useMutation({
    mutationFn: (body: MCPServerInput) =>
      unwrap(
        api.POST('/agents/{agentId}/mcp-servers', {
          params: { path: { agentId } },
          body,
        }),
      ),
    onSuccess: async () => {
      setAdding(false);
      await refresh();
    },
  });
  const attachSkill = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.PUT('/agents/{agentId}/skills/{skillId}', {
          params: { path: { agentId, skillId: id } },
        }),
      ),
    onSuccess: async () => {
      setPickSkill('');
      await refresh();
    },
  });
  const detachSkill = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE('/agents/{agentId}/skills/{skillId}', {
          params: { path: { agentId, skillId: id } },
        }),
      ),
    onSuccess: refresh,
  });

  const attachedServerIds = new Set(servers.data?.map((s) => s.id));
  const attachedSkillIds = new Set(skills.data?.map((s) => s.id));
  const availableServers =
    wsServers.data?.filter((s) => !attachedServerIds.has(s.id)) ?? [];
  const availableSkills =
    wsSkills.data?.filter((s) => !attachedSkillIds.has(s.id)) ?? [];

  return (
    <div className="flex flex-col gap-8">
      <p className="text-sm text-muted-foreground">
        Changes take effect in the agent's next session.
      </p>
      {agent && (
        <section
          aria-label="Executor accounts"
          className="flex flex-col gap-3 rounded-lg border p-4"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h2 className="text-base font-semibold">Executor accounts</h2>
              <p className="text-sm text-muted-foreground">
                Expose your connected Executor apps and accounts to this agent
                over the built-in <code>executor</code> MCP server. Credentials
                stay outside the sandbox.
              </p>
            </div>
            <Button asChild size="sm" variant="outline">
              <Link to="/settings/accounts">Manage accounts</Link>
            </Button>
          </div>
          {executorStatus.data && !executorStatus.data.enabled && (
            <p className="text-xs text-muted-foreground">
              Executor is disabled on this server.
            </p>
          )}
          {executorStatus.data &&
            executorStatus.data.enabled &&
            !executorStatus.data.available && (
              <p className="text-xs text-muted-foreground">
                {executorStatus.data.reason ||
                  'Executor runtime is not installed on this server.'}
              </p>
            )}
          <div className="flex flex-wrap items-end gap-4">
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={agent.executorEnabled}
                disabled={!isOwner || saveExecutor.isPending}
                onChange={(e) =>
                  saveExecutor.mutate({
                    executorEnabled: e.target.checked,
                    executorApprovalMode: agent.executorApprovalMode,
                  })
                }
              />
              Enable Executor MCP server
            </label>
            <label className="grid gap-1 text-sm">
              Tool approval mode
              <Select
                aria-label="Executor approval mode"
                value={agent.executorApprovalMode}
                disabled={
                  !isOwner || !agent.executorEnabled || saveExecutor.isPending
                }
                onChange={(e) =>
                  saveExecutor.mutate({
                    executorEnabled: agent.executorEnabled,
                    executorApprovalMode: e.target
                      .value as ExecutorApprovalMode,
                  })
                }
              >
                <option value="model">Ask in chat (recommended)</option>
                <option value="automatic">
                  Automatic (harness guards tool calls)
                </option>
                <option value="browser">Approve in browser</option>
              </Select>
            </label>
          </div>
          <ErrorText error={saveExecutor.error} />
        </section>
      )}
      <section className="flex flex-col gap-3">
        <h2 className="text-base font-semibold">MCP servers</h2>
        <ErrorText
          error={servers.error ?? detachServer.error ?? attachServer.error}
        />
        <ul
          aria-label="Agent MCP servers"
          className="divide-y rounded-lg border"
        >
          {servers.data?.length === 0 && (
            <li className="p-3 text-sm text-muted-foreground">None.</li>
          )}
          {servers.data?.map((s) => (
            <li key={s.id} className="flex items-center gap-2 p-3">
              <span className="font-medium">{s.name}</span>
              <Badge variant="outline">{s.transport}</Badge>
              <Badge variant="secondary">
                {s.scope === 'agent' ? 'this agent' : 'library'}
              </Badge>
              <code className="truncate text-xs text-muted-foreground">
                {describe(s)}
              </code>
              {isOwner && (
                <Button
                  className="ml-auto"
                  size="sm"
                  variant="ghost"
                  onClick={() => detachServer.mutate(s.id)}
                >
                  {s.scope === 'agent' ? 'Delete' : 'Detach'}
                </Button>
              )}
            </li>
          ))}
        </ul>
        {isOwner && (
          <div className="flex flex-wrap items-end gap-2">
            <label className="grid gap-1 text-sm">
              Attach a library server
              <Select
                aria-label="Library server"
                value={pickServer}
                onChange={(e) => setPickServer(e.target.value)}
              >
                <option value="">Choose…</option>
                {availableServers.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </Select>
            </label>
            <Button
              size="sm"
              disabled={!pickServer || attachServer.isPending}
              onClick={() => attachServer.mutate(pickServer)}
            >
              Attach
            </Button>
            <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
              Add a server for this agent only
            </Button>
          </div>
        )}
        {adding && (
          <MCPServerForm
            submitLabel="Add"
            busy={createOwn.isPending}
            error={createOwn.error}
            onSubmit={(v) => createOwn.mutate(v)}
          />
        )}
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="text-base font-semibold">Skills</h2>
        <ErrorText
          error={skills.error ?? detachSkill.error ?? attachSkill.error}
        />
        <ul aria-label="Agent skills" className="divide-y rounded-lg border">
          {skills.data?.length === 0 && (
            <li className="p-3 text-sm text-muted-foreground">None.</li>
          )}
          {skills.data?.map((s) => (
            <li key={s.id} className="flex items-center gap-2 p-3">
              <span className="font-medium">{s.name}</span>
              <span className="truncate text-sm text-muted-foreground">
                {s.description}
              </span>
              {isOwner && (
                <Button
                  className="ml-auto"
                  size="sm"
                  variant="ghost"
                  onClick={() => detachSkill.mutate(s.id)}
                >
                  Detach
                </Button>
              )}
            </li>
          ))}
        </ul>
        {isOwner && (
          <div className="flex items-end gap-2">
            <label className="grid gap-1 text-sm">
              Attach a skill
              <Select
                aria-label="Skill"
                value={pickSkill}
                onChange={(e) => setPickSkill(e.target.value)}
              >
                <option value="">Choose…</option>
                {availableSkills.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </Select>
            </label>
            <Button
              size="sm"
              disabled={!pickSkill || attachSkill.isPending}
              onClick={() => attachSkill.mutate(pickSkill)}
            >
              Attach
            </Button>
          </div>
        )}
      </section>
    </div>
  );
}

const libServersQueryOpts = {
  queryKey: libServersKey,
  queryFn: () => unwrap(api.GET('/mcp-servers')),
};

const libSkillsQueryOpts = {
  queryKey: libSkillsKey,
  queryFn: () => unwrap(api.GET('/skills')),
};
