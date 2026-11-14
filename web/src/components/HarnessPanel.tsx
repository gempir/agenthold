import { lazy, Suspense, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { TriangleAlert } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { harnessesQuery, type Agent } from '@/api/queries';
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
  admin,
  onSaved,
}: {
  agent: Agent;
  admin: boolean;
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
      {admin ? (
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
      {admin && <SignIn agent={agent} />}
    </section>
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
      await qc.invalidateQueries({
        queryKey: ['workspaces', a.workspaceId, 'agents'],
      });
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
    <div className="grid gap-2 border-t pt-3">
      <h3 className="text-sm font-medium">Sign in</h3>
      <p className="text-sm text-muted-foreground">
        Log the harness in inside the sandbox. This wakes the agent if needed.
        Credentials will then live inside the sandbox.
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
