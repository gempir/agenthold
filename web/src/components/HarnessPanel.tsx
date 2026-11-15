import { lazy, Suspense, useState, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { TriangleAlert } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { agentsQuery, harnessesQuery, type Agent } from '@/api/queries';
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

const TerminalTab = lazy(() =>
  import('@/components/TerminalTab').then((m) => ({ default: m.TerminalTab })),
);

type Policy = Agent['permissionPolicy'];

export function HarnessPanel({
  agent,
  isOwner,
  onSaved,
}: {
  agent: Agent;
  isOwner: boolean;
  onSaved: (a: Agent) => void;
}) {
  return (
    <section className="grid gap-3 rounded-lg border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-semibold">Harness</h2>
        {agent.credentialsInSandbox && (
          <span className="inline-flex items-center gap-1 rounded bg-amber-500/15 px-2 py-0.5 text-xs text-amber-700">
            <TriangleAlert className="size-3" aria-hidden />
            credentials inside sandbox
          </span>
        )}
      </div>
      {isOwner ? (
        <HarnessForm key={agent.updatedAt} agent={agent} onSaved={onSaved} />
      ) : (
        <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1 text-sm">
          <dt className="text-muted-foreground">Harness</dt>
          <dd>{agent.harness}</dd>
          <dt className="text-muted-foreground">Model</dt>
          <dd>{agent.model || 'default'}</dd>
          <dt className="text-muted-foreground">Permission policy</dt>
          <dd>{agent.permissionPolicy}</dd>
        </dl>
      )}
      <AccountLogin agent={agent} isOwner={isOwner} onSaved={onSaved} />
      {isOwner && (
        <details className="border-t pt-3">
          <summary className="cursor-pointer text-sm text-muted-foreground">
            Sign in inside this sandbox instead
          </summary>
          <SignIn agent={agent} />
        </details>
      )}
    </section>
  );
}

/** Which login the harness uses: the account login of agent.login.user. */
function AccountLogin({
  agent,
  isOwner,
  onSaved,
}: {
  agent: Agent;
  isOwner: boolean;
  onSaved: (a: Agent) => void;
}) {
  const harnesses = useQuery(harnessesQuery);
  const name =
    harnesses.data?.find((h) => h.id === agent.harness)?.name ?? agent.harness;
  const l = agent.login;
  const change = useMutation({
    mutationFn: (use: boolean) =>
      unwrap(
        use
          ? api.PUT('/agents/{agentId}/login', {
              params: { path: { agentId: agent.id } },
            })
          : api.DELETE('/agents/{agentId}/login', {
              params: { path: { agentId: agent.id } },
            }),
      ),
    onSuccess: onSaved,
  });
  if (l.source === 'unsupported') return null;
  const whose = l.mine ? 'your' : `${l.user?.name ?? 'someone'}'s`;
  const settings = (
    <Link to="/settings/providers" className="underline">
      Provider logins
    </Link>
  );
  let text: ReactNode;
  if (l.source === 'secret') {
    text = `Signs in with an API key secret attached to this agent.`;
  } else if (l.source === 'account' && l.connected) {
    text = (
      <>
        Signs in with {whose} {name} login
        {l.status === 'error' && (
          <span className="text-amber-700">
            {' '}
            (it needs to be connected again{l.mine && <>: {settings}</>})
          </span>
        )}
        .
      </>
    );
  } else if (l.source === 'account') {
    text = l.mine ? (
      <>
        Your {name} login is not connected. Connect it under {settings}.
      </>
    ) : (
      `${whose} account has no ${name} login.`
    );
  } else {
    text = `Uses the login inside its sandbox${l.reason ? ` (${l.reason})` : ''}.`;
  }
  const canStop = l.user && (isOwner || l.mine);
  return (
    <div className="grid gap-2 border-t pt-3">
      <h3 className="text-sm font-medium">Login</h3>
      <p className="text-sm" data-testid="agent-login">
        {text}
      </p>
      <div className="flex flex-wrap gap-2">
        {isOwner && !l.mine && (
          <Button
            size="sm"
            variant="outline"
            disabled={change.isPending}
            onClick={() => change.mutate(true)}
          >
            Use my logins
          </Button>
        )}
        {canStop && (
          <Button
            size="sm"
            variant="ghost"
            disabled={change.isPending}
            onClick={() => change.mutate(false)}
          >
            {l.mine ? 'Stop using my logins' : 'Stop using these logins'}
          </Button>
        )}
      </div>
      <ErrorText error={change.error} />
    </div>
  );
}

function HarnessForm({
  agent,
  onSaved,
}: {
  agent: Agent;
  onSaved: (a: Agent) => void;
}) {
  const qc = useQueryClient();
  const harnesses = useQuery(harnessesQuery);
  const [harness, setHarness] = useState(agent.harness);
  const [model, setModel] = useState(agent.model);
  const [policy, setPolicy] = useState<Policy>(agent.permissionPolicy);
  const options = harnesses.data ?? [];
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/agents/{agentId}', {
          params: { path: { agentId: agent.id } },
          body: { harness, model: model.trim(), permissionPolicy: policy },
        }),
      ),
    onSuccess: async (a) => {
      onSaved(a);
      await qc.invalidateQueries({ queryKey: agentsQuery.queryKey });
    },
  });
  return (
    <form
      className="grid gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="grid gap-3 sm:grid-cols-3">
        <label className="grid gap-1 text-sm">
          Harness
          <Select value={harness} onChange={(e) => setHarness(e.target.value)}>
            {!options.some((h) => h.id === harness) && (
              <option value={harness}>{harness}</option>
            )}
            {options.map((h) => (
              <option key={h.id} value={h.id}>
                {h.name}
              </option>
            ))}
          </Select>
        </label>
        <label className="grid gap-1 text-sm">
          Model
          <Input
            value={model}
            placeholder="default"
            onChange={(e) => setModel(e.target.value)}
          />
        </label>
        <label className="grid gap-1 text-sm">
          Permission policy
          <Select
            value={policy}
            onChange={(e) => setPolicy(e.target.value as Policy)}
          >
            <option value="auto-approve">auto-approve</option>
            <option value="ask">ask</option>
          </Select>
        </label>
      </div>
      <ErrorText error={save.error} />
      <div>
        <Button type="submit" disabled={save.isPending}>
          Save harness
        </Button>
      </div>
    </form>
  );
}

