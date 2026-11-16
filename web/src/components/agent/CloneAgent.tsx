import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import { agentsQuery, type Agent } from '@/api/queries';
import { ErrorText } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';

/** Copy a machine, or start a new agent from a template (the same thing). */
export function CloneAgent({ agent }: { agent: Agent }) {
  const [open, setOpen] = useState(false);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState('');
  const [asTemplate, setAsTemplate] = useState(false);
  const template = agent.isTemplate;

  const clone = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/agents/{agentId}/clone', {
          params: { path: { agentId: agent.id } },
          body: { name: name.trim(), ...(asTemplate && { asTemplate: true }) },
        }),
      ),
    onSuccess: async (a) => {
      await qc.invalidateQueries({ queryKey: agentsQuery.queryKey });
      setOpen(false);
      await navigate({ to: '/a/$agentId', params: { agentId: a.id } });
    },
  });

  return (
    <section className="grid gap-2 rounded-xl border p-4">
      <h2 className="text-sm font-semibold">
        {template ? 'New agent from this template' : 'Clone'}
      </h2>
      <p className="text-sm text-muted-foreground">
        {template
          ? 'Copies the template’s files and settings into a new agent. The template is not changed.'
          : 'Makes a new agent with a copy of this one’s system, home files and settings. Conversations, schedules, webhooks and sharing are not copied.'}
        {!template && agent.state === 'running' && (
          <>
            {' '}
            A running agent is stopped while it is copied, then started again.
          </>
        )}
        {agent.credentialsInSandbox && (
          <>
            {' '}
            This agent holds a harness login inside its sandbox; the copy
            carries it.
          </>
        )}
      </p>
      <div>
        <Button
          variant="outline"
          onClick={() => {
            setName(template ? '' : `${agent.name} copy`);
            setAsTemplate(false);
            setOpen(true);
          }}
        >
          {template ? 'New agent from template' : 'Clone'}
        </Button>
      </div>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {template ? 'New agent from template' : `Clone ${agent.name}`}
            </DialogTitle>
            <DialogDescription>
              The copy is created in the background and sleeps until you use it.
            </DialogDescription>
          </DialogHeader>
          <form
            className="grid gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              clone.mutate();
            }}
          >
            <label className="grid gap-1 text-sm">
              Name
              <Input
                required
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={asTemplate}
                onChange={(e) => setAsTemplate(e.target.checked)}
              />
              Create the copy as a template
            </label>
            <ErrorText error={clone.error} />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setOpen(false)}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={!name.trim() || clone.isPending}>
                {template ? 'Create agent' : 'Clone'}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </section>
  );
}

/** Turn a machine into a template and back. */
export function TemplateToggle({
  agent,
  onSaved,
}: {
  agent: Agent;
  onSaved: (a: Agent) => void;
}) {
  const qc = useQueryClient();
  const template = agent.isTemplate;
  const toggle = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/agents/{agentId}', {
          params: { path: { agentId: agent.id } },
          body: { isTemplate: !template },
        }),
      ),
    onSuccess: async (a) => {
      onSaved(a);
      await qc.invalidateQueries({ queryKey: agentsQuery.queryKey });
    },
  });
  return (
    <section className="grid gap-2 rounded-xl border p-4">
      <h2 className="text-sm font-semibold">Template</h2>
      <p className="text-sm text-muted-foreground">
        {template
          ? 'Converting it back makes this a normal agent again: it can be started and chatted with, and it appears among your agents.'
          : 'A template is a stopped machine that new agents are cloned from, for example with your tools installed. Saving it as a template stops it, and it no longer appears among your agents.'}
      </p>
      <div>
        <Button
          variant="outline"
          disabled={toggle.isPending || (!template && busyState(agent))}
          onClick={() => toggle.mutate()}
        >
          {template ? 'Convert to agent' : 'Save as template'}
        </Button>
      </div>
      <ErrorText error={toggle.error} />
    </section>
  );
}

// Only machines at rest convert: the server refuses the rest with a conflict.
const busyState = (a: Agent) =>
  !['running', 'sleeping', 'error'].includes(a.state);