function SignIn({ agent }: { agent: Agent }) {
  const qc = useQueryClient();
  const [method, setMethod] = useState<{ id: string; name: string } | null>(
    null,
  );
  const check = useMutation({
    mutationFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/harness', {
          params: {
            path: { agentId: agent.id },
            query: { connect: true },
          },
        }),
      ),
  });
  const methods = check.data?.authMethods ?? [];
  return (
    <div className="grid gap-2 pt-3">
      <p className="text-sm text-muted-foreground">
        Log the harness in inside this sandbox only. This wakes the agent if
        needed. The credentials then live inside the sandbox, where the agent
        can read them. A connected account login takes precedence.
      </p>
      <div>
        <Button
          variant="outline"
          disabled={check.isPending}
          onClick={() => check.mutate()}
        >
          Check login methods
        </Button>
      </div>
      <ErrorText error={check.error} />
      {check.data && methods.length === 0 && (
        <p className="text-sm text-muted-foreground">
          No login methods reported.
        </p>
      )}
      <ul className="grid gap-2">
        {methods
          .filter((m) => m.type === 'terminal')
          .map((m) => (
            <li key={m.id} className="flex flex-wrap items-center gap-3">
              <Button size="sm" onClick={() => setMethod(m)}>
                Sign in with {m.name}
              </Button>
              <span className="text-sm text-muted-foreground">
                {m.description}
              </span>
            </li>
          ))}
      </ul>
      {method && (
        <Dialog
          open
          onOpenChange={(o) => {
            if (o) return;
            setMethod(null);
            void qc.invalidateQueries({ queryKey: ['agents', agent.id] });
          }}
        >
          <DialogContent className="max-w-3xl">
            <DialogHeader>
              <DialogTitle>Sign in with {method.name}</DialogTitle>
              <DialogDescription>
                The login URL or code appears in the terminal below. Open the
                URL in your browser and paste any code back into the terminal.
              </DialogDescription>
            </DialogHeader>
            <Suspense fallback={<Loading />}>
              <TerminalTab agentId={agent.id} auth={method.id} />
            </Suspense>
          </DialogContent>
        </Dialog>
      )}
    </div>
  );
}
